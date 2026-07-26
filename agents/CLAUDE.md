# public/agents — the agent-profile registry (`caveman wrap` data)

Declarative profiles for every AI coding agent `caveman wrap` can route through the byte-safe
gateway. The keystone (docs/UNIVERSAL_AGENT_WRAP_SPEC.md): adding an agent is a **data change**
(one JSON file here), not a code change — the CLI's injection appliers and the proxy's
wire-protocol adapters are the only code.

## Layout
- `profiles/*.json` — one profile per agent (`claude`, `codex`, `gemini`, `aider`, `opencode`, `hermes`, `openclaw`).
- `profiles/schema.json` — the profile contract (JSON Schema, draft-07).
- `reserved-verbs.json` — command tokens no profile id or binary name may shadow;
  compiled into the CLI and tested against dispatcher reality.
- `compile.mjs` — zero-dep compiler: validates every profile (fail-closed) and emits
  `agents.json` + the CLI's embedded `../cli/src/{agents,reserved-verbs}.generated.ts`.
- `agents.json` — generated published registry (do not hand-edit).

## How a profile points an agent at the gateway
`injection.method` is one of:
- `env` — set literal env vars; values may use `{{cave_base_url}}` / `{{cave_api_key}}` /
  `{{cave_proxy_url}}` / `{{cave_org_id}}` templates. A var that renders empty is **omitted**
  (so we never set an empty auth token).
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
- `claude-pretooluse` / `gemini-beforetool` / `opencode-plugin` / `hermes-plugin` /
  `openclaw-plugin` — **hard rewrite**: a `Bash` PreToolUse hook (Claude), a `BeforeTool`
  hook on `run_shell_command` (Gemini), or a `tool.execute.before`-style plugin
  (opencode/hermes/openclaw) deterministically reroutes the command before it runs.
  Claude + Gemini share one `installSettingsHook` (same settings.json shape, different
  event/matcher); every hard-rewrite method routes the command through `caveman shrink-hook`.
- `instruction-note` (+ `file`) — **soft model-nudge**: append a delimited "prefer
  `caveman shrink`" note to a file the agent auto-reads (`~/.codex/AGENTS.md`). Best-effort,
  idempotent, install→uninstall is a byte-exact round-trip. Used where no byte-safe hard
  rewrite exists — Codex's runtime rejects `updatedInput` (openai/codex#18491) and a
  PATH-shim corrupts pipes, so the nudge is its honest ceiling.
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

See ../../CLAUDE.md (root) · ../../docs/UNIVERSAL_AGENT_WRAP_SPEC.md · ../cli/CLAUDE.md
