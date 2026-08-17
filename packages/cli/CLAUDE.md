# packages/cli — `caveman` CLI

TypeScript CLI (`src/index.ts`) driving the local proxy and wrapping control-api
REST calls. Published package has zero runtime dependencies. Build emits
`dist/index.js`, dependency-free `dist/caveman-delegate-mcp.mjs`, and a lazy
bundled `dist/learn-tui.js` Clack chunk, then
`scripts/shebang.mjs` adds shebang + chmod. `bin` exposes both `caveman` and `cave`. Non-secret config lives at
`~/.caveman-cloud/config.json` (0o600); the **auth token lives in the OS keychain** (macOS
`security`) or a `~/.caveman/credentials` (0o600) fallback — never plaintext config.

## Layout
- `src/index.ts` — entire CLI: arg dispatch, HTTP helpers (`get`/`post`), token storage, all commands
- `src/learn-tui.ts` — bounded interactive learn view; bundled with Clack and loaded only for a real TTY
- `tests/*.runtime.mjs` — Node `--test` runtime tests; spawn the built binary against HTTP stubs (`providers-verify`, `wrap`, `login`, `compress`)
- `scripts/bundle-delegate.mjs` — copies canonical dependency-free delegate server into published `dist/`
- `scripts/bundle-tui.mjs` — bundles Clack into the self-contained learn TUI chunk
- `scripts/shebang.mjs` — post-build: prepends shebang, marks executable
- `package.json` — bin: `caveman`/`cave → dist/index.js`; build: registries → `tsc` → delegate/TUI bundles → shebang

## Command surface
`caveman <agent>` (e.g. `caveman claude`) is the **persistent default door**: for hook-capable native agents (claude/codex/hermes/gemini/opencode — not aider, whose hookless native install could never start the proxy) it runs the `caveman enable <agent>` user-scoped native install if no install journal exists, pre-starts the local proxy when it isn't listening (the native SessionStart hook re-does this, but only after the host approves the hooks), then launches the host binary directly — so plain `<agent>` stays caveman'd in every later session. It falls back to the ephemeral `wrap` door whenever the native path can't hold: wrap-session flags given (`--off`/`--pixel`/`--workflow`), a non-native agent, a Cave Build lock in cwd (lock enforcement is wrap-door-only), or any enable failure (e.g. missing caveman-mcp/proxy binaries) — the shortcut is never worse than a session-only wrap. Note the fallback is session-only relative to an install that may already exist: once the native install is in place, `--off`/`wrap` runs are ephemeral but the persistent routing stays until `caveman disable <agent>`. Any known agent id or binary name works as a top-level command; it is dispatched **last**, so real commands always shadow an agent name, and everything after the agent name goes to the agent verbatim. `caveman wrap <agent>` / `caveman run <agent>` remain the explicit session-only path.
`cave` is a permanent byte-compatible bin alias. Relocated verbs keep their bare
spellings as silent legacy aliases: no deprecation text may alter piped output.

Printed porcelain is `run`, `learn`, `login`, `status`, plus the agent shortcut.
Local capabilities live under `caveman tools`; account- or network-dependent
operations live under `caveman cloud`. `dev` and `deploy` are undocumented
maintainer aliases. `tools` is capped at 15 printed verbs and `cloud` at 15;
current counts are 15 and 14. Internal/advanced `shrink-hook`, `practices`, and
`check` remain callable through existing paths but are unprinted, including in
legacy `help tools --all` output.

