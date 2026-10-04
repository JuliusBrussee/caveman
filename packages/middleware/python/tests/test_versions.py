"""Range gates replace exact pins: a patch release must not disable an adapter."""
import pytest

from caveman_middleware._versions import in_range, matches_framework


@pytest.mark.parametrize(
    "installed,low,high,expected",
    [
        ("1.4.0", "1.4", "2", True),
        ("1.9.3", "1.4", "2", True),
        ("1.4", "1.4", "2", True),
        ("1.3.9", "1.4", "2", False),
        ("2.0.0", "1.4", "2", False),
        ("0.7.5", "0.7", "0.8", True),
        ("0.8.0", "0.7", "0.8", False),
        ("1.4.0rc1", "1.4", "2", False),
        ("1!1.4.0", "1.4", "2", False),
        ("0!1.4.0", "1.4", "2", True),
        ("1.4.0+local", "1.4", "2", True),
        ("1.4+linux_x86-64.1", "1.4.0", "2", True),
        ("1.4.post0", "1.4.0", "2", True),
        ("1.4.post2", "1.4.post1", "1.4.post3", True),
        ("1.4.post1", "1.4.post2", "2", False),
        ("1.4.post1", "1.3", "1.4", False),
        ("1.4-2", "1.4.post1", "1.4.post3", True),
        ("1.4.r2", "1.4.post1", "1.4.post3", True),
        ("1.4.REV2+local", "1.4.post1", "1.4.post3", True),
        ("1.4.post", "1.4.post0", "2", True),
        ("1.4-post-", "1.4.post0", "2", True),
        ("1.4", "1.4.0", "2", True),
        ("2", "1.4", "2.0.0", False),
        ("1.4.0.0", "1.4", "1.4.0.1", True),
        ("1.04.00", "1.4", "2", True),
        (None, "1.4", "2", False),
        (140, "1.4", "2", False),
        ("", "1.4", "2", False),
        ("nightly", "1.4", "2", False),
    ],
)
def test_in_range(installed, low, high, expected):
    assert in_range(installed, low, high) is expected


@pytest.mark.parametrize("installed", [
    "1.5a1", "1.5b1", "1.5rc1", "1.5preview1", "1.5.dev1", "1.5.post1.dev1", "1.5rc1+local",
    "1!1.5", "2!1.5.post1", "1.5nightly", "1.5junk", "1..5", "1.5.", "1.5+", "1.5+local..build",
    "1.5+local!build", "1.5.post1junk", "1.5-post--", "1.5\n", "١.٥", "v1.5", " 1.5",
])
def test_rejects_uncovered_or_malformed_versions(installed):
    assert in_range(installed, "1.4", "2") is False


@pytest.mark.parametrize("low,high", [("nightly", "2"), ("1.4", "2rc1"), ("1!1.4", "2"), (None, "2")])
def test_invalid_support_bounds_fail_closed(low, high):
    assert in_range("1.5", low, high) is False


def test_matches_framework_refuses_an_absent_distribution():
    assert matches_framework(("caveman-no-such-framework", "1.0", "2")) is False


def test_matches_framework_requires_every_pin():
    assert matches_framework(("pytest", "0", "99999"), ("caveman-no-such-framework", "1.0", "2")) is False
    assert matches_framework(("pytest", "0", "99999")) is True


