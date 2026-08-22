"""Runtime-policy client conformance suite (Python half).

Drives every section of the shared fixtures
(../../parity/runtime-policy.fixtures.json): the fetch wire, the signature
cases, all assignment vectors (pinning this port of the Go
``shared/platform/sampling.Fraction`` bit-for-bit — exact float equality), and
all decision cases. The TypeScript half runs the SAME fixtures; a case passing
here and missing there is a release-gate failure.

The fixtures are the authority: if a vector disagrees with this port, the port
is wrong. Never recompute a fraction by hand.

Run with: pytest (from public/sdk/python).
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import threading
import urllib.request
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from caveman_cloud import Cave, PolicyDecision, RuntimePolicyClient, policy_unit_fraction
from caveman_cloud.core import (
    _ED_B,
    _ED_L,
    _POLICY_MAX_RESPONSE_BYTES,
    _ed25519_verify,
    _ed_encode,
    _ed_scalarmult,
    _policy_guard_passes,
)

PARITY_DIR = Path(__file__).resolve().parents[2] / "parity"
FIXTURES = json.loads((PARITY_DIR / "runtime-policy.fixtures.json").read_text(encoding="utf-8"))
CONFIG = json.loads((PARITY_DIR / "fixtures.json").read_text(encoding="utf-8"))["config"]
STD_HEADERS = json.loads((PARITY_DIR / "fixtures.json").read_text(encoding="utf-8"))["std_headers"]

FETCH = FIXTURES["fetch"]
RESPONSE = FETCH["response"]
BUNDLE = RESPONSE["bundle"]
KILL_BUNDLE = FIXTURES["kill_bundle"]
PINNED_KEY = RESPONSE["public_key"]["key"]
# One byte of the signed payload flipped: the signature must stop verifying.
TAMPERED_BUNDLE = BUNDLE.replace('"policy_version":7', '"policy_version":9')


def _sign_bundle(bundle: str) -> dict[str, Any]:
    """Sign ``bundle`` with the fixture's ``signing_test_seed_hex``.

    Test-only: the SDK is a verifier and holds no signing key. RFC 8032 §5.1.6
    over the module's own group arithmetic, so a test can mint a validly signed
    bundle whose CONTENTS the client will still reject (schema, sequence) —
    which is the only way to observe when the trust-on-first-use pin is stored.
    """
    seed = bytes.fromhex(FIXTURES["signing_test_seed_hex"])
    h = hashlib.sha512(seed).digest()
    a = (int.from_bytes(h[:32], "little") & ((1 << 254) - 8)) | (1 << 254)
    public_key = _ed_encode(_ed_scalarmult(_ED_B, a))
    message = bundle.encode("utf-8")
    r = int.from_bytes(hashlib.sha512(h[32:] + message).digest(), "little") % _ED_L
    r_point = _ed_encode(_ed_scalarmult(_ED_B, r))
    k = int.from_bytes(hashlib.sha512(r_point + public_key + message).digest(), "little") % _ED_L
    signature = r_point + ((r + k * a) % _ED_L).to_bytes(32, "little")
    return {
        "bundle": bundle,
        "signature": {"sig": base64.b64encode(signature).decode()},
        "public_key": {"key": base64.b64encode(public_key).decode()},
    }


def _make_cave() -> Cave:
    return Cave(
        api_key=CONFIG["api_key"],
        base_url=CONFIG["base_url"],
        agent=CONFIG["agent"],
        default_workflow=CONFIG["default_workflow"],
        retention=CONFIG["retention"],
        control_url=CONFIG["control_url"],
        user=CONFIG["user"],
    )


def _fake_response(data: Any) -> MagicMock:
    body = json.dumps(data).encode()
    cm = MagicMock()
    cm.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def _signed_payload(bundle: str = BUNDLE) -> dict[str, Any]:
    return {"bundle": bundle, "signature": RESPONSE["signature"], "public_key": RESPONSE["public_key"]}


def _unsigned_payload(bundle: str = KILL_BUNDLE) -> dict[str, Any]:
    return {"bundle": bundle, "signature": None, "public_key": None}


def _refresh_with(client: RuntimePolicyClient, payload: Any, captured: list[dict[str, Any]] | None = None) -> Any:
    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        if captured is not None:
            captured.append(
                {
                    "url": req.full_url,
                    "method": req.get_method(),
                    "body": req.data,
                    "headers": {k.lower(): v for k, v in dict(req.headers).items()},
                }
            )
        return _fake_response(payload)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        return client.refresh()


def _loaded_client(payload: Any = None, **options: Any) -> RuntimePolicyClient:
    """A client holding the fixture bundle (signature verified against the pin)."""
    client = _make_cave().runtime_policy(public_key=PINNED_KEY, **options)
    result = _refresh_with(client, payload if payload is not None else _signed_payload())
    assert result.ok is True
    return client


# ─── fetch: the wire ────────────────────────────────────────────────────────


def test_fetch_wire_matches_the_fixture() -> None:
    captured: list[dict[str, Any]] = []
    client = _make_cave().runtime_policy(public_key=PINNED_KEY)
    result = _refresh_with(client, _signed_payload(), captured)

    wire = FETCH["wire"]
    assert len(captured) == 1
    assert captured[0]["method"] == wire["method"]
    assert captured[0]["url"] == f"{CONFIG['base_url']}{wire['path']}"
    assert captured[0]["body"] is None
    # "std_headers (from fixtures.json, minus content-type)" — a GET has no body.
    assert captured[0]["headers"] == {k: v for k, v in STD_HEADERS.items() if k != "content-type"}
    assert result.ok is True
    assert result.error is None


def test_fetch_state_matches_the_fixture() -> None:
    state = _loaded_client().state()
    expected = FETCH["expect_state"]
    assert state.has_bundle is True
    assert state.signed is expected["signed"]
    assert state.policy_version == expected["policy_version"]
    assert state.sequence == expected["sequence"]
    assert state.kill is expected["kill"]
    assert state.killed_locally is False
    assert len(json.loads(BUNDLE)["runtime_policies"]) == expected["policy_count"]


# ─── signature cases ────────────────────────────────────────────────────────


def _signature_case(name: str) -> dict[str, Any]:
    for case in FIXTURES["signature_cases"]:
        if case["name"] == name:
            return case
    raise AssertionError(f"missing signature case {name}")


def test_signature_case_pinned_key_verifies() -> None:
    case = _signature_case("pinned_key_verifies")
    client = _make_cave().runtime_policy(public_key=case["pinned_public_key"])
    result = _refresh_with(client, _signed_payload())
    assert result.ok is True
    assert result.signed is True
    assert client.state().signed is True


def test_signature_case_tampered_bundle_rejected() -> None:
    case = _signature_case("tampered_bundle_rejected")
    client = _make_cave().runtime_policy(public_key=case["pinned_public_key"])
    result = _refresh_with(client, _signed_payload(TAMPERED_BUNDLE))
    assert result.ok is False
    assert result.error == "signature_invalid"
    # No prior bundle survives a rejection, so decide() says so honestly.
    assert client.state().has_bundle is False
    decision = client.decide("fix_failing_test_with_stacktrace", "task-a", {})
    assert (decision.decision, decision.reason) == ("baseline", "policy_unavailable")


def test_signature_case_unsigned_refused_when_key_pinned() -> None:
    case = _signature_case("unsigned_refused_when_key_pinned")
    client = _make_cave().runtime_policy(public_key=case["pinned_public_key"])
    result = _refresh_with(client, _unsigned_payload())
    assert result.ok is False
    assert result.error == "unsigned_rejected"
    assert client.state().has_bundle is False


def test_signature_case_unsigned_accepted_when_nothing_pinned() -> None:
    case = _signature_case("unsigned_accepted_when_nothing_pinned")
    assert case["pinned_public_key"] is None
    client = _make_cave().runtime_policy()
    result = _refresh_with(client, _unsigned_payload())
    assert result.ok is True
    assert result.signed is False
    assert client.state().signed is False
    assert client.state().kill is True


def test_every_signature_case_is_covered() -> None:
    covered = {
        "pinned_key_verifies",
        "tampered_bundle_rejected",
        "unsigned_refused_when_key_pinned",
        "unsigned_accepted_when_nothing_pinned",
    }
    assert {case["name"] for case in FIXTURES["signature_cases"]} == covered


def test_ed25519_verifier_rejects_malformed_inputs() -> None:
    body = BUNDLE.encode("utf-8")
    sig = base64.b64decode(RESPONSE["signature"]["sig"])
    key = base64.b64decode(PINNED_KEY)
    assert _ed25519_verify(sig, body, key) is True
    assert _ed25519_verify(sig[:-1], body, key) is False  # wrong length
    assert _ed25519_verify(sig, body, key[:-1]) is False
    assert _ed25519_verify(bytes(64), body, key) is False
    assert _ed25519_verify(sig, body, bytes(32)) is False


def test_signature_without_a_usable_public_key_is_unverifiable_and_rejected() -> None:
    client = _make_cave().runtime_policy()  # nothing pinned
    result = _refresh_with(client, {"bundle": BUNDLE, "signature": RESPONSE["signature"], "public_key": None})
    assert result.ok is False
    assert result.error == "signature_invalid"
    assert client.state().has_bundle is False


def test_malformed_or_partial_signature_metadata_cannot_downgrade_to_unsigned() -> None:
    envelopes = (
        {"bundle": BUNDLE, "signature": {"sig": "***not-base64***"}, "public_key": {"key": "also-bad"}},
        {"bundle": BUNDLE, "signature": {}, "public_key": None},
        {"bundle": BUNDLE, "signature": "bad-shape", "public_key": None},
        {"bundle": BUNDLE, "signature": None, "public_key": RESPONSE["public_key"]},
    )
    for envelope in envelopes:
        client = _make_cave().runtime_policy()
        result = _refresh_with(client, envelope)
        assert (result.ok, result.error) == (False, "signature_invalid")
        assert client.state().has_bundle is False


def test_tofu_pins_the_embedded_key_and_refuses_a_later_unsigned_bundle() -> None:
    client = _make_cave().runtime_policy()
    assert _refresh_with(client, _signed_payload()).signed is True
    # A later unsigned bundle must not downgrade a client that has seen a key.
    later_unsigned = json.loads(KILL_BUNDLE)
    later_unsigned["sequence"] = 99
    result = _refresh_with(client, {"bundle": json.dumps(later_unsigned), "signature": None, "public_key": None})
    assert result.ok is False
    assert result.error == "unsigned_rejected"
    assert client.state().kill is False  # last-known-good survived


def test_tofu_pins_as_soon_as_a_signature_verifies_even_if_the_bundle_is_rejected() -> None:
    """The pin is stored the moment the signature verifies — BEFORE the schema
    and sequence checks. A validly signed bundle this client refuses on its
    contents has still proven which key the server signs with, so it must not
    leave the client downgradeable to unsigned."""
    client = _make_cave().runtime_policy()  # nothing pinned
    future = json.loads(BUNDLE)
    future["schema_version"] = "caveman.runtime-policy.v2"
    rejected = _refresh_with(client, _sign_bundle(json.dumps(future)))
    assert (rejected.ok, rejected.error) == (False, "unknown_schema_version")
    assert client.state().has_bundle is False  # nothing was accepted…

    # …but the key is pinned, so an unsigned bundle can no longer take hold.
    unsigned = _refresh_with(client, _unsigned_payload())
    assert (unsigned.ok, unsigned.error) == (False, "unsigned_rejected")
    assert client.state().has_bundle is False
    # A bundle signed by that same key is still accepted.
    assert _refresh_with(client, _signed_payload()).ok is True
    assert client.state().signed is True


def test_the_fixture_signing_seed_matches_the_fixture_public_key() -> None:
    """Keeps the test-local signer honest: it must mint the very key the
    signature cases pin, or the TOFU test above would prove nothing."""
    assert _sign_bundle(BUNDLE)["public_key"]["key"] == PINNED_KEY
    assert _refresh_with(_make_cave().runtime_policy(public_key=PINNED_KEY), _sign_bundle(BUNDLE)).ok is True


# ─── assignment vectors (exact float equality against the Go implementation) ──


@pytest.mark.parametrize("vector", FIXTURES["assignment_vectors"], ids=lambda v: "|".join(v["keys"]))
def test_assignment_vector_fraction_is_bit_identical(vector: dict[str, Any]) -> None:
    assert policy_unit_fraction(*vector["keys"]) == vector["fraction"]


def test_empty_unit_key_vector_is_reachable_through_the_public_name() -> None:
    """The empty-unit-key vector is only reachable through the exported hash:
    decide() refuses an empty unit key, so nothing else can pin this fraction."""
    empty = [v for v in FIXTURES["assignment_vectors"] if v["keys"][2] == ""]
    assert len(empty) == 1  # the fixture must keep carrying it
    assert policy_unit_fraction(*empty[0]["keys"]) == empty[0]["fraction"]


@pytest.mark.parametrize("vector", FIXTURES["assignment_vectors"], ids=lambda v: "|".join(v["keys"]))
def test_assignment_vector_arm_and_propensity(vector: dict[str, Any]) -> None:
    project_id, experiment_id, unit_key = vector["keys"]
    bundle = {
        "schema_version": "caveman.runtime-policy.v1",
        "project_id": project_id,
        "policy_version": 1,
        "sequence": 1,
        "issued_at": "2026-08-08T12:00:00Z",
        "refresh_seconds": 60,
        "kill": False,
        "runtime_policies": [
            {
                "id": "vector_policy",
                "task_family": "vector_family",
                "execute": {"workflow": "vector_execute"},
                "fallback": {"workflow": "vector_fallback"},
                "experiment": {
                    "id": experiment_id,
                    "holdout_frac": vector["holdout_frac"],
                    "arms": vector["arms"],
                },
            }
        ],
        "experiments": [],
    }
    client = _make_cave().runtime_policy()
    assert _refresh_with(client, {"bundle": json.dumps(bundle)}).ok is True
    decision = client.decide("vector_family", unit_key)

    if unit_key == "":
        # The empty-key vector pins the hash only: decide() refuses an empty
        # unit key before it ever assigns an arm.
        assert decision.reason == "no_unit_key"
        assert decision.arm is None
        return

    assert decision.arm == vector["expected_arm"]
    assert decision.propensity == vector["expected_propensity"]
    if vector["expected_arm"] == "holdout":
        assert decision.decision == "fallback"
        assert decision.workflow == "vector_fallback"
    else:
        assert decision.decision == "execute"
        assert decision.workflow == "vector_execute"


def test_weighted_arm_propensity_uses_the_fixtures_float_association() -> None:
    """Propensity is ``(1 - holdout) * (weight / total)`` in EXACTLY that
    association. ``((1 - holdout) * weight) / total`` is a different float
    expression; the fixture's weighted ``exp-w`` vectors exist to catch it."""
    weighted = [v for v in FIXTURES["assignment_vectors"] if v["keys"][1] == "exp-w"]
    assert len(weighted) >= 3  # the fixture must keep carrying the fractional arms
    diverged = 0
    for vector in weighted:
        if vector["expected_arm"] == "holdout":
            assert vector["expected_propensity"] == vector["holdout_frac"]
            continue
        total = sum(arm["fraction"] for arm in vector["arms"])
        weight = next(arm["fraction"] for arm in vector["arms"] if arm["name"] == vector["expected_arm"])
        holdout = vector["holdout_frac"]
        assert (1 - holdout) * (weight / total) == vector["expected_propensity"]
        if (1 - holdout) * weight / total != vector["expected_propensity"]:
            diverged += 1
    # At least one vector must actually separate the two associations, or the
    # assertion above would pass under the wrong arithmetic too.
    assert diverged >= 1


