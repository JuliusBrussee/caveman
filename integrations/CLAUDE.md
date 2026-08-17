# integrations — the framework-recipe registry (`caveman sdk` data)

Declarative routing recipes for AI SDKs/frameworks: each recipe is the one-line
base-URL shape that points a framework at the byte-safe gateway, with `{{baseURL}}`
(gateway origin) and `{{app}}` (the `/w/<app>` attribution slug) left as render-time
templates. Recipes are DATA, not code — the collapse decision applied to app frameworks.

## Layout
- `recipes/*.json` — one recipe per framework (12 today: openai-ts/py, anthropic-ts/py,
  google-genai, vercel-ai-sdk, langchain, litellm, crewai, pydantic-ai, openai-agents, curl).
  Fields: `id`, `display_name`, `lang` (`ts`/`python`/`bash`), `wire_protocol`, `note`, `code`.
- `compile.mjs` — zero-dep build-time compiler: validates every recipe (fail-closed) and
  emits `recipes.json` (published registry) + two EMBEDDED copies —
  `../cli/src/recipes.generated.ts` (the CLI stays zero-runtime-dep) and
  the web app's embedded copy (cloud may consume public data; the reverse import is forbidden).
- `recipes.json` — generated; do not hand-edit.

## Conventions
- **fail-closed**: an unknown `lang` or `wire_protocol` fails the compile — never a guessed
  protocol (no-placeholder rule). Allowed protocols are the 4 the proxy speaks natively
  (anthropic-messages · openai-chat · openai-responses · gemini-generatecontent) plus
  `multi` for frameworks that fan out behind one config.
- Output is deterministic (recipes sorted by id); re-running on unchanged input is a no-op.
- Run from the CLI build/test: `node ../integrations/compile.mjs`. The generated files are
  committed and must stay in sync — change a recipe and regenerate all three artifacts.

See ../../CLAUDE.md (root) · ../agents/CLAUDE.md (the agent twin) · ../cli/CLAUDE.md
