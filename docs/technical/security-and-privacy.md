# Security and privacy

Caveman local runtime processes prompts, tool output, source code, provider
credentials, and browser state. Install it with same care as a coding agent or
local proxy.

## Trust model

By default the proxy is a single-operator tool: it binds to loopback and accepts
every request on it without authentication. In that configuration do not expose
it on a LAN, container bridge, public interface, or shared host. A non-loopback
listen address is refused at startup unless an inbound credential is configured:
`CAVEMAN_AUTH_TOKEN`, a middleware token map, an OIDC issuer, or a TLS client CA.
The container image listens on `0.0.0.0:8787`, so it will not start without one.

A shared deployment is a separate, explicit configuration. On the provider
(inference) routes it requires `CAVEMAN_AUTH_TOKEN`, and every request must
present that token in `x-cave-api-key` or `Authorization: Bearer`. The proxy
consumes the header before resolving a provider credential, so the shared token
is never forwarded upstream and is never mistaken for a provider key. Provider
credentials live on the server, in its environment or in an AWS role, not on
the clients. That token is a single shared secret (at least 16 bytes) with no
per-user identity or roles: rotate it when someone leaves.

The framework middleware routes (`/caveman/v1/middleware/*`) have their own
identity, from runtime `bin-v2.0.0`: the shared token still means one
`single_operator` principal, and a token map, an OIDC issuer or TLS client
certificates add per-team principals. A principal reaches only its own
sessions; its allowed namespaces are checked server-side on every route, and
each request is written to an audit log line with the principal, never the
content. Principal names carry their source (`oidc:<issuer>#<claim>`,
`mtls:uri:…`, `mtls:dns:…`), so no JWT or certificate can take over a token
principal's sessions by spelling its name; a certificate's subject CN names a
principal only when `CAVEMAN_TLS_CLIENT_CN_FALLBACK` is set. Originals can be
encrypted at rest, and several replicas can share one Postgres store.

The proxy can serve TLS itself (`CAVEMAN_TLS_CERT_FILE`, `CAVEMAN_TLS_KEY_FILE`,
optionally `CAVEMAN_TLS_CLIENT_CA_FILE` for mTLS, checked against the current CA
on every request so a CA rotation also cuts live connections); otherwise it
speaks plain HTTP and TLS belongs in front of it. Keep the listener inside a
private network either way. Health endpoints stay unauthenticated for load
balancers, and `/metrics` does too unless `CAVEMAN_METRICS_TOKEN` is set, so do
not expose them publicly. A token map that fails to reload keeps the previous
one, revoked tokens included; alert on
`caveman_identity_reload_failures_total`. See
[Deploy the proxy for a team](deploy.md#identity).

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

Framework middleware: the client, adapters, and runtime make no calls to
Caveman servers and send no telemetry. The runtime you host stores tool-result
originals and scope state; what it keeps, for how long, and how deletion and
encryption at rest work (current and next release) is in
[SECURITY.md](../../SECURITY.md#framework-middleware-data). The CLI that can
start the runtime has separate opt-out telemetry (`caveman telemetry off` or
`DO_NOT_TRACK=1`); the Python import name `caveman_cloud` is historical and does
not imply a cloud service.

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

Engine, TOON, pixel, and output shrinker can change
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
assess them. Use [GitHub private vulnerability
reporting](https://github.com/JuliusBrussee/caveman/security/advisories/new),
and include affected version, minimal reproduction, impact, and suggested
mitigation without real credentials or customer data. Supported versions and
response targets are in [SECURITY.md](../../SECURITY.md#supported-versions).

## Deployment checklist

1. Confirm the proxy listens on `127.0.0.1`, or that a non-loopback listener is
   deliberate, private, and served over TLS (its own listener or in front).
2. Set `CAVEMAN_AUTH_TOKEN` from a secret store for any shared listener, and
   rotate it on team changes. For framework middleware shared by several teams,
   give each team a token-map principal with only its namespaces.
3. Keep secrets out of configuration files.
4. Review enabled transforms and model allowlists.
5. Set precise SSRF allowlist only when required.
6. Restrict local database and hook-state permissions.
7. Test recovery before a long lossy session.
8. Run record mode for byte-sensitive workflows.
9. Review agent, browser, hook, and plugin permissions separately.