# ─── decision cases ─────────────────────────────────────────────────────────


def _client_for_case(case: dict[str, Any]) -> RuntimePolicyClient:
    named = case.get("bundle", "fetch")
    if named is None:
        return _make_cave().runtime_policy()  # never refreshed: no bundle at all
    if named == "kill_bundle":
        client = _make_cave().runtime_policy()
        assert _refresh_with(client, _unsigned_payload(KILL_BUNDLE)).ok is True
        return client
    return _loaded_client()


@pytest.mark.parametrize("case", FIXTURES["decision_cases"], ids=lambda c: c["name"])
def test_decision_case(case: dict[str, Any]) -> None:
    client = _client_for_case(case)
    decision = client.decide(case["task_family"], case["unit_key"], case["context"])
    expected = case["expect"]

    assert decision.decision == expected["decision"]
    assert decision.reason == expected["reason"]
    if "workflow" in expected:
        assert decision.workflow == expected["workflow"]
    if "policy_id" in expected:
        assert decision.policy_id == expected["policy_id"]
    if "experiment_id" in expected:
        assert decision.experiment_id == expected["experiment_id"]
    if "arm" in expected:
        assert decision.arm == expected["arm"]
    if "propensity" in expected:
        assert decision.propensity == expected["propensity"]


def test_applied_decision_passes_the_opaque_policy_payload_through() -> None:
    decision = _loaded_client().decide(
        "fix_failing_test_with_stacktrace",
        "task-a",
        {"stack_trace_location_confidence": 0.95, "language": "typescript"},
    )
    assert decision.budget == {"max_cost_usd": 1.2, "max_duration_seconds": 180}
    assert decision.verify == ["targeted_test_passes", "full_suite_passes"]
    assert decision.escalation == [{"on": "ambiguous_symbol", "action": "baseline"}]
    assert decision.policy_version == 7
    assert decision.sequence == 42
    assert decision.signed is True


