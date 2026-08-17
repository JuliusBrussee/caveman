# agents — the agent-profile registry (`caveman wrap` data)

Profiles for every AI coding agent `caveman wrap` can route through the byte-safe gateway. The
keystone is a **collapse toward data**, not an absolute: for
most agents, adding one is a **data change** (one JSON file here) and the CLI's injection appliers
+ the proxy's wire-protocol adapters are the only code. But routing sits on a **three-tier honesty
scale** (`injection_completeness`), because several agents genuinely need code:

- **declarative** — the profile's `injection` block alone routes it (claude's default env, gemini,
  aider, opencode). A true data-only add.
- **builder-assisted** — the profile is the base, but the CLI augments it in code: an
  `overlayBuilders.<id>` config-overlay builder (openclaw), or an auth/bedrock short-circuit
  (`applyClaudeBedrockWrap` for claude, `applyHermesAuthEnv` for hermes).
- **code-only** — the declared `injection` is inert and all routing is code (codex ships
  `injection.env: {}`; `buildCodexEphemeralWrapEnv` + `codexCavemanProviderToml` do the real work).

`compile.mjs` DERIVES the true tier from the CLI (`overlayBuilders.<id>` + named wrap builders
scanned from `index.ts`) plus the inert-injection rule, and **fails closed** when a profile's
declared `injection_completeness` claims more data-purity than reality (e.g. `declarative` for an
agent that needs a builder). Separately, `buildWrapEnv` first sprays a generic base-URL union
(`WRAP_BASE_URL_ENV_VARS` in `../cli/src/index.ts`) as a fail-open fallback that any SDK subprocess
children inherit; each agent reads only its own subset, and the profile's injection then layers on
top.

## Layout
- `profiles/*.json` — one profile per agent (`claude`, `codex`, `gemini`, `aider`, `opencode`, `hermes`, `openclaw`).
- `profiles/schema.json` — the profile contract (JSON Schema, draft-07).
- `reserved-verbs.json` — command tokens no profile id or binary name may shadow;
  compiled into the CLI and tested against dispatcher reality.
- `compile.mjs` — zero-dep compiler: validates every profile (fail-closed) and emits
  `agents.json` + the CLI's embedded `../cli/src/{agents,reserved-verbs}.generated.ts`. It also
  fails closed on four honesty invariants: any model id pinned in the injection config must be
  **priced by the provider catalog** (`../shared/provider-catalog/catalog/current.yaml`); a declared
  `injection_completeness` must match the CLI's real routing; the `agent-conformance.yml` CI pins
  are **derived from `tested_agent_version`** and must equal it; and a declared `last_verified_at`
  must be inside the staleness budget.
- `agents.json` — generated published registry (do not hand-edit).
- `probe-installed.mjs` — isolated `--version` + `--help` gate for installed profile binaries;
  `--require <id>` and `--all` fail on missing, broken, or version-drifted binaries. `--allow-newer`
  reclassifies a working binary **newer** than the pin as `drift` (not `broken`) — used by the
  non-blocking @latest lane.
- `drift-report.mjs` — turns a `probe --allow-newer --json` result into one GitHub issue per
  drifted agent (idempotent; opens or updates). Runs only in the non-blocking CI lane.
- `.github/workflows/agent-conformance.yml` — daily/manual real-engine compression contract for
  all profiles plus one clean **pinned** upstream-binary probe per profile, and a separate
  **non-blocking `@latest` drift-report** lane that files drift issues.

`tested_agent_version` is evidence, not compatibility theater. Doctor compares
installed version, downgrades unknown/newer/older hosts to safe inactive subsets,
and reports binary-present-but-unlaunchable separately. Claude 2.1.226 is current
locally grounded pin; other pins remain unchanged until equivalent proof exists.

## How a profile points an agent at the gateway
`injection.method` is one of:
- `env` — set literal env vars; values may use `{{cave_base_url}}` / `{{cave_api_key}}` /
  `{{cave_proxy_url}}` / `{{cave_org_id}}` templates. A var that renders empty is **omitted**
  (so we never set an empty auth token). Base-URL/API-base keys may append a compiler-validated
  safe path such as `/openai/v1`; secret keys cannot.
- `config-env-content` — render a mode-selected inline JSON config (`config_content.local` for
  BYOK `caveman start`; `config_content.managed` when `CAVE_GATEWAY_URL` is off-loopback) and set
  it as one env var. This is how **opencode** is wrapped (`OPENCODE_CONFIG_CONTENT`) without ever
  touching the user's `opencode.json`. An agent's own `{env:VAR}` tokens are left untouched.
- `config-file` — merge a mode-selected config overlay into a temp copy of the agent's own
  config file on disk (how **openclaw** is wrapped, without mutating the user's real config).

## How a profile auto-shrinks command output (`command_hook`, optional)
`command_hook` declares how `caveman wrap`/`caveman hooks` route the agent's noisy
shell-command output through `caveman shrink` (RTK parity, byte-exact recoverable).
Three honest tiers — never claim a rewrite an agent can't do:
- `claude-pretooluse` / `codex-pretooluse` / `gemini-beforetool` / `opencode-plugin` / `hermes-plugin` /
  `openclaw-plugin` — **hard rewrite**: a `Bash` PreToolUse hook (Claude), a `BeforeTool`
  hook on `run_shell_command` (Gemini), or a `tool.execute.before`-style plugin
  (opencode/hermes/openclaw) deterministically reroutes the command before it runs.
  Claude + Gemini share one `installSettingsHook` (same settings.json shape, different
  event/matcher); every hard-rewrite method routes the command through `caveman shrink-hook`.
- `instruction-note` (+ `file`) — **soft model-nudge**: append a delimited "prefer
  `caveman shrink`" note to a file the agent auto-reads. Best-effort, idempotent,
  install→uninstall is a byte-exact round-trip. Retained only for hosts without a
  current hard hook surface. Codex now uses its native `PreToolUse` hook in
  `~/.codex/hooks.json` and supports `updatedInput` when allowing the tool.
- **absent** — **manual-only**: no installable surface (e.g. Aider); `hooks` just prints
  the `caveman shrink -- <cmd>` guidance.

## How a profile declares a skill surface (`skills`, optional)
`skills` declares where the agent keeps on-disk skills so `caveman convert` can
pixel-compress their bodies (SKILL.md frontmatter stays text — it drives skill
discovery; only the body becomes PNG pages). `format` is a closed enum (only
`skill-md`: `<root>/<name>/SKILL.md` with YAML frontmatter — the Claude Code /
Codex convention); `user_dirs` are `~`-resolved roots, `project_dirs` are
repo-relative and scanned only with `--project`. **Absent = no verified skill
convention** — `convert` skips the agent with an honest note, never guesses a path.
- **Source format is JSON, not YAML** — deliberately, to keep the CLI **zero-runtime-dep** (the
  compiler needs no parser; the CLI imports the generated TS, never reads a file at runtime).
- **fail-closed**: unknown fields/enums/templates, command collisions, unsafe env
  keys/values, and paths outside the profile's own hidden home directory fail
  compile — never a guessed protocol, redirect, loader override, or arbitrary
  file read/write.
- The compiler runs in the CLI build **and** test (`node ../agents/compile.mjs`); the generated
  `agents.generated.ts` is committed and must stay in sync.
- `wire_protocol` must be one the proxy speaks natively (anthropic-messages · openai-chat ·
  openai-responses · gemini-generatecontent) — we don't translate protocols in the wrap path.

See ../../CLAUDE.md (root) · ../cli/CLAUDE.md
