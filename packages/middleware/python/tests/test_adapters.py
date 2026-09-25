"""Every eligible adapter must import and expose its documented entry points."""

import pytest

from frameworks import require_adapter

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
    module = require_adapter(family)
    missing = [name for name in names if not hasattr(module, name)]
    assert not missing, f"caveman_middleware.{family} lost {missing}"