# ─── kill paths ─────────────────────────────────────────────────────────────


def test_local_kill_latch_wins_over_a_live_bundle() -> None:
    client = _loaded_client()
    context = {"stack_trace_location_confidence": 0.95, "language": "typescript"}
    assert client.decide("fix_failing_test_with_stacktrace", "task-a", context).decision == "execute"
    client.kill()
    decision = client.decide("fix_failing_test_with_stacktrace", "task-a", context)
    assert (decision.decision, decision.reason, decision.workflow) == ("baseline", "local_kill", None)
    assert client.state().killed_locally is True


def test_kill_env_forces_baseline_and_off_values_do_not() -> None:
    client = _loaded_client()
    context = {"stack_trace_location_confidence": 0.95, "language": "typescript"}
    with patch.dict(os.environ, {"CAVEMAN_POLICY_KILL": "1"}):
        assert client.decide("fix_failing_test_with_stacktrace", "task-a", context).reason == "local_kill"
    with patch.dict(os.environ, {"CAVEMAN_POLICY_KILL": "false"}):
        assert client.decide("fix_failing_test_with_stacktrace", "task-a", context).decision == "execute"
    assert client.decide("fix_failing_test_with_stacktrace", "task-a", context).decision == "execute"


def test_kill_env_name_is_configurable() -> None:
    client = _make_cave().runtime_policy(public_key=PINNED_KEY, kill_env="MY_BRAKE")
    assert _refresh_with(client, _signed_payload()).ok is True
    with patch.dict(os.environ, {"MY_BRAKE": "on"}):
        assert client.decide("fix_failing_test_with_stacktrace", "task-a", {}).reason == "local_kill"


