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
        ("1.4.0rc1", "1.4", "2", True),
        ("1!1.4.0", "1.4", "2", True),
        ("1.4.0+local", "1.4", "2", True),
        (None, "1.4", "2", False),
        ("", "1.4", "2", False),
        ("nightly", "1.4", "2", False),
    ],
)
def test_in_range(installed, low, high, expected):
    assert in_range(installed, low, high) is expected


def test_matches_framework_refuses_an_absent_distribution():
    assert matches_framework(("caveman-no-such-framework", "1.0", "2")) is False


def test_matches_framework_requires_every_pin():
    assert matches_framework(("pytest", "0", "99999"), ("caveman-no-such-framework", "1.0", "2")) is False
    assert matches_framework(("pytest", "0", "99999")) is True
