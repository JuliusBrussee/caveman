# Security and privacy

This document describes current repository behavior. Published releases can lag
source; verify the tag you install when policy depends on an exact version.

## Supported versions

Only the latest stable release receives security patches.

## Report a vulnerability

Do not open a public issue for suspected arbitrary code execution, path escape,
credential exposure, proxy isolation failure, recovery-data exposure, or similar
security bugs. Use [GitHub private vulnerability
reporting](https://github.com/JuliusBrussee/caveman/security/advisories/new).

## Data-flow summary

| Surface | Caveman account required? | Where content goes |
|---|---:|---|
| Caveman skill and classic output hooks | No | Local agent context and local files. These components do not directly call a Caveman service. |
| Local Proxy + Engine | No | Request content, possibly transformed, and provider credentials go to the provider selected by the agent. Recovery originals stay in local CCR storage unless the agent retrieves and sends them later. |
| Agent SDK `observe-only` | No | Directly to the configured provider. No Caveman gateway telemetry. |
| Managed Caveman gateway | Yes | Requests and responses transit Caveman Cloud and the selected provider. Do not treat managed mode as local-only. |
| CLI usage telemetry | No | Content-free usage events, including token counts processed and saved, go to Caveman by default (opt-out) and are stored with the sender's IP address. First interactive run prints the disclosure; `caveman telemetry off` or `DO_NOT_TRACK=1` turns it off for good. |
| Authenticated dashboard sync | Yes | Local span metadata and aggregate findings go to Caveman Cloud when credentials are present. Raw prompt and response bodies are excluded. |

Your model provider, MCP servers, browser targets, agent plugins, and any command
the agent runs remain separate data processors. Caveman cannot make those tools
offline or private.

## CLI usage telemetry

Telemetry is **on by default and opt-out**.
The default is never silent: the first interactive command persists the decision
(with a stable random install ID) and prints a one-line disclosure naming the
scope and the off switch. Nothing sends before that disclosure run, and CI /
non-interactive runs never persist the default. Once a yes is persisted, agent
sessions started through caveman's native hooks (which have no terminal) also
send a `session_start` event from a background process. Login is not
required; events go to `https://xvfgtprkhzlvegvmeefq.supabase.co/functions/v1/cli-telemetry`,
a Supabase Edge Function that validates each event and stores it in Caveman's
Supabase database. Its source and table schema live in [`supabase/`](./supabase/).

```bash
caveman telemetry status
caveman telemetry on
caveman telemetry off
```

Controls, in precedence order:

- non-empty, non-zero `DO_NOT_TRACK` forces telemetry off;
- `CAVEMAN_TELEMETRY=1|true|on` enables it and other non-empty values disable it;
- CI is always off; a non-interactive run (such as a native agent hook) sends
  only under a yes already persisted by an interactive run, and never persists
  one itself;
- otherwise the persisted choice in `~/.caveman-cloud/config.json` applies —
  a persisted opt-out (from any version, including the old opt-in prompt's "no")
  is honored forever;
- no persisted choice means on, persisted with a printed disclosure on the
  first interactive command.

Agent hooks started by desktop apps or background services may never read your
shell profile, so an environment variable alone can miss them. When an
interactive command sees `DO_NOT_TRACK` or `CAVEMAN_TELEMETRY=0` while the saved
choice is on, it saves a lasting opt-out. `caveman telemetry off` does the same
immediately.

`CAVEMAN_TELEMETRY_URL` overrides the destination, mainly for testing. Telemetry
requests time out after 1.5 seconds and failures do not fail the CLI command.

When the disclosed scope widens, the persisted decision carries the wording
version it was made under. A wider scope reprints the disclosure once on the next
interactive command and bumps the stored version; it never re-asks, never flips a
decision, and never touches a persisted opt-out. Version 4 added the token
totals below; version 5 added the IP address, agent session starts, account
and install type, timezone, and locale.

This telemetry is pseudonymous, not anonymous: the install ID links one
install's events together, and the stored IP address shows where they came
from. IP addresses are cleared from stored events after 90 days; the rest of
each event is kept. Separately, Supabase's platform request logs record each
request's IP address and approximate location derived by Cloudflare (city,
region, country, network) for the Supabase plan's log retention period; the
90-day clearing covers the events table, not those logs.

Events can contain:

- the IP address the request came from, as seen by Supabase's edge network
  (not a value the client can set). Stored with every event and used to
  rate-limit each sender; the server also records when it received the event;
- event name and client timestamp; random install ID; CLI version; OS;
  architecture; Node major version;
- account state (signed in or not, and the cached plan name), how the CLI was
  installed (npm, npx, pnpm, bun, or a source checkout; never the path),
  timezone, and locale;
- agent session starts: which agent launched (Claude Code, Codex, ...) and
  whether the session was new, resumed, or cleared, sent once per host session
  by the native SessionStart hook, with the same token increment described
  below;
- allowlisted command, subcommand, and known agent ID; duration; outcome; broad
  error class;
- tokens processed and tokens saved by the local Proxy, as the increment since
  the last event rather than lifetime totals, always carrying their measurement
  basis (`inferred` — tokenizer estimates, never billed counts, never a dollar
  figure). Read as an aggregate over the local store; when no store or Proxy
  binary is present the fields are omitted rather than reported as zero. The
  first read on a machine only records a baseline and reports nothing, so a store
  holding traffic from before this disclosure is never reported retroactively;
- local Proxy session aggregates: request and token counts, compression counts,
  cache read/write counts, measurement mode, and headline-suppression state;
- first-run aggregate scan counts from local Claude Code or Codex history,
  including sessions, tokens, estimated cuts, scan timing, and whether an
  account was already connected;
- Caveman MCP tool name, duration, and outcome.

Telemetry does **not** include prompt or completion bodies, raw argv,
file paths, tool arguments or results, provider credentials, or local database
rows/files. Source enforcement and runtime tests live in
[`packages/cli/src/index.ts`](./packages/cli/src/index.ts),
[`packages/cli/tests/telemetry.runtime.mjs`](./packages/cli/tests/telemetry.runtime.mjs),
and the receiving side in
[`supabase/functions/cli-telemetry/`](./supabase/functions/cli-telemetry/), which
stores only the fields listed above and drops malformed events.

## Authenticated Caveman Cloud traffic

Connected commands require stored credentials or `CAVE_TOKEN`; new logins are
blocked during beta. Sync sends usage metadata and aggregate findings, never
prompts, responses, credentials, tool evidence, or source paths. Subscription
traffic omits dollar figures, and synced local data remains `inferred`. Managed
gateway mode carries request and response content through Caveman Cloud; local
mode sends it only to your provider. `CAVEMAN_OFFLINE=1` disables entitlement
refresh and sync, but opted-in telemetry needs `CAVEMAN_TELEMETRY=0` or
`DO_NOT_TRACK=1` too.

## Local storage

Caveman stores runtime data under `~/.caveman/` and account/config state under
`~/.caveman-cloud/` unless a documented environment override changes a path.
Important files include:

- `~/.caveman/caveman.db`: per-request metadata, usage, local savings estimates,
  transformed prefix replacements, and related local evidence. Normal request
  rows do not store raw request or response bodies, but transformed content can
  remain in this database. Treat it as sensitive.
- `~/.caveman/ccr.db`: exact originals for recoverable transforms. This file can
  contain prompts, credentials embedded in content, and tool results. Treat it
  as sensitive.
- explicit `caveman trial` runs store raw request payloads in the local
  `trial_payloads` table for replay. Reports exclude those payloads.
- local learn/first-run scans read supported Claude Code and Codex history files
  and write aggregate reports/state locally. Raw session content is not included
  in CLI telemetry or authenticated scan sync.
- `~/.caveman-cloud/config.json`: endpoints, project/account pointers, telemetry
  decision, and other CLI state.
- account credentials: macOS Keychain when available, otherwise
  `~/.caveman/credentials` with file mode `0600`. `CAVE_TOKEN` remains owned by
  the parent environment.

CCR SQLite files and sidecars are created or tightened to mode `0600` and refuse
unsafe symlink/non-regular-file paths. This is filesystem access control, not
database encryption. Default retained CCR payload budget is 512 MiB;
`CAVEMAN_CCR_MAX_BYTES` can change it. Existing recovery handles are never
evicted. When the budget is exhausted, new recovery writes fail and lossy
transforms must fall back to pass-through.

Uninstall removes installed integrations and hooks. Do not assume it erases
runtime databases, reports, backups, or credentials; inspect `~/.caveman/` and
`~/.caveman-cloud/` separately if data deletion is required.

## Local Proxy security

`caveman start` defaults to `127.0.0.1:8787`. Standalone Proxy authentication
accepts every inbound request because loopback, single-operator isolation is the
security boundary. Startup rejects non-loopback `--host` and `CAVEMAN_LISTEN`
values. A firewall does not turn standalone mode into an authenticated external
gateway; use the managed authenticated gateway for remote access.

Proxy upstream clients apply SSRF controls. Compression is recovery-first:
parse failure, unsafe transform, unavailable durable recovery, storage failure,
or a result that is not smaller returns original bytes instead of a lossy
replacement. This reduces corruption risk; it does not make model output or
third-party tools trustworthy.

## Install and update network access

Network installers fetch source from GitHub and may invoke npm or agent-specific
registries. Per-agent installers can contact Anthropic/GitHub, Gemini extension,
the Oh My Pi plugin manager, npm, or other configured registries. Detached hook
installation downloads files from an immutable release tag and verifies SHA-256
manifest entries. Runtime companion setup downloads a signed checksum manifest
and verifies each binary's signature and SHA-256 before installation.

For inspection-first installation, clone a pinned tag and run the local installer
instead of piping a remote script into a shell. A source clone avoids installer
downloads only when required dependencies and runtime binaries are already
available locally.

## Scanner warnings

- Windows Defender or SmartScreen can flag `install.ps1` because it pipes a
  downloaded script into PowerShell and writes agent configuration. Clone and
  inspect the pinned source first if policy forbids pipe-to-shell installation.
- Generic scanners can flag `caveman-compress` because it rewrites the file the
  user names and creates a backup. That file mutation is intentional. Review
  [`skills/caveman-compress/`](./skills/caveman-compress/) before enabling it.