# ─── refresh robustness ─────────────────────────────────────────────────────


def test_sequence_regression_is_rejected() -> None:
    # Unpinned + unsigned so the sequence check is what rejects the bundle.
    client = _client_holding(BUNDLE)
    stale = json.loads(BUNDLE)
    stale["sequence"] = 41
    stale["policy_version"] = 6
    result = _refresh_with(client, {"bundle": json.dumps(stale)})
    assert result.ok is False
    assert result.error == "stale_sequence"
    assert client.state().sequence == 42
    assert client.state().policy_version == 7


def test_missing_or_invalid_counters_cannot_replace_last_known_good() -> None:
    invalid_counters = (
        ("sequence", None),
        ("sequence", 42.5),
        ("sequence", float("nan")),
        ("sequence", 9_007_199_254_740_992),
        ("policy_version", None),
        ("policy_version", -1),
    )
    for field, value in invalid_counters:
        client = _client_holding(BUNDLE)
        invalid = json.loads(BUNDLE)
        invalid["kill"] = True
        if value is None:
            invalid.pop(field)
        else:
            invalid[field] = value
        result = _refresh_with(client, {"bundle": json.dumps(invalid)})
        assert (result.ok, result.error) == (False, "invalid_bundle_counter")
        assert client.state().sequence == 42
        assert client.state().kill is False


def test_unknown_schema_version_is_rejected() -> None:
    client = _client_holding(BUNDLE)
    future = json.loads(BUNDLE)
    future["schema_version"] = "caveman.runtime-policy.v2"
    future["sequence"] = 99
    result = _refresh_with(client, {"bundle": json.dumps(future)})
    assert result.ok is False
    assert result.error == "unknown_schema_version"
    assert client.state().sequence == 42


