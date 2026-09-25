"""Shared fail-open fixtures: a runtime whose proxy is guaranteed unreachable."""
import socket
import copy
import json
from pathlib import Path

import pytest

from frameworks import required_adapters


def pytest_configure(config):
    required_adapters()


def _closed_port():
    """A port nothing is listening on. Connecting to it refuses immediately."""
    probe = socket.socket()
    probe.bind(("127.0.0.1", 0))
    port = probe.getsockname()[1]
    probe.close()
    return port


@pytest.fixture
def unreachable_runtime():
    """The failure every adapter must survive: the local proxy is not running."""
    from caveman_cloud.middleware import MiddlewareRuntime
    reports = []
    runtime = MiddlewareRuntime(endpoint=f"http://127.0.0.1:{_closed_port()}", deadline_ms=200,
                                on_report=reports.append)
    runtime.reports = reports
    try:
        yield runtime
    finally:
        runtime.close()


def bypassed(runtime):
    """Reports where the adapter chose the caller's own input, not a plan.

    An unreachable runtime can only ever produce these: `skipped` when the
    adapter asked and got nothing back, `disabled` when it never asked.
    """
    return [event for event in runtime.reports if event.status in ("skipped", "disabled")]


@pytest.fixture
def protocol_runtime():
    """A real SDK runtime with a deterministic protocol peer, never a provider.

    This validates adapter projection and recovery plumbing, not Engine quality
    or provider savings. All returned plans still pass the SDK plan validator.
    """
    from caveman_cloud.middleware import MiddlewareRuntime, sha256

    fixture = json.loads((Path(__file__).parents[3] / "sdk/parity/middleware.fixtures.json").read_text())
    reports, receipts, requests, retrievals = [], [], [], []
    runtime = MiddlewareRuntime(on_report=reports.append, strict=True)
    runtime.reports, runtime.receipts, runtime.requests, runtime.retrievals = reports, receipts, requests, retrievals
    originals = {}

    def http(path, body, timeout):
        if path == "capabilities":
            caps = copy.deepcopy(fixture["capabilities"])
            recovery_free = {**caps["transforms"][0], "transform_id": "fixture.literal.v1", "recovery": "none"}
            caps["transforms"].append(recovery_free)
            return caps
        request = json.loads(body)
        if path == "retrieve":
            retrievals.append(request)
            scope, segment = originals[request["handle"]]
            assert request["scope"] == scope, "recovery crossed a session boundary"
            text = segment["content"]
            return {**fixture["page"], "handle": request["handle"], "source_id": segment["source_id"],
                    "text": text, "original_sha256": sha256(text), "total_bytes": len(text.encode())}
        assert path == "optimize", path
        requests.append(request)
        plan = copy.deepcopy(fixture["plan"])
        plan.update(request_id=request["request_id"], input_digest=sha256(body), replacements=[])
        bound = request["recovery_binding"]
        plan["recovery"]["binding_id"] = bound["id"] if bound else None
        for index, segment in enumerate(request["segments"]):
            handle = "cmw_" + sha256(segment["content"])[:48]
            originals[handle] = (request["scope"], segment)
            text = f"[caveman: shortened; exact original via caveman_retrieve handle={handle}]\nshort excerpt" if bound else "short excerpt"
            replacement = {**fixture["plan"]["replacements"][0], "segment_id": segment["id"], "source_id": segment["source_id"],
                           "original_sha256": segment["sha256"], "text": text, "sha256": sha256(text), "recovery_handle": handle,
                           "transform_id": "caveman.engine.log.v1" if bound else "fixture.literal.v1"}
            plan["replacements"].append(replacement)
        count = len(plan["replacements"])
        plan["measurement"].update(tokens_before=1000 * count, tokens_after=100 * count, unique_tokens_reduced=900 * count)
        return plan

    runtime._http = http
    runtime.observe_background = lambda receipt: receipts.append(receipt)
    try:
        yield runtime
    finally:
        runtime.close()
