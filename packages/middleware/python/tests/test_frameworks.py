"""The CI selector must never turn broken selected coverage green."""
import pytest

import frameworks


def test_missing_optional_adapter_skips(monkeypatch):
    monkeypatch.delenv("CAVEMAN_REQUIRED_ADAPTERS", raising=False)
    monkeypatch.setattr(frameworks, "version", lambda _: (_ for _ in ()).throw(frameworks.PackageNotFoundError()))
    with pytest.raises(pytest.skip.Exception, match="not installed"):
        frameworks.require_adapter("openai")


def test_missing_required_adapter_fails(monkeypatch):
    monkeypatch.setenv("CAVEMAN_REQUIRED_ADAPTERS", "openai")
    monkeypatch.setattr(frameworks, "version", lambda _: (_ for _ in ()).throw(frameworks.PackageNotFoundError()))
    with pytest.raises(pytest.fail.Exception, match="not installed"):
        frameworks.require_adapter("openai")


def test_unsupported_required_adapter_fails(monkeypatch):
    monkeypatch.setenv("CAVEMAN_REQUIRED_ADAPTERS", "openai")
    monkeypatch.setattr(frameworks, "version", lambda _: "2.20.0")
    with pytest.raises(pytest.fail.Exception, match="unsupported"):
        frameworks.require_adapter("openai")


def test_broken_installed_adapter_fails_even_when_optional(monkeypatch):
    monkeypatch.delenv("CAVEMAN_REQUIRED_ADAPTERS", raising=False)
    monkeypatch.setattr(frameworks, "version", lambda _: "3.10.0")
    monkeypatch.setattr(frameworks.importlib, "import_module", lambda _: (_ for _ in ()).throw(ImportError("removed native symbol")))
    with pytest.raises(ImportError, match="removed native symbol"):
        frameworks.require_adapter("openai")


def test_required_selector_accepts_extra_names_and_rejects_typos(monkeypatch):
    monkeypatch.setenv("CAVEMAN_REQUIRED_ADAPTERS", "pydantic-ai,llama_index")
    assert frameworks.required_adapters() == {"pydantic_ai", "llama_index"}
    monkeypatch.setenv("CAVEMAN_REQUIRED_ADAPTERS", "openia")
    with pytest.raises(pytest.UsageError, match="openia"):
        frameworks.required_adapters()
