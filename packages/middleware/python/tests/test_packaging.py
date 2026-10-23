"""Installer exclusions must reach the wheel without capping future patches."""
from importlib.metadata import requires

import pytest
from packaging.requirements import Requirement


@pytest.mark.parametrize("version,accepted", [
    ("1.94.9", False),
    ("1.95.0", True),
    ("1.105.3", True),
    ("1.106.0", False),
    ("1.106.1", False),
    ("1.106.2", False),
    ("1.107.0", False),
    ("1.106.3", True),
    ("1.107.1", True),
    ("2.0.0", False),
])
def test_litellm_extra_excludes_only_known_bad_releases(version, accepted):
    requirements = [Requirement(value) for value in requires("caveman-middleware")]
    extra = [req for req in requirements if req.marker and req.marker.evaluate({"extra": "litellm"})]
    assert [req.name for req in extra] == ["litellm"]
    assert (version in extra[0].specifier) is accepted
