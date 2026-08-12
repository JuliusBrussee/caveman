# Caveman Gateway — the public, byte-safe `caveman` standalone gateway.

A base-URL-swap reverse proxy: point an agent at `http://127.0.0.1:8787` and its
LLM traffic flows through Caveman with no code change. Single-operator, BYOK, zero
cloud dependencies. Record mode is always a pass-through; on any transform problem
the original bytes are forwarded unchanged. Standalone records truthful per-request
spend to `~/.caveman/caveman.db` and only ever labels savings `inferred` — never
`verified`.

```bash
go build ./proxy/...                 # build
ANTHROPIC_API_KEY=… caveman-proxy    # serve on 127.0.0.1:8787
caveman-proxy stats                  # print the local spend summary as JSON
```

Bedrock Runtime is first-party in standalone mode; no raw endpoint is required.
Use either the low-friction bearer key or a complete IAM pair:

```bash
AWS_REGION=us-east-1 AWS_BEARER_TOKEN_BEDROCK=… caveman-proxy
# or:
AWS_REGION=us-east-1 AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… caveman-proxy
```

`AWS_SESSION_TOKEN` is honored for temporary IAM credentials. Credential
precedence is an explicit inbound credential, then the Bedrock bearer token,
then a complete IAM pair. A partial pair fails closed. Region precedence is
`providers.bedrock.region` in `caveman.yaml`, `CAVE_BEDROCK_REGION`,
`AWS_REGION`, `AWS_DEFAULT_REGION`, then `us-east-1`.

Inbound Bedrock `x-api-key` and bearer credentials are stamped as Bedrock API
keys before auth-mode classification. A Claude Code user agent therefore cannot
relabel paid Bedrock traffic as subscription traffic.

Driven by the `caveman` CLI: `caveman start` launches this binary, `caveman wrap
<agent>` points the agent's provider-specific base URL at it. A Bedrock Claude
Code wrap preserves the local AWS BYOK environment. Against a managed gateway it
adds the Caveman project key through Claude Code's custom-header seam; the
gateway resolves the project's stored Bedrock credential when the child has no
AWS credential. When the environment does contain a bearer key or complete IAM
tuple, wrap forwards it ephemerally as `x-cave-upstream-key` (bearer first;
IAM encoded as
`AWS_ACCESS_KEY_ID:AWS_SECRET_ACCESS_KEY[:AWS_SESSION_TOKEN]`). Newlines and
incomplete IAM environments fail before launch.

Runtime is the default Bedrock lane. Mantle's Anthropic Messages-compatible
route is separate, uses `/bedrock/anthropic`, and remains disabled unless the
deployment explicitly enables `CAVE_BEDROCK_MANTLE_ENABLED`.

The byte-safe provider adapters under `providers/` are shared with the managed
gateway, which imports them from here.