def test_gate_policy(monkeypatch, caplog):
    """Decision 3: out of range skips and warns once, unreadable runs with version_unverified, never raises."""
    from conftest import peer_runtime
    from caveman_middleware import _versions

    runtime, diagnostics = peer_runtime(strict=True), []
    runtime._diagnostic = diagnostics.append
    versions = {"old": "0.1.0", "good": "1.5.0", "vendored": None}
    monkeypatch.setattr(_versions, "installed_version", versions.get)
    assert _versions.gate(runtime, "demo", ("good", "1.4", "2")) is True
    assert _versions.gate(runtime, "demo", ("old", "1.4", "2")) is False
    assert _versions.gate(runtime, "demo", ("old", "1.4", "2"), accept=True) is True
    assert _versions.gate(runtime, "demo", ("vendored", "1.4", "2"), ("good", "1.4", "2")) is True
    assert _versions.gate(runtime.as_async(), "demo-async", ("old", "1.4", "2")) is False
    assert _versions.framework_state(("old", "1.4", "2"), ("vendored", "1.4", "2")) == "unsupported"
    assert caplog.text.count("adapter=demo reason=unsupported_version") == 1
    assert "adapter=demo-async reason=unsupported_version" in caplog.text
    # The reason code and adapter, not the sentence around them: the wording of this warn-once line is the SDK's.
    assert any("adapter=demo" in line and "reason=version_unverified" in line for line in caplog.text.splitlines())
    assert "adapter=-" not in caplog.text, "every decline names its adapter"
    assert diagnostics == [{"code": "unsupported_version", "cache_continuity": "unavailable"}] * 2
    runtime.close()


def test_preflight_and_ready_surface_an_untested_framework(monkeypatch):
    """Strict version failures surface from preflight()/ready(), not from wrapping."""
    import pytest
    from conftest import peer_runtime
    from caveman_cloud.middleware import MiddlewareError
    import caveman_middleware
    from caveman_middleware import _versions

    runtime = peer_runtime(strict=True)
    assert caveman_middleware.preflight(runtime, "langchain", accept_framework_version=True).status == "ready"
    monkeypatch.setattr(_versions, "installed_version", lambda name: "0.0.1")
    report = caveman_middleware.preflight(runtime.as_async(), "langchain")
    assert (report.status, report.reason) == ("unavailable", "unsupported_version")
    with pytest.raises(MiddlewareError, match="unsupported_version"):
        caveman_middleware.ready(runtime, "langchain")
    assert caveman_middleware.ready(runtime, "langchain", accept_framework_version=True)["schema_version"] == 1
    runtime.close()


# The first framework module each adapter imports; blocking it stands in for a release that lacks it.
FRAMEWORK_IMPORTS = {
    "langchain": "langchain.agents.middleware", "openai": "openai", "anthropic": "anthropic", "google": "google.genai",
    "litellm": "litellm", "strands": "strands", "agno": "agno.models.base", "crewai": "crewai", "pydantic_ai": "pydantic_ai",
    "autogen": "autogen_core", "llama_index": "llama_index.core.agent.workflow", "mcp": "mcp.types",
}


@pytest.mark.parametrize("family,blocked", sorted(FRAMEWORK_IMPORTS.items()))
def test_a_failed_framework_import_names_the_installed_version_and_range(family, blocked, monkeypatch):
    """An out-of-range install is unsupported_version with both versions, never a misleading "Install ..." hint."""
    import importlib
    import sys
    from caveman_middleware import _versions

    assert sorted(FRAMEWORK_IMPORTS) == sorted(set(_versions.COMPATIBILITY) - {"asgi"})
    module = f"caveman_middleware.{family}"
    monkeypatch.delitem(sys.modules, module, raising=False)
    monkeypatch.setitem(sys.modules, blocked, None)
    monkeypatch.setattr(_versions, "installed_version", lambda name: "0.0.3")
    with pytest.raises(ImportError) as raised:
        importlib.import_module(module)
    name, low, high = _versions.COMPATIBILITY[family].pins[0]
    assert raised.value.code == "unsupported_version" and "unsupported_version" in str(raised.value)
    assert f"{name} 0.0.3 is installed; this adapter requires {name}>={low},<{high}" in str(raised.value)
    monkeypatch.setattr(_versions, "installed_version", lambda name: None)  # not installed at all: the install hint
    with pytest.raises(ImportError, match=r"Install caveman-middleware\[") as raised:
        importlib.import_module(module)
    assert not hasattr(raised.value, "code")
