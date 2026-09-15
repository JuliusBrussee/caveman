# Configuration

Caveman has two configuration layers:

1. feature configuration used by `caveman wrap` and agent shortcuts;
2. proxy configuration used by `caveman start`.

Credentials belong in environment variables or provider-native credential
stores. Do not put API keys in either configuration file.

## Feature configuration

Global feature configuration lives at:

```text
~/.caveman-cloud/config.json
```

A project can add a restricted overlay at:

```text
./.caveman/config.json
```

Inspect the resolved path and values with:

```bash
caveman tools config path
caveman tools config get think.mode
```

### Keys and defaults

| Key | Default | Accepted values | Meaning |
|---|---|---|---|
| `think.mode` | `compress` | `compress`, `record`, `pixel` | Main request mode |
| `think.core` | `true` | Boolean | Enable core context compression |
| `think.toon` | `true` | Boolean | Allow TOON when it is smaller and supported |
| `think.shrink` | `true` | Boolean | Enable output shrinking where supported |
| `think.pixel.models` | `[]` | Model-name array | Models allowed to receive pixel context |
| `think.pixel.density` | `balanced` | `conservative`, `balanced`, `max` | Pixel packing density |
| `remember.mem` | `true` | Boolean | Enable local memory integration |
| `remember.offload` | `auto` | `auto`, `on`, `off` | Control automatic memory offload |
| `remember.recall` | `false` | Boolean | Enable automatic memory recall |
| `execute.mcp` | `auto` | `auto`, `marker-only`, `true`, `false` | Control MCP recovery server wiring |
| `execute.browse_tool` | `true` | Boolean | Expose browser tool integration |
| `execute.browse_cli` | `false` | Boolean | Enable browser command integration |
| `execute.delegate` | `false` | Boolean | Enable supported delegation integration |
| `execute.proxy` | `true` | Boolean | Route supported agents through local proxy |

Project overlays may set `think.toon`, `think.shrink`, `remember.*`, and
`execute.*`. They cannot change `think.mode`, `think.core`, or pixel settings.
This prevents a checked-in project file from silently enabling a more invasive
transformation mode.

### Environment overrides

Environment variables take precedence over stored feature configuration.

| Variable | Corresponding setting |
|---|---|
| `CAVEMAN_WRAP_MODE` | `think.mode` |
| `CAVEMAN_CORE` | `think.core` |
| `CAVEMAN_TOON` | `think.toon` |
| `CAVEMAN_SHRINK` | `think.shrink` |
| `CAVEMAN_MCP` | `execute.mcp` |
| `CAVE_PIXEL_MODELS` | `think.pixel.models` |
| `CAVE_PIXEL_DENSITY` | `think.pixel.density` |

Use environment overrides for temporary sessions. Use `caveman tools config
set` for durable operator choices.

## Proxy configuration

Default proxy configuration path:

```text
~/.caveman/caveman.yaml
```

Set `CAVEMAN_CONFIG` to load another file.

```yaml
label: local
mode: record
listen: 127.0.0.1:8787
optimizers: {}
subscription_compress: false
toolschema_strip: false
breakpoint_plan: frontier
providers: {}
compat: {}
upstream_proxy: env
ca_bundle: ""
```

### Main fields

| Field | Meaning |
|---|---|
| `label` | Human-readable installation label |
| `mode` | Proxy operating mode |
| `listen` | Listen address. Loopback by default; a non-loopback address requires `CAVEMAN_AUTH_TOKEN` |
| `optimizers` | Optimizer overrides; provider-cache optimizers default on in optimization modes and accept explicit `false` |
| `subscription_compress` | Allow eligible subscription traffic compression |
| `toolschema_strip` | Allow configured tool-schema annotation stripping |
| `breakpoint_plan` | Cache breakpoint plan; defaults to `frontier`, with `off` as explicit off-switch |
| `providers` | Provider endpoint, billing tier and region overrides |
| `compat` | Named OpenAI-compatible provider mounts |
| `upstream_proxy` | Outbound proxy for provider traffic: `env` (default, honours `HTTPS_PROXY`), `off`, or a proxy URL |
| `ca_bundle` | Extra PEM roots to trust for provider TLS, on top of the system store |

