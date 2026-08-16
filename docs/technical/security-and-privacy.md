# Security and privacy

Caveman local runtime processes prompts, tool output, source code, provider
credentials, and browser state. Install it with same care as a coding agent or
local proxy.

## Trust model

Local proxy is designed for one trusted operating-system user. It binds to
loopback and has no multi-user authentication layer. Do not expose it on a LAN,
container bridge, public interface, or shared host.

Connected Caveman Cloud commands have separate account and organization
controls. Those controls do not turn local loopback proxy into a shared service.

## Data flow

Local mode keeps Caveman processing on device, but provider-bound content still
goes to provider selected by agent. "Local" describes Caveman layer, not entire
model request.

Potential local data stores include:

- proxy request metadata and recovery records in SQLite;
- durable facts in Cavemem;
- feature configuration;
- agent-native hook or plugin state;
- browser session state owned by Chrome.

Read [Context recovery](context-recovery.md) before treating a recovery handle
as secret storage.

## Credentials

- Keep API keys in environment or provider-native credential stores.
- Do not write secrets into YAML, project config, prompts, benchmark fixtures, or
  command history.
- Preserve inbound authorization without logging it.
- Use distinct provider keys for development where provider supports it.
- Rotate a key if terminal, trace, or issue output exposed it.

## SSRF protection

Proxy validates upstream addresses and redirects. Private, loopback,
link-local, and other unsafe address classes are blocked by default. A
self-hosted provider needs explicit `CAVE_SSRF_ALLOWLIST` configuration.

Allow only exact hosts needed. Broad private-network ranges can let prompt-driven
requests reach unrelated local services.

## Lossy transforms

Engine, TOON, pixel, output shrinker, and trajectory rewriter can change
model-visible context. Safety controls include:

- record-mode byte pass-through;
- parse validation;
- size comparison;
- explicit capability gates;
- exact-source recovery;
- original-byte fallback when recovery store fails;
- fail-closed handling for unknown modes or grader types.

Recovery reduces information-loss risk but does not prove model will request
missing detail. Use record mode for workflows where every input byte must remain
visible.

## Browser controls

Browser bridge can inspect pages, click elements or evaluate JavaScript; write
actions may submit forms or trigger purchases. Grant permissions per operation,
and review script expressions before evaluation.

MV3 response extension runs locally, sends no Caveman analytics, and modifies
outgoing message text visibly. Browser platform and selected chat service still
receive submitted message.

## Skills and plugins

Remote skills influence model behavior, while hooks and plugins execute under
host-agent permissions. Preview source, confirm repository identity, and inspect
file changes before installation.

Do not install a skill because its name resembles a trusted package. Use pinned
release or commit when reproducibility matters.

## Local file permissions

Hook state uses restrictive file modes and symlink-safe writes. Protect Caveman
databases, configuration, and backups with user-only permissions because they
can contain recovered prompts or remembered facts.

## Reporting a vulnerability

Do not publish exploitable details in a public issue before maintainers can
assess them. Use repository security policy or confidential contact listed on
hosting page, and include affected version, minimal reproduction, impact, and
suggested mitigation without real credentials or customer data.

## Deployment checklist

1. Confirm proxy listens on `127.0.0.1`.
2. Keep secrets out of configuration files.
3. Review enabled transforms and model allowlists.
4. Set precise SSRF allowlist only when required.
5. Restrict local database and hook-state permissions.
6. Test recovery before a long lossy session.
7. Run record mode for byte-sensitive workflows.
8. Review agent, browser, hook, and plugin permissions separately.
