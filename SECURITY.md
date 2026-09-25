# Security and privacy

This document describes current repository behavior. Published releases can lag
source; verify the tag you install when policy depends on an exact version.

## Supported versions

| Component | Supported line | Security fixes |
|---|---|---|
| Caveman skill, installer, CLI (`@caveman-ai/cli`), runtime binaries (`bin-v*`, container image) | Latest stable release | Yes |
| SDKs: `@caveman-ai/sdk`, `caveman-sdk` | 1.x, latest minor | Yes |
| Middleware: `@caveman-ai/middleware`, `caveman-middleware` | 0.x, latest published version (alpha today) | Yes, in the next 0.x version. No backports to older 0.x versions. |
| Anything older than the lines above | — | No |

The `middleware` modules inside the 1.x SDKs (`@caveman-ai/sdk/middleware`,
`caveman_cloud.middleware`) get security fixes on the SDK 1.x line, but their
API is experimental like the 0.x middleware packages: it can change in a minor
release.

Response policy for reports sent through the channel below:

- acknowledgement within 3 business days;
- a fix or mitigation for a confirmed critical issue targeted within 30 days,
  with a published advisory once a fixed version is available.

These are policy targets for a small maintainer team, not a contractual SLA.

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
| Framework middleware (client, adapters, and the runtime you host) | No | Your app keeps calling its provider directly. The adapters send eligible tool-result text to the runtime you point them at, and the runtime stores the originals so the model can fetch them back. Nothing goes to Caveman servers. See [Framework middleware data](#framework-middleware-data). |
| Anonymous CLI telemetry | No | Content-free usage events, including token counts processed and saved, go to Caveman by default (opt-out). First interactive run prints the disclosure; `caveman telemetry off` or `DO_NOT_TRACK=1` turns it off for good. |
| Authenticated dashboard sync | Yes | Local span metadata and aggregate findings go to Caveman Cloud when credentials are present. Raw prompt and response bodies are excluded. |

Your model provider, MCP servers, browser targets, agent plugins, and any command
the agent runs remain separate data processors. Caveman cannot make those tools
offline or private.

## Framework middleware data

The middleware client (`@caveman-ai/sdk/middleware`, `caveman_cloud.middleware`),
the adapters (`@caveman-ai/middleware`, `caveman-middleware`), and the
`caveman-proxy` runtime make **no network calls to Caveman servers**. They send
no telemetry. The client talks only to the runtime endpoint you configure; the
runtime answers the `/caveman/v1/middleware/*` routes and does not call a model
provider on this path. The Python import name `caveman_cloud` is historical: it
does not mean a cloud service is involved.

The CLI you may use to install and start the runtime (`caveman setup`,
`caveman start`) is a separate program with its own opt-out
[anonymous telemetry](#anonymous-cli-telemetry): content-free usage events,
never prompts or tool results. Turn it off with `caveman telemetry off` or
`DO_NOT_TRACK=1`. Running the binary or container image directly
(`caveman-proxy serve`) involves no CLI and no telemetry.

What the runtime stores, on its own disk, or in the Postgres database you point
it at with `CAVEMAN_MIDDLEWARE_DATABASE_URL` when several replicas share one
store:

- **Tool-result originals** (exact text), so the model can recover them. Treat
  them as sensitive as the tool output itself.
- **Scope state** in the runtime database (`~/.caveman/caveman.db`, or under
  `CAVEMAN_HOME` in the container): scope identifiers (namespace, session,
  branch, cache epoch), the compressed replacement text the runtime chose,
  recovery grants, and receipts. The replacement text is derived from tool
  output, so it is sensitive too. No provider credentials, no model responses.

Lifecycle **up to and including runtime `bin-v1.1.8`**: originals go into the
shared recovery store (`~/.caveman/ccr.db`) in plaintext and are not deleted
when a session is deleted or expires; retention (`retention_seconds`, default
24 hours) covers scope metadata only. Delete the recovery store yourself when
you need the originals gone.

Lifecycle **from the next runtime release after `bin-v1.1.8`** (being built on
the middleware hardening branch; check the release notes before relying on it):

- originals live in the middleware store and belong to their scope, not to the
  shared recovery store;
- `retention_seconds` covers originals too, and a `max_retention_seconds` cap
  bounds sliding renewal;
- deleting a session (`sessions/delete`) deletes the scope's metadata and every
  original it owns, and reports `originals_deleted: true` with counts;
- originals are encrypted at rest when an encryption key is configured;
  without a key they rely on file permissions, as today;
- an original from a call that produced no replacement is not kept.

The normative rules are in the protocol spec,
[`docs/technical/middleware-protocol.md`](./docs/technical/middleware-protocol.md)
(section 12, Lifecycle).

## Anonymous CLI telemetry

Telemetry is **on by default and opt-out**.
The default is never silent: the first interactive command persists the decision
(with a stable anonymous identifier) and prints a one-line disclosure naming the
scope and the off switch. Nothing sends before that disclosure run, and CI /
non-interactive runs never send and never persist the default. Login is not
required; anonymous events go to `https://api.caveman.so/telemetry/cli`.

```bash
caveman telemetry status
caveman telemetry on
caveman telemetry off
```

Controls, in precedence order:

- non-empty, non-zero `DO_NOT_TRACK` forces telemetry off;
- `CAVEMAN_TELEMETRY=1|true|on` enables it and other non-empty values disable it;
- CI and non-interactive runs are always off;
- otherwise the persisted choice in `~/.caveman-cloud/config.json` applies —
  a persisted opt-out (from any version, including the old opt-in prompt's "no")
  is honored forever;
- no persisted choice means on, persisted with a printed disclosure on the
  first interactive command.

`CAVEMAN_TELEMETRY_URL` overrides the destination, mainly for testing. Telemetry
requests time out after 1.5 seconds and failures do not fail the CLI command.

When the disclosed scope widens, the persisted decision carries the wording
version it was made under. A wider scope reprints the disclosure once on the next
interactive command and bumps the stored version; it never re-asks, never flips a
decision, and never touches a persisted opt-out. Version 4 added the token
totals below.

Anonymous events can contain:

- random anonymous ID; CLI version; OS; architecture; Node major version;
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
  including sessions, turns, tokens, estimated cuts, scan timing, and whether an
  account was already connected;
- Caveman MCP tool name, duration, and outcome.

Anonymous telemetry does **not** include prompt or completion bodies, raw argv,
file paths, tool arguments or results, provider credentials, or local database
rows/files. Source enforcement and runtime tests live in
[`packages/cli/src/index.ts`](./packages/cli/src/index.ts) and
[`packages/cli/tests/telemetry.runtime.mjs`](./packages/cli/tests/telemetry.runtime.mjs).

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
  in anonymous telemetry or authenticated scan sync.
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

`caveman start` defaults to `127.0.0.1:8787`. Without `CAVEMAN_AUTH_TOKEN`,
the proxy accepts every inbound request, because loopback single-operator
isolation is the security boundary, and startup rejects a non-loopback `--host`
or `CAVEMAN_LISTEN` value.

A non-loopback listener needs an inbound credential: `CAVEMAN_AUTH_TOKEN` (at
least 16 bytes, no spaces or control characters), or one of the middleware
identity sources below. The container image listens on `0.0.0.0:8787`, so it
refuses to start without one.

- **Provider routes** (inference) accept only `CAVEMAN_AUTH_TOKEN`, in
  `x-cave-api-key` or `Authorization: Bearer`: one shared secret, the operator's
  authority over the server's provider keys. Without it on a non-loopback
  listener they refuse every request.
- **Framework middleware routes** (from runtime `bin-v2.0.0`) resolve a
  principal per caller: the shared token (`single_operator`, every namespace),
  a token map of SHA-256 token hashes to principals with namespace globs and
  quotas (several tokens per principal, so rotation needs no outage; reloaded on
  change or `SIGHUP`), an OIDC/JWT bearer checked against the issuer's key set
  (RS256/ES256 only, fetched over https only, redirects included), or a TLS
  client certificate. Principal names carry their source and are compared byte
  for byte: `oidc:<issuer>#<claim>`, `mtls:uri:<SAN>`, `mtls:dns:<SAN>`, and
  `mtls:cn:<CN>` only when `CAVEMAN_TLS_CLIENT_CN_FALLBACK` is set. So no JWT or
  certificate can take over a token principal's sessions by spelling its name,
  and no token map entry for such a principal may carry a token. Each principal
  reaches only its own sessions, and its allowed namespaces are enforced
  server-side on every route (`403 forbidden_namespace`). Every middleware
  request writes one audit line with the principal and how it authenticated,
  never content. A token map that fails to reload keeps the previous one,
  revoked tokens included, and is counted in
  `caveman_identity_reload_failures_total`: alert on it.
- **TLS:** `CAVEMAN_TLS_CERT_FILE` / `CAVEMAN_TLS_KEY_FILE` serve TLS 1.2+
  directly, reloaded on change; `CAVEMAN_TLS_CLIENT_CA_FILE` adds mTLS, and a
  client certificate is re-checked against the current CA on every request, so
  replacing the CA also cuts live keep-alive connections. Without them the
  proxy speaks plain HTTP: terminate TLS in front of it.
- `/health/*` stay unauthenticated for load balancers; `/metrics` does too
  unless `CAVEMAN_METRICS_TOKEN` is set.

Still not provided: per-user identity or roles on the provider routes, and an
audit log for them beyond the counted and logged rejections. Keep any shared
listener on a private network. A firewall alone does not make it an
authenticated external gateway. Configuration and examples:
[`docs/technical/deploy.md`](./docs/technical/deploy.md#identity).

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
