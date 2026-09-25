"""Native version gates over release ranges, without importing the frameworks."""
from functools import lru_cache
from importlib.metadata import PackageNotFoundError, version
import re


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


def supports_framework(runtime, *pins):
    if runtime.mode == "off":
        return False
    if matches_framework(*pins):
        return True
    runtime.decline("unsupported_version")
    return False
