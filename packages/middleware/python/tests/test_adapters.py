"""Fail-open smoke: every adapter must import and expose its documented entry.

The adapter modules raise ImportError with install guidance when their framework
is missing, so an absent extra skips rather than fails. What is asserted here is
the part that holds for all of them: importing an adapter never touches the
network, and the names the README tells people to call actually exist.
"""
import importlib

import pytest

ADAPTERS = {
    "openai": ("with_caveman_openai", "with_caveman_openai_tools"),
    "anthropic": ("with_caveman_anthropic", "CavemanAnthropicMiddleware"),
    "google": ("with_caveman_google", "with_caveman_google_chat"),
    "langchain": ("CavemanMiddleware", "with_caveman_agent", "with_caveman_model", "CavemanDocumentCompressor"),
    "litellm": ("CavemanLiteLLM",),
    "strands": ("CavemanModel", "with_caveman_model", "with_caveman_agent"),
    "agno": ("CavemanModel", "with_caveman_model", "with_caveman_agent"),
    "asgi": ("CavemanASGIMiddleware", "ASGIContext"),
    "mcp": ("CavemanMCPHost", "bind_mcp_tool"),
    "crewai": ("CavemanLLM", "with_caveman_llm", "with_caveman_agent"),
    "pydantic_ai": ("CavemanModel", "with_caveman_model"),
    "autogen": ("CavemanChatCompletionClient", "CavemanWorkbench", "with_caveman_model"),
    "llama_index": ("CavemanLLMTools",),
}


@pytest.mark.parametrize("family,names", sorted(ADAPTERS.items()))
def test_adapter_exposes_its_documented_entry_points(family, names):
    try:
        module = importlib.import_module(f"caveman_middleware.{family}")
    except ImportError as error:
        pytest.skip(f"{family} framework not installed: {error}")
    missing = [name for name in names if not hasattr(module, name)]
    assert not missing, f"caveman_middleware.{family} lost {missing}"
