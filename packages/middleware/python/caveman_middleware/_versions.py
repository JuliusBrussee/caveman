"""Native version gates over release ranges, without importing the frameworks."""
from functools import lru_cache
from importlib.metadata import PackageNotFoundError, version


@lru_cache(maxsize=32)
def installed_version(name):
    try:
        return version(name)
    except (PackageNotFoundError, ValueError, OSError):
        return None


def _release(value):
    """Leading numeric release segments of a PEP 440 version, as a tuple.

    `packaging` is not a dependency of this package and may not be installed, so
    epochs, prerelease and local segments are dropped rather than ordered: an
    `rc` compares as its own release, which is what a support gate wants.
    """
    parts = []
    for chunk in value.split("+")[0].split("!")[-1].split("."):
        digits = ""
        for character in chunk:
            if not character.isdigit():
                break
            digits += character
        if not digits:
            break
        parts.append(int(digits))
        if digits != chunk:
            break
    return tuple(parts)


def in_range(installed, low, high):
    """`low <= installed < high` over release segments only.

    A framework that breaks the wire shape inside the range is caught by the
    adapter's serialization revision, which is part of the runtime's scope
    identity. This gate only stops a different major from reaching the wire.
    """
    if not installed:
        return False
    found = _release(installed)
    return bool(found) and _release(low) <= found < _release(high)


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
