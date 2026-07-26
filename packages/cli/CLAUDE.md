# public/cli — `caveman` CLI

Single-file TypeScript CLI (`src/index.ts`) driving the local proxy and wrapping control-api
REST calls. No runtime dependencies; builds to `dist/index.js` via `tsc` + `scripts/shebang.mjs`
(adds shebang + chmod). `bin` exposes both `caveman` and `cave`. Non-secret config lives at
`~/.caveman-cloud/config.json` (0o600); the **auth token lives in the OS keychain** (macOS
`security`) or a `~/.caveman/credentials` (0o600) fallback — never plaintext config.

## Layout
- `src/index.ts` — entire CLI: arg dispatch, HTTP helpers (`get`/`post`), token storage, all commands
- `tests/*.runtime.mjs` — Node `--test` runtime tests; spawn the built binary against HTTP stubs (`providers-verify`, `wrap`, `login`, `compress`)
- `scripts/shebang.mjs` — post-build: prepends shebang, marks executable
- `package.json` — bin: `caveman`/`cave → dist/index.js`; build: `tsc && node scripts/shebang.mjs`

## Command surface
`caveman <agent>` (e.g. `caveman claude`) is shorthand for `caveman wrap <agent>` — any known agent id or binary name works as a top-level command; it is dispatched **last**, so real commands always shadow an agent name, and everything after the agent name goes to the agent verbatim.
`cave` is a permanent byte-compatible bin alias. Relocated verbs keep their bare
spellings as silent legacy aliases: no deprecation text may alter piped output.

Printed porcelain is `run`, `learn`, `login`, `status`, plus the agent shortcut.
Local capabilities live under `caveman tools`; account- or network-dependent
operations live under `caveman cloud`. `dev` and `deploy` are undocumented
maintainer aliases. `tools` is capped at 15 printed verbs and `cloud` at 15;
current counts are 15 and 14.

Caveman's own Go binaries (proxy/engine/mcp/browse) resolve via `cavemanBin()`: env override (`CAVEMAN_*_BIN`) → PATH → `~/.caveman/bin` (where `scripts/install-local-cli.sh` builds them) → bare name (so missing-binary panels still trigger).
`caveman setup` prints per-binary install status — what works, what degrades to a loud byte-safe pass-through, and the one install command — and exits non-zero when a required binary (proxy/engine/mcp; browse is optional) is missing. It's the anti-silent-degrade front door for npm installs (the package ships JS only); every degraded path also prints its own warning line pointing at it. Publish checklist lives in `PUBLISHING.md`.
Local (no account): `start` (launch the proxy via `CAVEMAN_PROXY_BIN`; if the binary is missing or the port is already served it renders a status panel — build/`make dev`+live docker status/env — instead of a bare spawn error) · `wrap [agent]` (inject `ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL` = `CAVE_GATEWAY_URL` or the local proxy, exec child; known agents `claude`/`codex`/`gemini`/`aider` launch by id with install detection; an unknown/unfound target shows an install hint or the wrappable list; bare `wrap` in a TTY opens an arrow-key picker) · `compress` (shells out to `caveman-engine` via `CAVEMAN_ENGINE_BIN`; byte-safe pass-through fallback if the binary is missing; `inferred`) · `toon encode|decode` (stateless JSON⇄TOON converter via the engine binary; `encode` degrades byte-safe when the engine is missing, `decode` fails loudly — it must not emit raw TOON as JSON) · `wrap --toon` (sets `CAVE_ENGINE_TOON=best-of` on the spawned proxy so it re-encodes uniform JSON the model reads — tool results — as TOON when smaller; opt-in, implies `--compress`) · `mcp install|uninstall [agent]` (register/remove the caveman_retrieve MCP tool; compress-mode wrap **auto-installs** it when a real `caveman-mcp` executable resolves — never the npx fallback — because streams are only compressible with agent-side recovery; opt out with `--no-mcp`/`--minimal`) · `evals run` (delegates to `caveman-engine evals run`, forwards its exit code) · `stats` (delegates to `caveman-proxy stats`) · `convert` (pixel-compresses installed agent skills in place: SKILL.md **body** → `SKILL.pxN.png` pages via `caveman-engine pixel render`, frontmatter stays text so discovery/triggering still works, body becomes a stub telling the agent to read the images on invocation; dirs come from registry profiles with a `skills` block — claude + codex today; converts only when image+stub est tokens < text est tokens, else untouched; original kept byte-exact as `SKILL.orig.md`, `--revert` restores; every skip is reported with its reason; savings `inferred`, per-invocation) · `skills install [caveman|caveman-learn]` (writes the embedded SKILL.md; **auto-pixel by default** via the same convert routine, `--no-pixel` opts out, honest plain-text fallback when the engine is missing or the gate says not smaller; `--agent codex` writes `~/.codex/skills/<name>/SKILL.md`).
Connected namespace: `whoami · projects · keys · providers · billing · score ·
costs · plan · traces · experiments · receipts · audit · sync · agent`.
`doctor`, `opportunities`, `snippets`, `dev`, and `deploy` remain unprinted legacy
aliases; `status`, `plan`, and `tools sdk snippets` absorb their public jobs.

