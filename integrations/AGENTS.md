# integrations: the framework recipe registry

Routing recipes for AI SDKs and frameworks. Each recipe is the short code snippet that points one
framework at the gateway, with `{{baseURL}}` (gateway origin) and `{{app}}` (the `/w/<app>`
attribution slug) left as templates filled at render time. Recipes are data, not code. The CLI
embeds them and prints them with `caveman snippets [<id>] [--app <slug>]`.

## Layout
- `recipes/*.json`: one recipe per framework (12 today: anthropic-py, anthropic-ts, crewai, curl, google-genai, langchain, litellm, openai-agents, openai-py, openai-ts, pydantic-ai, vercel-ai-sdk). Fields: `schema_version`, `id`, `display_name`, `lang` (`ts`, `python`, `bash`), `wire_protocol`, optional `note`, `code`, `models`.
- `compile.mjs`: zero-dependency compiler. Validates every recipe, then writes `recipes.json` (the published registry) and `packages/cli/src/recipes.generated.ts` (the CLI's embedded copy, so the CLI needs no runtime dependency).
- `recipes.json`: generated. Do not hand-edit.

## Build
- `node integrations/compile.mjs` from the repo root regenerates both outputs. `packages/cli/scripts/compile-registries.mjs` runs it (with `agents/` and `skills/`) on every CLI `build` and `test`.
- `CAVEMAN_RECIPES_DIR`, `CAVEMAN_CLI_DIR` and `CAVEMAN_CATALOG_FILE` override the inputs and output; `packages/cli/tests/registry-gates.runtime.mjs` uses them to test failure cases.
- Both generated files are committed. Change a recipe, regenerate, and commit all three.

## Invariants
- **Fail closed.** An unknown `lang` or `wire_protocol`, a duplicate `id`, or `code` that does not reference `{{baseURL}}` fails the compile. Allowed protocols are the four the proxy speaks natively (`anthropic-messages`, `openai-chat`, `openai-responses`, `gemini-generatecontent`) plus `multi` for frameworks that fan out behind one config.
- **Every pinned model is priced.** Each `models` entry must exist in `shared/provider-catalog/catalog/current.yaml` and appear in `code`, and every model literal in `code` must be declared in `models`. A recipe never ships an unpriced model.
- **Deterministic output.** Recipes are sorted by `id`; re-running on unchanged input changes nothing.

See the root [AGENTS.md](../AGENTS.md).