Caveman's own Go binaries (proxy/engine/mcp/mem/browse/shrink) resolve via `cavemanBin()`: env override (`CAVEMAN_*_BIN`) → PATH → `~/.caveman/bin` (where `scripts/install-local-cli.sh` or `scripts/install-local-cli.ps1` builds them) → bare name (so missing-binary panels still trigger).
`caveman setup` prints per-binary install status — what works, what degrades to a loud byte-safe pass-through, and the one install command — and exits non-zero when a required binary (proxy/engine/mcp/mem; browse and shrink are optional) is missing. `caveman tools compress catalog` delegates to the dedicated `caveman-shrink` binary; `caveman tools shrink` remains command-output compression. It's the anti-silent-degrade front door for npm installs (the package ships JS only); every degraded path also prints its own warning line pointing at it. Publish checklist lives in `PUBLISHING.md`.
Local (no account): `start` (launch the proxy via `CAVEMAN_PROXY_BIN`; if the binary is missing or the port is already served it renders a status panel — build/`make dev`+live docker status/env — instead of a bare spawn error) · `wrap [agent]` (inject route and exec child; Claude, Codex, Hermes, Gemini, OpenCode, Aider, and OpenClaw launch by id; native-capable hosts get temp pack/home/config only, removed after exit; no user/project config write) · `enable|disable <agent>` (explicit user-scoped native install with atomic journal, lock, reversible owned-path restore, component doctor) · `inspect` / `why <decision-id>` (content-blind native receipt and Decision Ledger explanation) · `compress` (shells out to `caveman-engine`; byte-safe missing-binary pass-through; `inferred`) · `mcp install|uninstall [agent]` (explicit persistent recovery registration; wrap uses temporary recovery configuration only when `execute.mcp = auto` and a current local MCP binary exists; `marker-only` or `false` suppresses injection without silently removing user registrations) · `skills add <source>` (same Git/URL/local sources and flags as official `npx skills add`; download/selection stays upstream, forced copy mode lets Caveman pixelize only new/changed Claude Code/Codex `SKILL.md` bodies; resources remain untouched and third-party content remains explicitly unreviewed; `--no-pixel` opts out) · `skills import <dir|SKILL.md>` (blocked unevaluated native-skill draft; imported resources never execute) · `evals run` · `stats` · `convert`.
Connected namespace: `whoami · projects · keys · providers · billing · score ·
costs · plan · traces · experiments · receipts · audit · sync · agent`.
`doctor`, `opportunities`, `snippets`, `dev`, and `deploy` remain unprinted legacy
aliases; `status`, `plan`, and `tools sdk snippets` absorb their public jobs.

`login` polls control-api `/api/v1/auth/device/{code,token}`; `organization_id` is bound from the returned token. `CAVE_TOKEN` is the non-interactive CI path. Logged-out connected verbs print one line + exit non-zero (CI skips, never crashes).