Accepted internal proxy modes are `record`, `recommend`, `shadow`, `canary`,
`active`, `compress`, and `pixel`. Unknown values resolve to `record`.
Operator-facing local workflows normally use `record`, `compress`, or `pixel`.

`CAVEMAN_MODE` can override proxy YAML mode for `caveman start`.

### Proxy environment

These are read by the proxy process itself, not by feature configuration.

| Variable | Meaning |
|---|---|
| `CAVEMAN_CONFIG` | Path to `caveman.yaml` |
| `CAVEMAN_HOME` | State directory; default `~/.caveman` |
| `CAVEMAN_LISTEN` | Listen address; overrides `listen` |
| `CAVEMAN_AUTH_TOKEN` | Inbound shared token; generate with `openssl rand -hex 32`. Required for any non-loopback listen address. Minimum 16 characters, no whitespace or control characters. Environment only — an `auth_token:` key in `caveman.yaml` is a startup error, not a token |
| `CAVEMAN_MODE` | Proxy mode; overrides `mode` |
| `CAVE_UPSTREAM_PROXY` | Outbound proxy; overrides `upstream_proxy` |
| `CAVE_CA_BUNDLE` | Extra PEM roots; overrides `ca_bundle` |
| `CAVE_SSRF_ALLOWLIST` | Exact private or loopback upstream hosts to permit |

With `CAVEMAN_AUTH_TOKEN` set, every request must present the token in
`x-cave-api-key` or `Authorization: Bearer`. The proxy consumes that header
before resolving the provider credential, so the token is never forwarded
upstream. `/health/live`, `/health/ready` and `/metrics` stay unauthenticated.
See [Deploy the proxy for a team](deploy.md).

### Provider overrides

Provider entries can change public endpoint or regional information without
putting secrets in YAML.

```yaml
providers:
  bedrock:
    region: eu-west-1
  azure_openai:
    base_url: https://example-resource.openai.azure.com

compat:
  local-model:
    base_url: http://127.0.0.1:11434/v1
    api_key_env: LOCAL_MODEL_API_KEY
  zai:
    base_url: https://api.z.ai/api/anthropic
    wire_dialect: anthropic
```

`wire_dialect` declares the wire grammar the mount's upstream actually speaks.
Empty (the default) keeps the shared OpenAI shape everywhere. Set `anthropic`
only when the upstream answers the Anthropic Messages protocol on the mount.
It changes two behaviors, both scoped to Anthropic Messages-path requests:

- Usage accounting parses with the Anthropic dialect. Such an endpoint reports
  `input_tokens` exclusive of cache reads/writes, which the OpenAI-shape
  contradiction check misreads as malformed usage on every cache-warm
  response, so token accounting (and with it cache status and compression
  eligibility) silently drops those rows.
- Compression zones follow the Anthropic Messages grammar, which keys the
  live/frozen boundary on the request's own `cache_control` breakpoints. The
  OpenAI grammar would mark only the latest messages live, leaving the
  uncached post-breakpoint tail frozen or rewriting a block the provider
  already cached.

Routing, headers, and pricing keep the mount's OpenAI-compatible identity in
every dialect, and OpenAI-protocol paths on the same mount keep the OpenAI
grammar.

A named compat mount can forward additional provider headers explicitly:

```yaml
compat:
  tenant-relay:
    base_url: https://relay.example/tenant/v1
    api_key_env: RELAY_API_KEY
    forward_headers: [X-API-Tenant, CF-AIG-Authorization]
```

Only that mount forwards the listed headers. Standard provider credentials,
request framing, cookies, and Caveman control headers cannot be configured this
way. Headers named by an inbound `Connection` field stay on that connection.
The proxy publishes header names, never values, with its endpoint identity.
OpenClaw and Pi keep a provider direct when required configured headers are
absent from that contract. SDK session-affinity headers (`session_id`,
`x-session-id`, `x-client-request-id`, `x-session-affinity`) are forwarded as
standard end-to-end fields.

Self-hosted private or loopback upstreams require an explicit
`CAVE_SSRF_ALLOWLIST` entry. See [Security and privacy](security-and-privacy.md).

### Corporate networks: proxies and TLS inspection

