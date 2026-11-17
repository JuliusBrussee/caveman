# packages/cli

`@caveman-ai/cli`, the `caveman` command (also installed as `cave`). It wires coding agents to the local proxy, launches them, and calls the Caveman Cloud REST API for connected commands. The published package is JS only with zero runtime dependencies; the Go companions (proxy, engine, mcp, mem, browse, shrink) are separate signed binaries that `caveman setup --install` downloads.

## Layout

- `src/index.ts`: most of the CLI: dispatch tables, HTTP helpers, token storage, wrap and native install, commands.
- `src/modules/`: `caveman on|off`, `status`, `doctor`, `stop`, config home (`config-home.ts`), provider logins.
- `src/git-safe.ts`: the only way to run `git` against a user directory (see `proxy/AGENTS.md`).
- `src/learn-tui.ts`: interactive `learn` view, bundled with Clack and loaded only on a real TTY.
- `src/*.generated.ts`: never edit. `agents/compile.mjs`, `integrations/compile.mjs` and `skills/compile.mjs` write them through `scripts/compile-registries.mjs`; `scripts/gen-binaries.mjs` writes `binaries.generated.ts` from `BINARY_RELEASE`; `scripts/gen-device-auth.mjs` copies `packages/device-auth`.
- `BINARY_RELEASE`, `BINARY_SIGNING_PUBKEY.pub`: the Go binary release the CLI installs and the key that verifies its manifest.
- `scripts/`: build steps (`bundle-delegate.mjs` copies `agents/delegate/caveman-delegate-mcp.mjs`, `bundle-tui.mjs`, `bundle-pi-extension.mjs`, `shebang.mjs`).
- `tests/*.runtime.mjs`: `node --test` suites that run the built CLI against HTTP stubs and stub binaries. `tests/setup-real.e2e.mjs` runs the real Claude Code (CI `first-run` job).
- `PUBLISHING.md`: release order for the npm package and its binary pin.

## Build and test

From the repo root:

```bash
pnpm install --frozen-lockfile
pnpm --filter @caveman-ai/cli build
pnpm --filter @caveman-ai/cli run test:fast   # CI on macOS; stubs every Go binary
go build -o /tmp/caveman-proxy ./proxy/cmd/caveman-proxy
env -u FORCE_COLOR -u ANTHROPIC_BASE_URL CAVEMAN_TEST_PROXY_BIN=/tmp/caveman-proxy pnpm --filter @caveman-ai/cli test   # full suite
node packages/cli/scripts/gen-device-auth.mjs --check
```

`scripts/install-local-cli.sh` (or `.ps1`) installs a local build and the Go binaries into `~/.caveman/bin`.

## Command surface

- Printed porcelain is `run`, `learn`, `login`, `status` and the agent shortcut, plus two namespaces: `caveman tools` (local) and `caveman cloud` (connected). `tools` and `cloud` print at most 15 verbs each (15 and 14 today; `tests/porcelain.runtime.mjs` asserts it). A fifth porcelain verb or a 16th namespace verb needs a retirement decision.
- Hidden verbs (`shrink-hook`, `practices`, `check`, `welcome`, `doctor`, `dev`, `deploy`, ...) stay callable but unprinted. Moved verbs keep their old bare spelling as silent aliases: no deprecation text may change piped output.
- Dispatch uses handler tables. Each handler gets its own rebased argv slice; never read `process.argv` positionally inside a handler. An agent id or binary name is dispatched last, so real commands always shadow it, and everything after the agent name goes to the agent verbatim.
- `caveman <agent>`: for hook-capable agents (claude, codex, hermes, gemini, opencode, pi) with a native install journal, it starts the proxy if needed and launches the host directly. Without a journal, the first run goes through onboarding and nothing machine-wide is written without consent. Leading flags, a Cave Build lock (`.caveman/agent.lock.json`), surfaces that refuse a routed base URL, other agents (aider has no hooks) and any enable failure fall back to the session-only `wrap` door. `caveman wrap|run <agent>` is always session-only.
- `enable <agent>` on claude or codex also writes the voice skills (`caveman`, `ultracave`, `megacave`) create-only (`wx`). They are not journal entries, `disable` leaves them, and the `~/.caveman/integrations/<agent>.voice-skills.json` sidecar makes it run once.
- Go binaries resolve through `cavemanBin()`: `CAVEMAN_*_BIN` env, then PATH, then `~/.caveman/bin`, then the bare name (so missing-binary panels still trigger). `caveman setup` reports each binary and exits non-zero when a required one (proxy, engine, mcp, mem) is missing; every degraded path prints a warning pointing at it.
- `login` uses the device flow (`/api/v1/auth/device/{code,token}`); `CAVE_TOKEN` is the non-interactive path. Logged-out connected verbs print one line and exit non-zero.

