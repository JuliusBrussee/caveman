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
optimizers: []
subscription_compress: false
toolschema_strip: false
providers: {}
compat: {}
```

### Main fields

| Field | Meaning |
|---|---|
| `label` | Human-readable installation label |
| `mode` | Proxy operating mode |
| `listen` | Local listen address |
| `optimizers` | Explicit optimizer configuration |
| `subscription_compress` | Allow eligible subscription traffic compression |
| `toolschema_strip` | Allow configured tool-schema annotation stripping |
| `breakpoint_plan` | Optional cache breakpoint plan |
| `providers` | Provider endpoint, billing tier and region overrides |
| `compat` | Named OpenAI-compatible provider mounts |

Accepted internal proxy modes are `record`, `recommend`, `shadow`, `canary`,
`active`, `compress`, and `pixel`. Unknown values resolve to `record`.
Operator-facing local workflows normally use `record`, `compress`, or `pixel`.

`CAVEMAN_MODE` can override proxy YAML mode for `caveman start`.

### Provider overrides

Provider entries can change public endpoint or regional information without
putting secrets in YAML.

```yaml
providers:
  bedrock:
    region: eu-west-1
  azure:
    base_url: https://example-resource.openai.azure.com

compat:
  local-model:
    base_url: http://127.0.0.1:11434/v1
    api_key_env: LOCAL_MODEL_API_KEY
```

Self-hosted private or loopback upstreams require an explicit
`CAVE_SSRF_ALLOWLIST` entry. See [Security and privacy](security-and-privacy.md).

## Provider credentials

The proxy preserves an inbound request credential. When an integration does not
send one, supported providers can use their standard environment variables.
Common examples include:

```text
ANTHROPIC_API_KEY
OPENAI_API_KEY
GEMINI_API_KEY
AZURE_OPENAI_API_KEY
```

Amazon Bedrock supports its native authentication paths, including AWS
credentials and supported bearer-token configuration. Prefer provider-native
credential discovery over copying secrets into shell history.

## Precedence summary

Feature configuration resolves from defaults, global file, allowed project
overlay, then environment override. Proxy mode resolves from default, YAML,
then `CAVEMAN_MODE`. Command flags can select an explicit session mode such as
`caveman wrap --off` or `--pixel`.

When resolution fails or a mode is unknown, request transformation fails safe:
the runtime uses record or original-byte behavior instead of guessing.
