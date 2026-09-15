# Security and privacy

Caveman local runtime processes prompts, tool output, source code, provider
credentials, and browser state. Install it with same care as a coding agent or
local proxy.

## Trust model

By default the proxy is a single-operator tool: it binds to loopback and accepts
every request on it without authentication. In that configuration do not expose
it on a LAN, container bridge, public interface, or shared host — a non-loopback
listen address is refused at startup.

A shared deployment is a separate, explicit configuration. It requires
`CAVEMAN_AUTH_TOKEN`, and then every request must present that token in
`x-cave-api-key` or `Authorization: Bearer`. The proxy consumes the header before
resolving a provider credential, so the shared token is never forwarded upstream
and is never mistaken for a provider key. Provider credentials live on the
server, in its environment or in an AWS role, not on the clients. The token is a
single shared secret with no per-user identity: rotate it when someone leaves.
Keep the listener inside a private network and terminate TLS in front of it —
the proxy speaks plain HTTP. Health and metrics endpoints stay unauthenticated
for load balancers, so do not expose them publicly. See
[Deploy the proxy for a team](deploy.md).

Connected Caveman Cloud commands have separate account and organization
controls. Those controls are not what gates a self-hosted shared proxy.

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

When an outbound proxy is in use (`upstream_proxy`, which by default honours
`HTTPS_PROXY`), that proxy connects to providers on Caveman's behalf, so the
boundary moves to it. Caveman still applies the range checks to IP-literal
destinations and rejects `localhost` before selecting it, but hostnames are
resolved by the proxy, so hostname-level policy is the proxy's own access
control. The same applies to the Bedrock and Vertex endpoint pre-flight: for a
proxied destination it checks host syntax, `localhost`, and IP-literal ranges
without resolving. The proxy
address is operator configuration and is dialed without an allowlist entry.
Destinations that `NO_PROXY` sends direct keep the full guard and still need a
`CAVE_SSRF_ALLOWLIST` entry when they are private or loopback.

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

1. Confirm the proxy listens on `127.0.0.1`, or that a non-loopback listener is
   deliberate, private, and behind TLS.
2. Set `CAVEMAN_AUTH_TOKEN` from a secret store for any shared listener, and
   rotate it on team changes.
3. Keep secrets out of configuration files.
4. Review enabled transforms and model allowlists.
5. Set precise SSRF allowlist only when required.
6. Restrict local database and hook-state permissions.
7. Test recovery before a long lossy session.
8. Run record mode for byte-sensitive workflows.
9. Review agent, browser, hook, and plugin permissions separately.