## Config and secrets

- Config lives in `$CAVEMAN_HOME/cloud.json` (default `~/.caveman`, 0600). The old `~/.caveman-cloud/config.json` is copied there once and kept for older CLIs; the telemetry decision is mirrored to it.
- The auth token lives in the macOS keychain (`security`, service `caveman`) or `~/.caveman/credentials` (0600), never in config.
- Capability config is grouped as `think`, `remember`, `execute`. Resolution: default < proxy YAML (start only) < legacy `wrap` < global < project < env. `./.caveman/config.json` may set only `think.toon`, `think.shrink`, `remember.*` and `execute.*`; it cannot change mode, pixel, account or consent. `caveman tools config get` shows each key's source.

## Invariants

- Zero published runtime dependencies. TUI libraries are bundled, lazy-loaded and kept off ordinary command startup.
- A capability may default on only when it is byte-safe or behind its path's gate: an eval gate on the managed gateway, recovery plus CCR on local wrap. Never an account or entitlement. A PR that flips a default names the clause and path. `record` mode is always pass-through.
- Subscription and OAuth sessions compress locally with no account. `CAVEMAN_WRAP_ENTITLED` is gone; both doors `delete` an inherited copy. Both doors stamp `CAVEMAN_RECOVERY` explicitly (`"mcp"` or empty, never inherited), from the agent's own MCP install, because an exported `mcp` would let the proxy elide bytes the agent cannot retrieve. Without MCP recovery, say compression is off and name `caveman mcp install <agent>`.
- Subscription savings are tokens only; a seat has no per-token price. A local same-model API equivalent may be shown only labeled as hypothetical.
- Plain OpenAI `/responses` and Gemini `generateContent` with MCP disabled stay byte-identical with zero compression accounting; the conformance matrix pins these fail-closed cases.
- `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1` is set only when true: local mode and the proxy's anthropic upstream is `api.anthropic.com` (#865: otherwise Claude Code drops the 1M context window). Never override a user value; `disable` removes it only while it still holds our value.
- `providers verify` prints the server's status; never a hard-coded one (`tests/providers-verify.runtime.mjs`).
- `plan` shows per-day savings labeled with `basis`; never project to monthly. There is one voice; no `--engineer` flag.
- Local savings stay `inferred`, currency-free in `learn`. `tokens/day` (a rate) and `tokens observed` (a window total) are never added together. Unknown values stay absent, never a synthetic zero.
- The first-run scan persists only the closed `caveman.local_scan.v1` aggregate; `login` or `sync` uploads it as `local-scan`. Prompts, outputs, paths and session rows never enter it.
- Telemetry is anonymous and opt-out: `DO_NOT_TRACK`, `CAVEMAN_TELEMETRY=0` and `caveman telemetry off` always win; CI never sends; non-TTY never persists the default. Hook entrypoints never wait on a telemetry POST. Widening what is collected means bumping `TELEMETRY_PROMPT_VERSION` (reprints the disclosure, never re-asks). Events go to the `cli-telemetry` function in `supabase/`.
- Third-party skill discovery scans both `~/.agents/skills` (the Skills CLI's global Codex path) and `~/.codex/skills`.

## Terminal output

- Machine output (`--json`, `--md`, compressed bytes, recipes, delegated command output) gets no decoration or prompt.
- Interactive UI needs stdin, stdout and stderr to be TTYs. `--plain`, `CAVEMAN_PLAIN=1`, `TERM=dumb` and pipes never prompt or animate; `NO_COLOR` disables color. Runtime tests assert the plain paths.
- Bounded inline output only: no full-screen app or alternate screen. Detail moves behind `--all`, `--json`, `--md` or a subcommand; it never disappears.
- Errors are one problem, one likely fix, one help pointer. No stack traces.
- First-run failures degrade to one dim line and the agent still launches.

See the root `AGENTS.md` for repo-wide rules.