def test_refresh_failure_keeps_last_known_good() -> None:
    client = _loaded_client()

    def boom(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        raise OSError("gateway unreachable")

    with patch("urllib.request.urlopen", side_effect=boom):
        result = client.refresh()
    assert result.ok is False
    assert result.error == "transport"
    assert client.state().policy_version == 7
    decision = client.decide(
        "fix_failing_test_with_stacktrace",
        "task-a",
        {"stack_trace_location_confidence": 0.95, "language": "typescript"},
    )
    assert decision.decision == "execute"


def test_malformed_response_and_bundle_are_rejected() -> None:
    client = _make_cave().runtime_policy()
    assert _refresh_with(client, {"bundle": None}).error == "malformed_response"
    assert _refresh_with(client, ["not", "an", "object"]).error == "malformed_response"
    assert _refresh_with(client, {"bundle": "{not json"}).error == "invalid_bundle"
    assert _refresh_with(client, {"bundle": "[]"}).error == "invalid_bundle"
    assert client.state().has_bundle is False


def test_refresh_response_is_capped_and_keeps_last_known_good() -> None:
    client = _loaded_client()
    body_reader = MagicMock(return_value=b"x" * (_POLICY_MAX_RESPONSE_BYTES + 1))
    response = MagicMock()
    response.__enter__ = MagicMock(return_value=MagicMock(read=body_reader))
    response.__exit__ = MagicMock(return_value=False)
    with patch("urllib.request.urlopen", return_value=response):
        result = client.refresh()
    assert result.error == "oversized_response"
    assert client.state().policy_version == 7
    assert client.state().sequence == 42
    body_reader.assert_called_once_with(_POLICY_MAX_RESPONSE_BYTES + 1)


@pytest.mark.parametrize("bad_key", ["not-base64!!", "", "c2hvcnQ=", base64.b64encode(bytes(33)).decode()])
def test_an_unusable_pinned_key_raises_at_construction(bad_key: str) -> None:
    """A pin that cannot be a 32-byte Ed25519 key is a caller bug, surfaced
    immediately — not a client that silently rejects every bundle forever.
    Mirrors the TypeScript constructor, which throws on the same input."""
    with pytest.raises(ValueError, match="base64-encoded 32-byte Ed25519 key"):
        _make_cave().runtime_policy(public_key=bad_key)


def test_auto_refresh_thread_polls_and_stops() -> None:
    calls = threading.Event()
    seen = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        seen.append(req.full_url)
        calls.set()
        return _fake_response(_signed_payload())

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        client = _make_cave().runtime_policy(public_key=PINNED_KEY, auto_refresh_seconds=0.01)
        try:
            assert calls.wait(timeout=5.0) is True
        finally:
            client.close()
    assert seen[0].endswith("/sdk/v1/runtime-policy")
    assert client.state().policy_version == 7


# ─── decide() is total ──────────────────────────────────────────────────────


@pytest.mark.parametrize(
    "context",
    [
        None,
        "not-a-dict",
        42,
        [],
        {"stack_trace_location_confidence": "0.95", "language": "typescript"},
        {"stack_trace_location_confidence": True, "language": "typescript"},
        {"stack_trace_location_confidence": float("nan"), "language": "typescript"},
        {"stack_trace_location_confidence": None, "language": None},
        {"stack_trace_location_confidence": {"nested": 1}, "language": ["typescript"]},
    ],
)
def test_decide_never_raises_on_garbage_context(context: Any) -> None:
    decision = _loaded_client().decide("fix_failing_test_with_stacktrace", "task-a", context)
    assert isinstance(decision, PolicyDecision)
    # Nothing unclear ever reaches "execute": guards fail closed.
    assert decision.decision in ("fallback", "baseline")
    assert decision.reason == "guards_failed"


def test_decide_never_raises_on_garbage_task_family_or_unit_key() -> None:
    client = _loaded_client()
    for family in (None, 42, [], {"a": 1}):
        assert client.decide(family, "task-a", {}).reason == "no_policy"
    for key in (None, "", 7, [], {}):
        decision = client.decide(
            "fix_failing_test_with_stacktrace",
            key,
            {"stack_trace_location_confidence": 0.95, "language": "typescript"},
        )
        assert decision.reason == "no_unit_key"


def test_boolean_context_values_never_compare_as_numbers() -> None:
    bundle = _bundle_with_policies(
        [
            {
                "id": "bool_guard",
                "task_family": "bools",
                "applies_when": [{"field": "flag", "op": "eq", "value": 1}],
                "execute": {"workflow": "run"},
            }
        ]
    )
    client = _client_holding(bundle)
    assert client.decide("bools", "u", {"flag": True}).reason == "guards_failed"
    assert client.decide("bools", "u", {"flag": 1}).reason == "applied"
    assert client.decide("bools", "u", {"flag": 1.0}).reason == "applied"


# ─── fail-closed policy shapes ──────────────────────────────────────────────


def _bundle_with_policies(policies: list[Any], **overrides: Any) -> str:
    bundle: dict[str, Any] = {
        "schema_version": "caveman.runtime-policy.v1",
        "project_id": "proj_parity",
        "policy_version": 1,
        "sequence": 1,
        "issued_at": "2026-08-08T12:00:00Z",
        "refresh_seconds": 60,
        "kill": False,
        "runtime_policies": policies,
        "experiments": [],
    }
    bundle.update(overrides)
    return json.dumps(bundle)


def _client_holding(bundle: str) -> RuntimePolicyClient:
    client = _make_cave().runtime_policy()
    assert _refresh_with(client, {"bundle": bundle}).ok is True
    return client


def test_structurally_invalid_documents_are_skipped() -> None:
    cases: list[Any] = [
        {"task_family": "f", "execute": {"workflow": "w"}},  # no id
        {"id": "p", "task_family": "f"},  # no execute
        {"id": "p", "task_family": "f", "execute": {"workflow": ""}},
        {"id": "p", "task_family": "f", "execute": {"workflow": "w"}, "applies_when": "nope"},
        {"id": "p", "task_family": "f", "execute": {"workflow": "w"}, "applies_when": ["nope"]},
    ]
    for broken in cases:
        decision = _client_holding(_bundle_with_policies([broken])).decide("f", "u", {})
        assert (decision.decision, decision.reason) == ("baseline", "invalid_policy"), broken


def test_disabled_only_match_reports_disabled() -> None:
    policy = {"id": "p", "task_family": "f", "disabled": True, "execute": {"workflow": "w"}}
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {})
    assert (decision.decision, decision.reason) == ("baseline", "disabled")