**First-run experience** (TTY only, once per machine, marker `firstRunAt` in config): on the first interactive wrap — banner, 30-day retrospective scan (`caveman-proxy learn scan --retro`, read-only over local Claude Code/Codex session logs; base behavior pass capped at 20s, retro pass at 60s, child timeout derived as 90s including margin), count-up reveal of tokens sent / would-have-cut / rides-every-turn stream figure (sums over scanned sessions, never extrapolated; `inferred`; tokens never dollars; sent is deduped once per API response, base cut carries no ratio against sent, only the stream figure shares sent's basis), telemetry disclosure, one `[y/N]` account question (yes → device login → existing span backfill). CLI persists only the closed, content-free aggregate; login or explicit `sync` uploads it to the authenticated project's `local-scan` import lane. Prompts, outputs, paths, session rows, free-text caveats, and anonymous telemetry never enter that upload; dashboard keeps it `inferred` and separate from spend/verified savings. Every failure degrades to one dim line and the agent still launches. Replay with the **unprinted** `caveman welcome` (porcelain caps unchanged). Anonymous **telemetry defaults ON (opt-out)**: persisted v1 decisions and `DO_NOT_TRACK`/`CAVEMAN_TELEMETRY=0`/`caveman telemetry off` always win; CI/non-TTY never sends and never persists the default; the first eligible interactive run persists a stable anonymous id and prints the disclosure line. `command_run` also carries `tokens_processed`/`tokens_saved` read from the local proxy store as a **delta** against the `telemetryTokens` watermark in config, always with `tokens_basis` (`inferred`, tokens never dollars); the read is best-effort behind a 400ms spawn budget and omits the fields entirely when the store or binary is absent. Widening the disclosed scope means bumping `TELEMETRY_PROMPT_VERSION` (now 4) — that reprints the line once for stale-version opt-ins, never re-asks, never touches an opt-out.

`providers verify <conn>` → real POST `/api/v1/projects/{id}/providers/{conn}/verify` (no hardcoded status).

`plan` renders the Cave Plan in plain English (one operator voice); `--json` prints the raw response. There is **no** caveman voice / `--engineer` flag — the dual-voice was deliberately removed. Headline is labeled `basis` ("inferred"), savings are per-day — never reprojected to monthly. (honesty rule: no-fake-savings)

`learn` is summary-first: real terminals get animated progress, bounded score
and move cards, then one keyboard action menu. `--plain` restores compact text,
`--all` restores every sink id/class/practice/suggestion, while `--json` and
`--md` stay complete. `learn implement [claude|codex]
[--prompt <focus>]` installs the existing `caveman-learn` safety guide when
missing and launches the chosen interactive agent. The guide's per-edit consent,
load-bearing protection, re-measurement, and inferred-only rules remain binding.
Presentation contract: [`TERMINAL_UX.md`](TERMINAL_UX.md).

## Conventions
- Dispatch uses handler tables. Every handler receives its own rebased argv slice;
  never read process-global argv positionally inside a handler.
- `flag("--name", fallback)` parses named args from current invocation.
- Tests use `node --test` (Node built-in runner); run `tsc` first, test spins a real HTTP server
- Build: `pnpm build` (tsc + shebang); install locally: `scripts/install-local-cli.sh` (macOS/Linux) or `scripts/install-local-cli.ps1` (Windows) at repo root

## Capability promotion rule

A capability may default on only when it is byte-safe, or when protected by the
applicable path-specific gate: managed gateway uses an eval gate; local wrap
uses recovery + CCR — **not** an account or entitlement. There is
no eval gate in local `run`.
Any PR flipping a default must name the clause and path.

A verb enters porcelain only when its capability is automatic-by-default-safe
inside `run` and users no longer need to type it. Porcelain stays capped at four
verbs + agent shortcut + exactly two namespaces. A fifth verb, or a 16th printed
verb in either namespace, requires a retirement decision. `record` mode is always
pass-through.

Capability config is grouped in `~/.caveman-cloud/config.json` as `think`,
`remember`, and `execute`. `./.caveman/config.json` may only narrow its allowlisted
project-local keys; it cannot change `think.mode`, pixel settings, account state,
consent, or entitlement. Resolution is default < proxy YAML < legacy `wrap` <
global groups < project overlay < env. Env parity is knob-specific. Inspect
per-key source with `caveman tools config get`.

## Gotchas
- `providers verify` must NOT return a hardcoded status; the test asserts the CLI echoes the server's value (no-placeholder rule)
- `plan` savings display must stay per-day; never multiply to monthly projection
- Non-PAYG coverage includes Claude Pro/Max, Codex ChatGPT, Gemini OAuth, and routed compatible agents. Codex subscription mode keeps provider config ephemeral under `CODEX_HOME`, auto-installs its MCP recovery, and starts `/chatgpt/responses` in compress mode instead of forcing record/pass-through. Plain OpenAI `/responses` and Gemini `generateContent` requests with MCP explicitly disabled have no server-retrieval grammar, so they must remain byte-identical with zero compression accounting; the compression conformance matrix pins these protocol-specific fail-closed cases instead of requiring every profile to emit a CCR marker.
- Subscription/OAuth wrap sessions (Claude Pro/Max) compress **locally only**, live zone only, and with **no account**: `CAVEMAN_WRAP_ENTITLED` is gone from both doors and from the proxy, and both doors `delete` any inherited copy so a stray export cannot resurrect it. What both doors DO stamp is the recovery path (`CAVEMAN_RECOVERY`), explicitly (`"mcp"` or empty, never inherited): `wrap` answers it from the **agent's own** MCP install (an exported `CAVEMAN_RECOVERY=mcp` can't outlive that answer — it would have the proxy elide bytes behind markers this agent has no `caveman_retrieve` tool to expand), `start` from machine-wide MCP install evidence plus an explicit `CAVEMAN_RECOVERY=mcp` counted as the operator's own opt-in, re-stamped so the disclosure line and the proxy can never disagree; the compression disclosure line prints only when recovery holds, and no-MCP says compression is off and names `caveman mcp install <agent>`. The `subscription_compress: off` operator switch stays the operator's. Their savings are **tokens only** — a seat has no per-token price, so no dollar figure may ever appear for them, locally or in the synced span (no-fake-savings). The session-savings line treats `oauth` like `subscription` (OAuth is list-price-eligible on Vertex alone) and qualifies unconditionally when its capped auth-mode window is truncated
- Published runtime dependencies stay zero. TUI libraries must be bundled,
  lazy-loaded, and measured; do not move them onto ordinary command startup.
- `learn` uses bundled Clack only when stdin/stdout/stderr are TTYs. `--plain`,
  `CAVEMAN_PLAIN=1`, `TERM=dumb`, machine modes, and pipes must never prompt.
- Remaining terminal UX (status panels + `wrap` picker) lives in the small toolkit
  at the bottom of `src/index.ts`. Piped/non-interactive paths remain plain; runtime
  tests assert them.
- Current official Skills CLI installs global Codex sources at
  `~/.agents/skills`, despite older/direct Caveman installs using
  `~/.codex/skills`; third-party post-install discovery must scan both.

See ../../CLAUDE.md (root)
