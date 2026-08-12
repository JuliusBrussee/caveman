---
name: caveman-setup
description: >
  Wire the current repository through the Caveman Cloud gateway so every LLM
  request is measured — cost, tokens, latency — with zero behavior change.
  Use when the user pastes the Caveman setup prompt, says "set up caveman",
  or wants LLM spend observability added to an app. Requires the gateway URL
  and a Cave API key (the setup prompt carries both).
---

You are wiring this repository through the Caveman gateway. Caveman is a
byte-preserving LLM proxy: in record mode it measures what your app sends and
what it costs, and changes nothing else. Your job is a minimal, verified
integration — not a refactor.

The prompt that sent you here provides four values. Refer to them as:

- `GATEWAY` — the gateway base URL (e.g. `https://gateway.caveman.so` or `http://127.0.0.1:8787`)
- `CAVE_API_KEY` — the gateway auth secret (treat like any API key: env var only, never committed, never printed in full)
- `PROVIDER_KEYS` — `stored` (provider keys live encrypted in Caveman Cloud) or `byok` (this app sends its own provider key per request)
- `DASHBOARD` — the dashboard base URL (e.g. `https://app.caveman.so`)

If any value is missing, stop and ask for it. Do not guess a URL or mint a key.

## Rules (non-negotiable)

1. **Coherent integration.** Wire every live LLM callsite through existing
   configuration and responsible seams. Touch each layer correctness requires.
   No drive-by refactors or formatting sweeps; add an abstraction only when it
   clarifies ownership or lowers lifecycle cost.
2. **Secrets stay in env vars.** `CAVE_API_KEY` goes into the env file the repo
   already uses (`.env`, `.env.local`, …). If that file isn't gitignored, add it
   to `.gitignore` and say so. Never hardcode the key in source.
3. **Report only what you observed.** The final report states the HTTP status
   and usage numbers from the real verification response — never assumed
   success. If verification fails, report the failure template instead.
4. **Record mode only.** You are adding measurement. You do not enable any
   optimization, and you do not claim any savings — verified savings are $0
   until an optimizer is explicitly turned on and passes its eval gate.
5. **Provider keys are not your business.** With `PROVIDER_KEYS: stored` you
   never see one. With `byok`, the app's existing provider key stays exactly
   where it already is.

## Step 1 — Find every live LLM callsite

Read dependency files (`package.json`, `requirements.txt`, `pyproject.toml`,
`go.mod`, lockfiles) and search the source for LLM clients:

- SDK imports: `openai`, `@anthropic-ai/sdk`, `anthropic`, `ai` +
  `@ai-sdk/*` (Vercel), `langchain*`, `litellm`, `google-genai` /
  `@google/genai`, `crewai`, `pydantic_ai`, `openai-agents` / `agents`
- Raw HTTP to `api.openai.com`, `api.anthropic.com`, `generativelanguage.googleapis.com`
- Existing base-URL env vars: `OPENAI_BASE_URL`, `OPENAI_API_BASE`,
  `ANTHROPIC_BASE_URL`, `GEMINI_BASE_URL`, `GOOGLE_GEMINI_BASE_URL`

List what you found (file:line per callsite) before changing anything. If you
find **no** LLM callsites, stop and report the "nothing to wire" template at
the end of this file — do not invent an integration.

## Step 2 — Pick the app slug

One slug names this app in the gateway path: `GATEWAY/w/<app>`. Derive it from
the package/module name (e.g. `support-bot`, `acme-api`). Grammar:
lowercase `[a-z0-9]` first, then `[a-z0-9._-]`, max 64 chars. Spend for this
whole app groups under that slug on the dashboard.

## Step 3 — Wire each callsite

The pattern is always the same: **base URL → the gateway with `/w/<app>`,
plus one auth header.** Gateway auth is `x-cave-api-key: CAVE_API_KEY`
(`Authorization: Bearer CAVE_API_KEY` also works where a header is awkward).
With `PROVIDER_KEYS: byok`, also send `x-cave-upstream-key: <the provider key
the app already uses>`.

Two facts that make the wiring safe (both are gateway-enforced, not hopes):
the gateway rebuilds upstream auth headers from scratch, so a client's
`Authorization`/`x-api-key` value is never forwarded to the provider; and with
`stored`, upstream auth comes from the encrypted connection server-side. So in
`stored` mode, where an SDK insists on an api-key parameter, set it to the
Cave key — it authenticates the gateway and goes no further.

Exact shapes (use the one matching each callsite — these are the product's
published recipes, not suggestions):

**OpenAI SDK (TS)** — Chat Completions and Responses both route through:
```ts
const client = new OpenAI({
  baseURL: `${process.env.CAVE_GATEWAY_URL}/w/<app>/openai/v1`,
  apiKey: process.env.OPENAI_API_KEY,           // byok: unchanged · stored: use CAVE_API_KEY
  defaultHeaders: {
    "x-cave-api-key": process.env.CAVE_API_KEY!,
    // byok only:
    "x-cave-upstream-key": process.env.OPENAI_API_KEY!,
  },
});
```

**OpenAI SDK (Python)** — same shape: `base_url=f"{gw}/w/<app>/openai/v1"`,
`default_headers={"x-cave-api-key": ..., "x-cave-upstream-key": ...}`.