def test_two_matching_policies_are_ambiguous_not_arbitrary() -> None:
    policies = [
        {"id": "a", "task_family": "f", "execute": {"workflow": "wa"}},
        {"id": "b", "task_family": "f", "execute": {"workflow": "wb"}},
    ]
    decision = _client_holding(_bundle_with_policies(policies)).decide("f", "u", {})
    assert (decision.decision, decision.reason, decision.workflow) == ("baseline", "ambiguous_policy", None)
    assert decision.policy_id is None


def test_policy_without_an_experiment_applies_to_everything_past_the_guards() -> None:
    policy = {"id": "p", "task_family": "f", "execute": {"workflow": "w"}}
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", None, {})
    assert (decision.decision, decision.reason, decision.workflow) == ("execute", "applied", "w")
    assert decision.arm is None and decision.propensity is None


@pytest.mark.parametrize(
    "experiment",
    [
        "not-an-object",
        {"id": "", "arms": [{"name": "a", "fraction": 1}]},
        {"id": "e", "arms": []},
        {"id": "e", "arms": "nope"},
        {"id": "e", "arms": [{"name": "holdout", "fraction": 1}]},
        {"id": "e", "arms": [{"name": "", "fraction": 1}]},
        {"id": "e", "arms": [{"name": "a", "fraction": 0}]},
        {"id": "e", "arms": [{"name": "a", "fraction": -1}]},
        {"id": "e", "arms": [{"name": "a", "fraction": True}]},
        {"id": "e", "arms": [{"name": "a", "fraction": "1"}]},
        {"id": "e", "arms": [{"name": "a", "fraction": 1}], "holdout_frac": 1},
        {"id": "e", "arms": [{"name": "a", "fraction": 1}], "holdout_frac": -0.1},
        {"id": "e", "arms": [{"name": "a", "fraction": 1}], "holdout_frac": "0.1"},
    ],
)
def test_invalid_experiment_configs_fall_back_and_never_guess_an_arm(experiment: Any) -> None:
    policy = {
        "id": "p",
        "task_family": "f",
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
        "experiment": experiment,
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {})
    assert (decision.decision, decision.reason, decision.workflow) == ("fallback", "invalid_experiment", "fb")
    assert decision.arm is None


def test_nan_holdout_fraction_falls_back() -> None:
    # NaN cannot survive JSON.parse in the mirror, so it is injected post-parse.
    client = _client_holding(
        _bundle_with_policies(
            [
                {
                    "id": "p",
                    "task_family": "f",
                    "execute": {"workflow": "w"},
                    "fallback": {"workflow": "fb"},
                    "experiment": {"id": "e", "holdout_frac": 0.1, "arms": [{"name": "a", "fraction": 1}]},
                }
            ]
        )
    )
    client._bundle["runtime_policies"][0]["experiment"]["holdout_frac"] = float("nan")
    assert client.decide("f", "u", {}).reason == "invalid_experiment"


def test_guards_failed_still_carries_the_declining_policys_terms() -> None:
    """The caller is about to run this policy's fallback, so it still needs the
    policy's budget/verify/escalation. Mirrors the TypeScript forDoc, which
    attaches them on the guards_failed path too."""
    policy = {
        "id": "p",
        "task_family": "f",
        "applies_when": [{"field": "x", "op": "eq", "value": 1}],
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
        "budget": {"max_cost_usd": 0.4, "max_duration_seconds": 60},
        "verify": ["targeted_test_passes"],
        "escalation": [{"on": "ambiguous_symbol", "action": "baseline"}],
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {"x": 2})
    assert (decision.decision, decision.reason, decision.policy_id) == ("fallback", "guards_failed", "p")
    assert decision.budget == {"max_cost_usd": 0.4, "max_duration_seconds": 60}
    assert decision.verify == ["targeted_test_passes"]
    assert decision.escalation == [{"on": "ambiguous_symbol", "action": "baseline"}]