`login` polls control-api `/api/v1/auth/device/{code,token}`; `organization_id` is bound from the returned token. `CAVE_TOKEN` is the non-interactive CI path. Logged-out connected verbs print one line + exit non-zero (CI skips, never crashes).

`providers verify <conn>` → real POST `/api/v1/projects/{id}/providers/{conn}/verify` (no hardcoded status).

`plan` renders the Cave Plan in plain English (one operator voice); `--json` prints the raw response. There is **no** caveman voice / `--engineer` flag — the dual-voice was deliberately removed. Headline is labeled `basis` ("inferred"), savings are per-day — never reprojected to monthly. (honesty rule: no-fake-savings)

## Conventions
- Dispatch uses handler tables. Every handler receives its own rebased argv slice;
  never read process-global argv positionally inside a handler.
- `flag("--name", fallback)` parses named args from current invocation.
- Tests use `node --test` (Node built-in runner); run `tsc` first, test spins a real HTTP server
- Build: `pnpm build` (tsc + shebang); install locally: `scripts/install-local-cli.sh` at repo root

## Capability promotion rule

A capability may default on only when it is byte-safe, or when protected by the
applicable path-specific gate: managed gateway uses an eval gate; local wrap
uses entitlement + recovery + CCR. There is no eval gate in local `run`.
Any PR flipping a default must name the clause and path.

A verb enters porcelain only when its capability is automatic-by-default-safe
inside `run` and users no longer need to type it. Porcelain stays capped at four
verbs + agent shortcut + exactly two namespaces. A fifth verb, or a 16th printed
verb in either namespace, requires a retirement ADR. `record` mode is always
pass-through. See [ADR 0024](../../docs/decisions/0024-cli-porcelain-and-capability-promotion.md).

Capability config is grouped in `~/.caveman-cloud/config.json` as `think`,
`remember`, and `execute`. `./.caveman/config.json` may only narrow its allowlisted
project-local keys; it cannot change `think.mode`, pixel settings, account state,
consent, or entitlement. Resolution is default < proxy YAML < legacy `wrap` <
global groups < project overlay < env. Env parity is knob-specific. Inspect
per-key source with `caveman tools config get`.

## Gotchas
- `providers verify` must NOT return a hardcoded status; the test asserts the CLI echoes the server's value (no-placeholder rule)
- `plan` savings display must stay per-day; never multiply to monthly projection
- Subscription/OAuth wrap sessions (Claude Pro/Max) compress **locally only**, live zone only, and only with a wrap entitlement: **both** proxy entry points — `startWrapProxy` (wrap) and `start` (`caveman start`) — stamp `CAVEMAN_WRAP_ENTITLED` explicitly `1`/`0` (never inherited, so an env claim can't beat the account gate at either door); the `subscription_compress: off` operator switch stays the operator's. The entitlement is only **half** the proxy's gate — it also needs a recovery path (`CAVEMAN_RECOVERY=mcp`), so both doors stamp that too, and just as explicitly (`"mcp"` or empty, never inherited): `wrap` answers it from the **agent's own** MCP install (an exported `CAVEMAN_RECOVERY=mcp` can't outlive that answer — it would have the proxy elide bytes behind markers this agent has no `caveman_retrieve` tool to expand), `start` from machine-wide MCP install evidence plus an explicit `CAVEMAN_RECOVERY=mcp` counted as the operator's own opt-in, re-stamped so the disclosure line and the proxy can never disagree; the compression disclosure line prints only when BOTH halves hold; entitled-but-no-MCP says compression is off and names `caveman mcp install <agent>`. Their savings are **tokens only** — a seat has no per-token price, so no dollar figure may ever appear for them, locally or in the synced span (no-fake-savings). The session-savings line treats `oauth` like `subscription` (OAuth is list-price-eligible on Vertex alone) and qualifies unconditionally when its capped auth-mode window is truncated. The stamp is a **product gate, not a security boundary** — it makes the paid capability follow the account, and the proxy re-checks it at the point the compress path is chosen (ADR 0023 Decision 3); the machine's own operator can always edit local state, and no doc or sales line may claim otherwise
- No framework — zero runtime deps; keep it that way unless there's a strong reason
- The cohesive terminal UX (status panels + the `wrap` agent picker) is a small zero-dep toolkit at the bottom of `src/index.ts` and is **TTY-gated** via `interactive()`/`useColor()`. Piped/non-interactive runs degrade to plain one-line errors — the runtime tests assert those non-TTY paths (`wrap` with no command must still print `usage: caveman wrap`). Keep the fallback.

See ../../CLAUDE.md (root) · ../../docs/design.md