**Anthropic SDK (TS/Python)** — the SDK appends `/v1/messages` itself. The
`x-cave-api-key` header is required here in both modes (this SDK's own key
param rides `x-api-key`, which is not a gateway-auth header):
```python
client = anthropic.Anthropic(
    base_url=f"{os.environ['CAVE_GATEWAY_URL']}/w/<app>",
    api_key=os.environ["ANTHROPIC_API_KEY"],      # byok: unchanged · stored: use CAVE_API_KEY
    default_headers={
        "x-cave-api-key": os.environ["CAVE_API_KEY"],
        # byok only:
        "x-cave-upstream-key": os.environ["ANTHROPIC_API_KEY"],
    },
)
```

**Vercel AI SDK** — `createOpenAICompatible({ baseURL: `${gw}/w/<app>/openai/v1`,
headers: { "x-cave-api-key": ... } })`; Anthropic models via
`createAnthropic({ baseURL: `${gw}/w/<app>/v1`, headers: { ... } })`.

**LangChain / LangGraph** — `ChatOpenAI(base_url=f"{gw}/w/<app>/openai/v1",
default_headers={...})`; `ChatAnthropic(base_url=f"{gw}/w/<app>",
default_headers={...})`. LangGraph inherits whatever model you pass it.

**LiteLLM** — per call `api_base=f"{gw}/w/<app>/openai/v1"` +
`extra_headers={...}`, or fleet-wide in the LiteLLM proxy `config.yaml`.

**Raw HTTP / anything else** — swap the host, keep the provider's native path:
`GATEWAY/w/<app>/v1/chat/completions` (OpenAI protocol) or
`GATEWAY/w/<app>/v1/messages` (Anthropic protocol), add the header(s).

Concretely, with slug `support-bot` and the hosted gateway, an OpenAI-SDK base
URL reads `https://gateway.caveman.so/w/support-bot/openai/v1`. And in `stored`
mode, drop every `x-cave-upstream-key` line entirely — it is byok-only.

For frameworks not listed (google-genai, crewai, pydantic-ai, openai-agents),
fetch the matching page under `<docs origin>/docs/integrations/` — same origin
this skill came from — and follow it.

Add to the repo's env file (and reference from code — no literals):

```
CAVE_GATEWAY_URL=<GATEWAY>
CAVE_API_KEY=<CAVE_API_KEY>
```

## Step 4 — Verify with one real request

The user pasted the setup prompt to authorize exactly this: one small
verification request. Send it now — do not pause to ask permission for it.
An integration that ends unverified because you hesitated is a worse outcome
than one tiny request; finishing the verification and the report autonomously
is the point of this skill.

Send one minimal request through the wiring you just built — the app's own
cheapest path if it has a script for it, otherwise curl **on the path matching
the protocol you just wired** with the app's own model and a small cap
(`max_tokens` ≤ 32):

```bash
# OpenAI-protocol wiring:
curl -sS "$CAVE_GATEWAY_URL/w/<app>/v1/chat/completions" \
  -H "x-cave-api-key: $CAVE_API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"<model the repo already uses>","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}'

# Anthropic-protocol wiring:
curl -sS "$CAVE_GATEWAY_URL/w/<app>/v1/messages" \
  -H "x-cave-api-key: $CAVE_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"<model the repo already uses>","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}'
```

(byok: add `-H "x-cave-upstream-key: $PROVIDER_KEY"`.) This is one real,
billable provider request — that is the point: real traffic, real measurement.

Read the response. Success = HTTP 200 with a `usage` block. Anything else =
the matching failure template below.

## Step 5 — Report

End with exactly this shape, values filled from what you actually did and saw:

```
## Caveman is live in this repo

Wired: <n> callsite(s) in <n> file(s)
  - <file> — <one-line what changed>
App slug: <app> — spend for this app groups under it
Verified: HTTP 200 · model <model> · <in> in / <out> out tokens (one real request)
Mode: record — measured only. No model-visible bytes changed, no optimization
enabled. Verified savings are $0 until you turn an optimizer on and it passes
its eval gate. That honesty is the product.

See the dollars: <DASHBOARD>/traces — your request is the top row, priced from
the public catalog. <DASHBOARD>/getting-started flips to "First request received."

Want spend split by workflow (e.g. support-reply vs nightly-digest), not just
by app? Say "discover workflows" — I'll fetch <docs origin>/docs/discover-workflows.md
and label every callsite by the job it does.
```

## Failure templates (use verbatim, filled in — never soften)

- **Nothing to wire**: "I found no LLM callsites in this repo (searched SDKs,
  raw provider HTTP, base-URL env vars). If this repo runs a coding agent
  rather than shipping LLM code, use `caveman wrap <agent>` instead — see
  <DASHBOARD>/getting-started."
- **Gateway unreachable**: "The verification request could not reach GATEWAY
  (<error>). Wiring is in place but unverified — nothing will be measured
  until the gateway is reachable. Check the URL and network, then re-run the
  verification curl above."
- **401 cave_invalid_api_key**: "The gateway rejected CAVE_API_KEY. Mint a new
  key at <DASHBOARD>/getting-started and update the env file; the wiring
  itself is unchanged."
- **404 cave_route_not_found**: "The gateway matched no route — usually a
  malformed /w/<app> slug (lowercase [a-z0-9] first, then [a-z0-9._-], max 64)
  or a path that doesn't match the SDK's protocol. Fix the URL and re-verify."
- **Provider error (4xx/5xx via gateway)**: report status + body verbatim; the
  gateway is reachable and auth passed, the upstream call failed — usually a
  provider key or model-name issue in the app itself.

Never report success on any of these. An unverified integration is reported as
unverified.