def test_opaque_payload_lists_are_filtered_to_well_typed_members() -> None:
    """Mirrors TS forDoc: verify keeps only strings, escalation only objects.
    Wrong-typed members are dropped, never repaired."""
    policy = {
        "id": "p",
        "task_family": "f",
        "execute": {"workflow": "w"},
        "verify": ["ok", 7, None, "also-ok", ["nested"]],
        "escalation": [{"on": "a", "action": "b"}, "not-a-record", 3, None],
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {})
    assert decision.verify == ["ok", "also-ok"]
    assert decision.escalation == [{"on": "a", "action": "b"}]


def test_an_empty_guard_field_name_fails_closed() -> None:
    """Mirrors TS guardPasses: field === "" is refused even when the context
    literally contains an empty-string key."""
    policy = {
        "id": "p",
        "task_family": "f",
        "applies_when": [{"field": "", "op": "eq", "value": 1}],
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {"": 1})
    assert (decision.decision, decision.reason) == ("fallback", "guards_failed")


def test_bundle_counters_accept_integer_valued_json_numbers_only() -> None:
    """JSON has one number type: a publisher may emit 7 or 7.0 for the same
    counter and the TypeScript mirror accepts both. Booleans and values beyond
    JavaScript's safe integer range reject the bundle."""
    bundle = _bundle_with_policies([], policy_version=7.0, sequence=42.0)
    state = _client_holding(bundle).state()
    assert state.policy_version == 7.0
    assert state.sequence == 42.0

    client = _make_cave().runtime_policy()
    rejected = _refresh_with(client, {"bundle": _bundle_with_policies(
        [], policy_version=True, sequence=10**400,
    )})
    assert (rejected.ok, rejected.error) == (False, "invalid_bundle_counter")
    assert client.state().has_bundle is False


def test_a_missing_unit_key_outranks_a_broken_experiment() -> None:
    """A caller with no stable unit could not be assigned even by a perfect
    experiment, so no_unit_key is reported first — matching the TypeScript
    order, which checks the unit key before normalizing the experiment."""
    policy = {
        "id": "p",
        "task_family": "f",
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
        "experiment": "not-an-object",
    }
    client = _client_holding(_bundle_with_policies([policy]))
    for missing in (None, "", 7, [], {}):
        decision = client.decide("f", missing, {})
        assert (decision.decision, decision.reason, decision.workflow) == ("fallback", "no_unit_key", "fb")
    # With a unit key present, the broken experiment is what fails.
    assert client.decide("f", "u", {}).reason == "invalid_experiment"


def test_fallback_workflow_may_be_the_customer_baseline() -> None:
    policy = {
        "id": "p",
        "task_family": "f",
        "applies_when": [{"field": "x", "op": "eq", "value": 1}],
        "execute": {"workflow": "w"},
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {"x": 2})
    assert (decision.decision, decision.reason, decision.workflow) == ("fallback", "guards_failed", None)


# ─── guard cases (the shared fail-closed truth table) ───────────────────────


@pytest.mark.parametrize("case", FIXTURES["guard_cases"], ids=lambda c: c["name"])
def test_guard_case(case: dict[str, Any]) -> None:
    """Every guard case in the shared fixture, evaluated twice: directly against
    the evaluator, and end-to-end through decide(). A condition is TRUE only
    when both sides are the same scalar kind AND the comparison holds — never
    fail-open, in either direction of any operator."""
    assert _policy_guard_passes(case["guard"], case["context"]) is case["expect"], case["name"]

    policy = {
        "id": "p",
        "task_family": "f",
        "applies_when": [case["guard"]],
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", case["context"])
    expected = ("execute", "applied", "w") if case["expect"] else ("fallback", "guards_failed", "fb")
    assert (decision.decision, decision.reason, decision.workflow) == expected, case["name"]


def test_guard_cases_cover_every_supported_operator() -> None:
    names = [case["name"] for case in FIXTURES["guard_cases"]]
    assert len(names) == len(set(names))  # ids must stay unique for the parametrization
    ops = {case["guard"]["op"] for case in FIXTURES["guard_cases"]}
    assert {"eq", "ne", "gt", "gte", "lt", "lte", "in"} <= ops
    # The fail-open direction the fixture exists to pin.
    assert any(case["guard"]["op"] == "ne" and case["expect"] is False for case in FIXTURES["guard_cases"])


def test_a_context_integer_too_large_for_a_float_is_not_comparable() -> None:
    """JSON has no integer bound; JavaScript parses this literal to Infinity and
    the guard is false. Python must reach the same answer — a fallback with
    guards_failed, never an OverflowError that collapses to policy_unavailable."""
    huge = 10**400
    for op, value in (("gt", 1), ("lt", 1), ("eq", huge), ("ne", 1)):
        policy = {
            "id": "p",
            "task_family": "f",
            "applies_when": [{"field": "x", "op": op, "value": value}],
            "execute": {"workflow": "w"},
            "fallback": {"workflow": "fb"},
        }
        decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {"x": huge})
        assert (decision.decision, decision.reason) == ("fallback", "guards_failed"), op


