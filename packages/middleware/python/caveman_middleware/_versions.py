"""Native version gates over release ranges, without importing the frameworks."""
from dataclasses import dataclass, replace
from functools import lru_cache
from importlib.metadata import PackageNotFoundError, version
from types import MappingProxyType
import re

from caveman_cloud.middleware import MiddlewareError, ensure_sync, warn_once


_STABLE_VERSION = re.compile(
    r"(?:(?P<epoch>[0-9]+)!)?(?P<release>[0-9]+(?:\.[0-9]+)*)"
    r"(?:(?:[-_.]?(?:post|rev|r)[-_.]?(?P<post>[0-9]*))|-(?P<implicit_post>[0-9]+))?"
    r"(?:\+[a-z0-9]+(?:[-_.][a-z0-9]+)*)?",
    re.IGNORECASE | re.ASCII,
)


@lru_cache(maxsize=32)
def installed_version(name):
    try:
        return version(name)
    except (PackageNotFoundError, ValueError, OSError):
        return None


def _stable_version(value):
    """Validate stable PEP 440 release/post/local forms without a dependency.

    Prereleases, development releases and nonzero epochs are not covered by our
    support ranges. Local labels do not alter a stable public version's range
    eligibility. An explicit zero epoch is equivalent to an omitted epoch.
    """
    match = _STABLE_VERSION.fullmatch(value) if isinstance(value, str) else None
    if match is None:
        return None
    try:
        if int(match["epoch"] or "0") != 0:
            return None
        release = tuple(int(part) for part in match["release"].split("."))
        post = match["post"] if match["post"] is not None else match["implicit_post"]
        return release, int(post or "0") if post is not None else -1
    except ValueError:
        return None


def in_range(installed, low, high):
    """Whether a stable public version satisfies `low <= installed < high`.

    Range eligibility does not prove compatibility. Upstream API changes still
    require native adapter tests; the serialization revision identifies our
    adapter contract and cannot detect arbitrary framework changes.
    """
    versions = [_stable_version(value) for value in (installed, low, high)]
    if any(value is None for value in versions):
        return False
    width = max(len(value[0]) for value in versions)
    found, lower, upper = [(release + (0,) * (width - len(release)), post) for release, post in versions]
    return lower <= found < upper


def matches_framework(*pins):
    """Pure version check for adapters retaining a passive per-call delegate.

    Each pin is `(distribution, minimum, exclusive_maximum)`.
    """
    return all(in_range(installed_version(name), low, high) for name, low, high in pins)


def framework_state(*pins):
    """``supported``; ``unsupported`` when a readable version is outside its pin; ``unverified`` when unreadable."""
    versions = [installed_version(name) for name, _, _ in pins]
    if any(found is not None and not in_range(found, low, high) for found, (_, low, high) in zip(versions, pins)):
        return "unsupported"
    return "unverified" if None in versions else "supported"


def gate(runtime, adapter, *pins, accept=False):
    """Decision 3: whether the adapter runs. Never raises.

    A readable version outside the tested range skips the adapter and warns once
    (``unsupported_version``) unless ``accept_framework_version=True``. An
    unreadable version (vendored or bundled builds) runs after the adapter's own
    imports proved the hooks exist, with a one-time ``version_unverified``.
    Strict mode surfaces the skip from ``preflight()``/``ready()``, not here.
    """
    state = "supported" if accept else framework_state(*pins)
    if state == "supported" or runtime.mode == "off":
        return state != "unsupported"  # an off runtime is inert: nothing to warn about
    if state == "unverified":
        warn_once(adapter, "version_unverified")
        return True
    runtime.decline("unsupported_version", adapter)  # warns once and keeps the on_diagnostic signal for existing hosts
    return False


@dataclass(frozen=True)
class Compatibility:
    """Decision 11 tier plus the tested ranges ``(distribution, oldest tested, exclusive maximum)``."""
    tier: str
    pins: tuple


COMPATIBILITY = MappingProxyType({
    "langchain": Compatibility("certified", (("langchain", "1.1", "2"), ("langchain-core", "1.1", "2"), ("langgraph", "1.0.2", "2"))),
    "openai": Compatibility("certified", (("openai", "2.20", "4"),)),
    "anthropic": Compatibility("certified", (("anthropic", "1.0", "2"),)),
    "litellm": Compatibility("certified", (("litellm", "1.95", "2"),)),
    "google": Compatibility("experimental", (("google-genai", "2.18", "3"),)),
    "strands": Compatibility("experimental", (("strands-agents", "1.43", "2"),)),
    "agno": Compatibility("experimental", (("agno", "3.0", "4"),)),
    "crewai": Compatibility("experimental", (("crewai", "1.15.3", "2"),)),
    "pydantic_ai": Compatibility("experimental", (("pydantic-ai-slim", "2.36", "3"),)),
    "autogen": Compatibility("experimental", (("autogen-core", "0.7", "0.8"), ("autogen-agentchat", "0.7", "0.8"), ("autogen-ext", "0.7", "0.8"))),
    "llama_index": Compatibility("experimental", (("llama-index-core", "0.14.5", "0.15"),)),
    "mcp": Compatibility("experimental", (("mcp", "2.0", "3"),)),
    "asgi": Compatibility("experimental", ()),  # pure ASGI: no framework to gate
})


def family_gate(runtime, family, adapter=None, accept=False):
    return gate(runtime, adapter or family, *COMPATIBILITY[family].pins, accept=accept)


def preflight(runtime, family, *, accept_framework_version=False):
    """The runtime's preflight plus this adapter family's version gate (Decision 3); never raises.

    Blocking discovery: call it at startup, not from inside a running event loop.
    """
    report = ensure_sync(runtime).preflight()
    if (report.status != "disabled" and not accept_framework_version
            and framework_state(*COMPATIBILITY[family].pins) == "unsupported"):
        return replace(report, status="unavailable", reason="unsupported_version", action=(
            f"Install a {family} version inside caveman_middleware.COMPATIBILITY[{family!r}].pins, "
            "or pass accept_framework_version=True after testing it."))
    return report


def ready(runtime, family, *, accept_framework_version=False):
    """Strict startup gate: raises ``unsupported_version`` for an untested framework, else the runtime's ready()."""
    if not accept_framework_version and framework_state(*COMPATIBILITY[family].pins) == "unsupported":
        raise MiddlewareError("unsupported_version")
    return ensure_sync(runtime).ready()