Provider traffic honours the standard `HTTPS_PROXY`, `HTTP_PROXY`, and
`NO_PROXY` variables by default, the same way curl, Python, and Node do. On a
host that only reaches the internet through a corporate proxy nothing extra is
needed. `upstream_proxy` changes that:

```yaml
upstream_proxy: env                              # default
upstream_proxy: off                              # always dial providers directly
upstream_proxy: http://proxy.corp.example:3128   # provider traffic only
upstream_proxy: http://user:pass@proxy.corp.example:3128
```

A URL pins one proxy (`http://`, `https://`, or `socks5://`) for provider
requests without exporting process-wide proxy variables that the wrapped agent's
shell commands would inherit. In both modes `localhost`, loopback addresses, and
`NO_PROXY` matches are dialed directly, so an allowlisted local model server
keeps working next to a corporate proxy. `CAVE_UPSTREAM_PROXY` overrides the
YAML value.
When a proxy is selected for a destination, Caveman does not resolve that
hostname itself — the proxy does, which is what makes this work on a network
with no outbound DNS at all. HTTPS providers tunnel through the proxy with
`CONNECT`, so a plain forward proxy never sees request bodies or credentials. A
proxy that performs TLS inspection (below) terminates TLS itself and does see
them. A proxy that accepts the connection and then answers nothing is bounded by
`CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS` (default 900,000 ms; `0` disables),
which covers the wait for response headers only and never truncates a running
stream. The proxy address itself needs no
`CAVE_SSRF_ALLOWLIST` entry; see
[Security and privacy](security-and-privacy.md#ssrf-protection) for what the
guard still checks when a proxy is in use.

TLS inspection (Zscaler, Netskope, and similar) presents provider certificates
signed by a company root. Trust it by pointing `ca_bundle` or `CAVE_CA_BUNDLE`
at the PEM file:

```yaml
ca_bundle: /etc/ssl/corp-root.pem
```

Bundles already exported for other toolchains are picked up too, so an
environment set up for curl, Python, or Claude Code works unchanged:

| Variable | Read by |
|---|---|
| `SSL_CERT_FILE` | OpenSSL, Go on Linux |
| `REQUESTS_CA_BUNDLE` | Python `requests` |
| `NODE_EXTRA_CA_CERTS` | Node.js, Claude Code |

Every bundle is additive on top of the system store, so public providers keep
verifying when a bundle holds only the private root. A `ca_bundle` that is missing, corrupt, or
truncated fails startup rather than being half-trusted. An inherited variable
that cannot be loaded for any of those reasons is skipped with a startup
warning instead.

The wrapped agent talks to the local proxy on loopback. If the agent itself
reads `HTTPS_PROXY` (Claude Code does), that hop would otherwise be handed to the
corporate proxy and time out. `caveman run` and `caveman native enable` add the
gateway host to the agent's `NO_PROXY` automatically, appending to whatever you
already set and keeping your spelling of the variable. Set it yourself only when
you launch the agent without caveman.

## Provider credentials

The proxy preserves an inbound request credential. When an integration does not
send one, supported providers can use their standard environment variables.
Common examples include:

```text
ANTHROPIC_API_KEY
OPENAI_API_KEY
GEMINI_API_KEY
AZURE_OPENAI_API_KEY
OPENCODE_API_KEY
```

Amazon Bedrock supports its native authentication paths, including AWS
credentials and supported bearer-token configuration. Prefer provider-native
credential discovery over copying secrets into shell history.

Bedrock resolves credentials in the AWS default chain order: environment keys
(`AWS_BEARER_TOKEN_BEDROCK`, then `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`
/ optional `AWS_SESSION_TOKEN`), then web identity
(`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`, which is how IRSA works), then
container credentials (ECS task role, EKS Pod Identity), then the EC2 IMDSv2
instance profile. On ECS, EKS, or EC2 you therefore grant the task, pod, or
instance role `bedrock:InvokeModel*` and set no AWS keys at all.

## Precedence summary

Feature configuration resolves from defaults, global file, allowed project
overlay, then environment override. Proxy mode resolves from default, YAML,
then `CAVEMAN_MODE`. Command flags can select an explicit session mode such as
`caveman wrap --off` or `--pixel`.

When resolution fails or a mode is unknown, request transformation fails safe:
the runtime uses record or original-byte behavior instead of guessing.
