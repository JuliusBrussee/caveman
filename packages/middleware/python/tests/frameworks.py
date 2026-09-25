"""Optional local coverage, mandatory coverage for each selected CI adapter.

CAVEMAN_REQUIRED_ADAPTERS accepts comma-separated module/extra names or ``all``.
An installed, eligible framework must import successfully: import errors are
never converted into skips. An unsupported transitive provider dependency does
not accidentally opt another adapter into an isolated framework's CI job.
"""
import importlib
import os
from importlib.metadata import PackageNotFoundError, version
from pathlib import Path
import tomllib

import pytest
from packaging.requirements import Requirement


EXTRAS = tomllib.loads((Path(__file__).parents[1] / "pyproject.toml").read_text())["project"]["optional-dependencies"]
REQUIREMENTS = {name.replace("-", "_"): [Requirement(value) for value in values] for name, values in EXTRAS.items()}


def required_adapters():
    selected = {name.strip().replace("-", "_") for name in os.environ.get("CAVEMAN_REQUIRED_ADAPTERS", "").split(",") if name.strip()}
    if selected == {"all"}:
        return set(REQUIREMENTS)
    unknown = selected - REQUIREMENTS.keys()
    if unknown:
        raise pytest.UsageError(f"Unknown CAVEMAN_REQUIRED_ADAPTERS: {', '.join(sorted(unknown))}")
    return selected


def require_adapter(family):
    """Import an eligible adapter; skip only absent/unsupported optional extras."""
    required = family in required_adapters()
    for requirement in REQUIREMENTS[family]:
        try:
            installed = version(requirement.name)
        except PackageNotFoundError:
            reason = f"{family} requires {requirement}; distribution is not installed"
        else:
            if installed in requirement.specifier:
                continue
            reason = f"{family} requires {requirement}; installed {installed} is unsupported"
        if required:
            pytest.fail(reason, pytrace=False)
        pytest.skip(reason)
    # Never catch ImportError here. Missing symbols and broken transitive
    # dependencies in an installed supported extra are real failures.
    return importlib.import_module(f"caveman_middleware.{family}")
