"""Security floors must survive wheel packaging without affecting other extras."""
from importlib.metadata import requires

from packaging.requirements import Requirement
import pytest


def _filelock_requirements(extra):
    return [
        requirement
        for raw in requires("caveman-middleware") or []
        if (requirement := Requirement(raw)).name == "filelock"
        and (requirement.marker is None or requirement.marker.evaluate({"extra": extra}))
    ]


@pytest.mark.parametrize("version", ["3.32.7", "4.2.0"])
def test_litellm_wheel_rejects_vulnerable_filelock(version):
    constraints = _filelock_requirements("litellm")
    assert constraints, "the installed wheel must carry the LiteLLM filelock security floor"
    assert not all(version in requirement.specifier for requirement in constraints)


def test_litellm_wheel_accepts_patched_filelock():
    constraints = _filelock_requirements("litellm")
    assert constraints
    assert all("4.2.1" in requirement.specifier for requirement in constraints)


@pytest.mark.parametrize("extra", ["", "openai", "anthropic", "langchain", "asgi"])
def test_filelock_floor_is_litellm_only(extra):
    assert not _filelock_requirements(extra)
