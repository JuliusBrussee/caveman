# public/cli — `cave` CLI

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

## Commands
`caveman <agent>` (e.g. `caveman claude`) is shorthand for `caveman wrap <agent>` — any known agent id or binary name works as a top-level command; it is dispatched **last**, so real commands always shadow an agent name, and everything after the agent name goes to the agent verbatim.
Caveman's own Go binaries (proxy/engine/mcp/browse) resolve via `cavemanBin()`: env override (`CAVEMAN_*_BIN`) → PATH → `~/.caveman/bin` (where `scripts/install-local-cli.sh` builds them) → bare name (so missing-binary panels still trigger).
`caveman setup` prints per-binary install status — what works, what degrades to a loud byte-safe pass-through, and the one install command — and exits non-zero when a required binary (proxy/engine/mcp; browse is optional) is missing. It's the anti-silent-degrade front door for npm installs (the package ships JS only); every degraded path also prints its own warning line pointing at it. Publish checklist lives in `PUBLISHING.md`.
Local (no account): `start` (launch the proxy via `CAVEMAN_PROXY_BIN`; if the binary is missing or the port is already served it renders a status panel — build/`make dev`+live docker status/env — instead of a bare spawn error) · `wrap [agent]` (inject `ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL` = `CAVE_GATEWAY_URL` or the local proxy, exec child; known agents `claude`/`codex`/`gemini`/`aider` launch by id with install detection; an unknown/unfound target shows an install hint or the wrappable list; bare `wrap` in a TTY opens an arrow-key picker) · `compress` (shells out to `caveman-engine` via `CAVEMAN_ENGINE_BIN`; byte-safe pass-through fallback if the binary is missing; `inferred`) · `toon encode|decode` (stateless JSON⇄TOON converter via the engine binary; `encode` degrades byte-safe when the engine is missing, `decode` fails loudly — it must not emit raw TOON as JSON) · `wrap --toon` (sets `CAVE_ENGINE_TOON=best-of` on the spawned proxy so it re-encodes uniform JSON the model reads — tool results — as TOON when smaller; opt-in, implies `--compress`) · `mcp install|uninstall [agent]` (register/remove the caveman_retrieve MCP tool; compress-mode wrap **auto-installs** it when a real `caveman-mcp` executable resolves — never the npx fallback — because streams are only compressible with agent-side recovery; opt out with `--no-mcp`/`--minimal`) · `evals run` (delegates to `caveman-engine evals run`, forwards its exit code) · `stats` (delegates to `caveman-proxy stats`) · `convert` (pixel-compresses installed agent skills in place: SKILL.md **body** → `SKILL.pxN.png` pages via `caveman-engine pixel render`, frontmatter stays text so discovery/triggering still works, body becomes a stub telling the agent to read the images on invocation; dirs come from registry profiles with a `skills` block — claude + codex today; converts only when image+stub est tokens < text est tokens, else untouched; original kept byte-exact as `SKILL.orig.md`, `--revert` restores; every skip is reported with its reason; savings `inferred`, per-invocation) · `skills install [caveman|caveman-learn]` (writes the embedded SKILL.md; **auto-pixel by default** via the same convert routine, `--no-pixel` opts out, honest plain-text fallback when the engine is missing or the gate says not smaller; `--agent codex` writes `~/.codex/skills/<name>/SKILL.md`).
Connected: `login` (RFC-8628 device flow) · `logout · whoami · init · doctor · projects · keys · providers list/verify · audit · score · costs · plan · traces · opportunities · experiments · sdk snippet · dev · deploy · version`.

`login` polls control-api `/api/v1/auth/device/{code,token}`; `organization_id` is bound from the returned token. `CAVE_TOKEN` is the non-interactive CI path. Logged-out connected verbs print one line + exit non-zero (CI skips, never crashes).

`providers verify <conn>` → real POST `/api/v1/projects/{id}/providers/{conn}/verify` (no hardcoded status).

`plan` renders the Cave Plan in plain English (one operator voice); `--json` prints the raw response. There is **no** caveman voice / `--engineer` flag — the dual-voice was deliberately removed. Headline is labeled `basis` ("inferred"), savings are per-day — never reprojected to monthly. (honesty rule: no-fake-savings)

## Conventions
- All commands in one flat `if`-chain in `main()` — add new commands there
- `flag("--name", fallback)` parses named args; positional args via `args[N]`
- Tests use `node --test` (Node built-in runner); run `tsc` first, test spins a real HTTP server
- Build: `pnpm build` (tsc + shebang); install locally: `scripts/install-local-cli.sh` at repo root

## Gotchas
- `providers verify` must NOT return a hardcoded status; the test asserts the CLI echoes the server's value (no-placeholder rule)
- `plan` savings display must stay per-day; never multiply to monthly projection
- No framework — zero runtime deps; keep it that way unless there's a strong reason
- The cohesive terminal UX (status panels + the `wrap` agent picker) is a small zero-dep toolkit at the bottom of `src/index.ts` and is **TTY-gated** via `interactive()`/`useColor()`. Piped/non-interactive runs degrade to plain one-line errors — the runtime tests assert those non-TTY paths (`wrap` with no command must still print `usage: caveman wrap`). Keep the fallback.

See ../../CLAUDE.md (root) · ../../docs/design.md