@pytest.mark.parametrize(
    "op,value,actual,expected",
    [
        ("eq", "a", "a", True),
        ("eq", "a", "b", False),
        ("ne", "a", "b", True),
        ("gt", 1, 2, True),
        ("gt", 1, 1, False),
        ("gte", 1, 1, True),
        ("lt", 2, 1, True),
        ("lte", 2, 2, True),
        ("in", ["a", "b"], "a", True),
        ("in", ["a", "b"], "c", False),
        ("in", "not-a-list", "a", False),
        ("matches", "a", "a", False),  # unknown op ⇒ condition false
        ("gt", "1", 2, False),  # type mismatch ⇒ condition false
        ("gt", 1, "2", False),
    ],
)
def test_guard_operator_table(op: str, value: Any, actual: Any, expected: bool) -> None:
    policy = {
        "id": "p",
        "task_family": "f",
        "applies_when": [{"field": "x", "op": op, "value": value}],
        "execute": {"workflow": "w"},
        "fallback": {"workflow": "fb"},
    }
    decision = _client_holding(_bundle_with_policies([policy])).decide("f", "u", {"x": actual})
    assert (decision.reason == "applied") is expected


# ─── decision span ──────────────────────────────────────────────────────────


def _span_attributes(payload: dict[str, Any], index: int = 0) -> dict[str, Any]:
    span = payload["resourceSpans"][0]["scopeSpans"][0]["spans"][index]
    out: dict[str, Any] = {}
    for kv in span["attributes"]:
        value = kv["value"]
        out[kv["key"]] = next(iter(value.values()))
    return out


def test_decision_span_rides_the_existing_exporter() -> None:
    cave = _make_cave()
    client = _make_cave().runtime_policy(public_key=PINNED_KEY)
    assert _refresh_with(client, _signed_payload()).ok is True
    exporter = cave.exporter()
    client.decide(
        "fix_failing_test_with_stacktrace",
        "task-a",
        {"stack_trace_location_confidence": 0.95, "language": "typescript"},
        exporter,
    )
    assert exporter.pending == 1
    payload = exporter.build_payload()
    span = payload["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
    assert span["name"] == "caveman.policy.decision"
    assert span["kind"] == 1
    attrs = _span_attributes(payload)
    assert attrs["cave.policy.id"] == "targeted_test_repair_v3"
    assert attrs["cave.policy.version"] == "7"  # OTLP int64 rides as a string
    assert attrs["cave.policy.decision"] == "execute"
    assert attrs["cave.policy.reason"] == "applied"
    assert attrs["cave.policy.signed"] is True
    assert attrs["cave.experiment.id"] == "exp-1"
    assert attrs["cave.experiment.arm"] == "candidate"
    assert attrs["cave.experiment.propensity"] == 0.9
    # Decisions are observability only: no money vocabulary anywhere.
    assert not any("saving" in key or "usd" in key or "verified" in key for key in attrs)


def test_decision_span_through_a_trace_parents_onto_the_trace() -> None:
    cave = _make_cave()
    client = cave.runtime_policy(public_key=PINNED_KEY)
    assert _refresh_with(client, _signed_payload()).ok is True
    with cave.trace() as trace:
        client.decide("summarize_ticket", "task-a", {}, trace)
        client.decide("summarize_ticket", "task-b", {}, trace)
        exporter = trace.exporter()
        assert trace.exporter() is exporter
        payload = exporter.build_payload()
        spans = payload["resourceSpans"][0]["scopeSpans"][0]["spans"]
        # Both decisions batch into ONE trace-bound exporter, no new network path.
        assert len(spans) == 2
        assert spans[0]["traceId"] == trace.trace_id
        assert spans[0]["parentSpanId"] == trace.span_id
        captured_request: list[Any] = []

        def fake_export(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
            captured_request.append(req)
            return _fake_response({"ok": True, "spans_accepted": 2, "spans_total": 2})

        with patch("urllib.request.urlopen", side_effect=fake_export):
            assert exporter.export() == {"ok": True, "spans_accepted": 2, "spans_total": 2}
        assert exporter.pending == 0
        assert captured_request[0].full_url == f"{CONFIG['base_url']}/v1/traces"
        exported = json.loads(captured_request[0].data)
        assert len(exported["resourceSpans"][0]["scopeSpans"][0]["spans"]) == 2
    attrs = _span_attributes(payload)
    assert attrs["cave.policy.decision"] == "baseline"
    assert attrs["cave.policy.reason"] == "no_policy"
    assert "cave.policy.id" not in attrs


def test_no_trace_means_no_emission_and_a_bad_sink_is_ignored() -> None:
    client = _loaded_client()
    assert client.decide("summarize_ticket", "task-a", {}).reason == "no_policy"
    assert client.decide("summarize_ticket", "task-a", {}, "not-a-trace").reason == "no_policy"


def test_decide_performs_no_network_io() -> None:
    client = _loaded_client()

    def explode(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        raise AssertionError("decide() must never touch the network")

    with patch("urllib.request.urlopen", side_effect=explode):
        for family in ("fix_failing_test_with_stacktrace", "summarize_ticket"):
            client.decide(family, "task-a", {"stack_trace_location_confidence": 0.95, "language": "typescript"})


def test_no_money_vocabulary_in_the_decision_surface() -> None:
    decision = _loaded_client().decide(
        "fix_failing_test_with_stacktrace",
        "task-a",
        {"stack_trace_location_confidence": 0.95, "language": "typescript"},
    )
    fields = set(vars(decision))
    assert not any(word in name for name in fields for word in ("saving", "verified", "dollar", "cost"))
