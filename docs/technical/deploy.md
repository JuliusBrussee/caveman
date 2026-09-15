# Deploy the proxy for a team (VPC and cloud)

The same `caveman-proxy` binary that runs on a laptop can run as one shared
service inside a private network. Every developer points `CAVE_GATEWAY_URL` at
it instead of at `127.0.0.1:8787`. Provider keys live on the server, not on the
laptops.

## What changes versus the laptop proxy

| | Laptop (`caveman start`) | Shared service |
|---|---|---|
| Listen | `127.0.0.1:8787` | `0.0.0.0:8787` via `CAVEMAN_LISTEN` or `listen:` |
| Inbound auth | none | `CAVEMAN_AUTH_TOKEN` required |
| Provider credential | the developer's env or inbound header | the server's env, or an AWS role |
| State | `~/.caveman` | a volume mounted at `CAVEMAN_HOME` |

The server reads `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY` and
`AWS_BEARER_TOKEN_BEDROCK` from its own environment. For Bedrock it can instead
use the AWS default credential chain, so an ECS task role, an EKS pod role or an
EC2 instance profile needs no keys at all.

`CAVEMAN_AUTH_TOKEN` is the switch. Set it and the proxy accepts a non-loopback
listen address; leave it unset and a non-loopback address is refused at startup,
exactly as before. At least 16 characters, no whitespace or control characters,
environment variable only — an `auth_token:` key in `caveman.yaml` is refused
at startup rather than ignored.

Generate one, never type one — a memorable token is a guessable token:

```bash
openssl rand -hex 32
```

Every request must then carry it, in either header:

```text
x-cave-api-key: <token>
Authorization: Bearer <token>
```

The proxy consumes that header — deletes it — before it resolves the provider
credential, so the shared token can never be forwarded to a provider. Only
headers are scrubbed: never put the token in a URL query string. A request
that carries a real provider credential of its own (`x-api-key`, Google's key
header, or a bearer that is not the token) still wins, exactly as on a laptop.

`/health/live`, `/health/ready` and `/metrics` stay unauthenticated so a load
balancer can probe them. The startup log reports `inbound_auth: token`.

## One container

```bash
docker run -d --name caveman-proxy \
  -p 8787:8787 \
  -v caveman-data:/data \
  -e CAVEMAN_AUTH_TOKEN="$(openssl rand -hex 32)" \
  -e ANTHROPIC_API_KEY=<anthropic-key> \
  ghcr.io/juliusbrussee/caveman-proxy:bin-v1.1.7
```

The image sets `CAVEMAN_HOME=/data` and `CAVEMAN_LISTEN=0.0.0.0:8787`, runs as
non-root uid 65532, and exposes 8787. It is published multi-arch (amd64, arm64)
by the signed `bin-v*` release workflow; always pin a `bin-vX.Y.Z` tag in
production, never `:latest`. `bin-v1.1.7` is the first tag that publishes the
image — every example here names it. To build it yourself, run
`docker build -t caveman-proxy .` at the repository root.

A named volume inherits the right owner. A **bind** mount does not — `chown` the
host directory to `65532` or the proxy cannot create its SQLite store.

Check it, then send one authenticated request:

```bash
curl -s http://localhost:8787/health/ready
```

```bash
curl -s http://localhost:8787/v1/messages \
  -H 'x-cave-api-key: <token>' \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'
```

## Docker Compose

`deploy/docker-compose.yml` holds a ready file. It reads configuration from
`deploy/.env`:

```bash
cat > deploy/.env <<EOF
CAVEMAN_AUTH_TOKEN=$(openssl rand -hex 32)
ANTHROPIC_API_KEY=<anthropic-key>
EOF
docker compose -f deploy/docker-compose.yml up -d
```

## AWS ECS Fargate

`deploy/aws-ecs-task-definition.json` is a starting task definition. Replace
every `REPLACE_*` placeholder, then:

```bash
aws ecs register-task-definition \
  --cli-input-json file://deploy/aws-ecs-task-definition.json
```

- Give the **task role** `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream`
  and set no AWS keys. The proxy picks the role up from the container credential
  endpoint.
- Put `CAVEMAN_AUTH_TOKEN` in Secrets Manager or SSM and reference it from the
  task definition's `secrets` block, not `environment`. Generate it with
  `openssl rand -hex 32`; the committed task definition carries no token value,
  only a `REPLACE_`-marked ARN that fails `register-task-definition` until you
  replace it.
- Mount an EFS access point at `/data` with POSIX uid/gid `65532`.
- Point the ALB target group health check at `/health/ready` on port 8787.
- Run the service in private subnets. The ALB is the only thing with a listener
  the developers reach.
- If provider traffic leaves through a VPC endpoint with private DNS, the
  provider hostname resolves to a private address and the SSRF guard blocks it.
  Allow that exact hostname:
  `CAVE_SSRF_ALLOWLIST=bedrock-runtime.<region>.amazonaws.com`.

## Kubernetes

`deploy/kubernetes.yaml` holds a Secret, a PVC, a single-replica Deployment and
a Service.

The Secret carries no `CAVEMAN_AUTH_TOKEN` on purpose: a placeholder long enough
to look like a placeholder is also long enough to pass validation and serve as a
real token. Create it first, then apply the file — the apply adds the provider
key beside the token and leaves the token alone:

```bash
kubectl create secret generic caveman-proxy -n <namespace> \
  --from-literal=CAVEMAN_AUTH_TOKEN="$(openssl rand -hex 32)"
```

Replace the remaining `REPLACE_...` provider key in the Secret, then:

```bash
kubectl apply -n <namespace> -f deploy/kubernetes.yaml
```

The pod runs as uid 65532 with `fsGroup: 65532` so the PVC is writable, probes
`/health/live` and `/health/ready` on 8787, and keeps `replicas: 1` with
`strategy: Recreate`. See [State and scaling](#state-and-scaling).

For Bedrock on EKS, set no AWS keys and give the ServiceAccount the role: either
the IRSA annotation
`eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/<role-name>`, or an
EKS Pod Identity association.

## Google Cloud Run

```bash
openssl rand -hex 32 | gcloud secrets create caveman-token --data-file=-
gcloud run deploy caveman-proxy \
  --image ghcr.io/juliusbrussee/caveman-proxy:bin-v1.1.7 \
  --port 8787 --ingress internal --allow-unauthenticated --max-instances 1 \
  --set-secrets CAVEMAN_AUTH_TOKEN=caveman-token:latest,ANTHROPIC_API_KEY=anthropic-key:latest
```

`--allow-unauthenticated` turns off Cloud Run's own IAM check: agents authenticate
with `CAVEMAN_AUTH_TOKEN`, not with a Google identity token. `--ingress internal`
then limits who can reach it to your VPC. `--max-instances 1` keeps a single
SQLite writer. Cloud Run's filesystem is not durable: spend history and recovery
originals do not survive a revision unless you mount a volume.

## Fly.io

```bash
fly launch --image ghcr.io/juliusbrussee/caveman-proxy:bin-v1.1.7 \
  --internal-port 8787 --no-deploy
fly volumes create caveman_data --size 1
fly secrets set CAVEMAN_AUTH_TOKEN="$(openssl rand -hex 32)" ANTHROPIC_API_KEY=<anthropic-key>
fly deploy
```

Add the mount to `fly.toml` before deploying:

```toml
[[mounts]]
  source = "caveman_data"
  destination = "/data"
```

## Point developers at it

Two variables, then the normal commands:

```bash
export CAVE_GATEWAY_URL=http://caveman.internal:8787
export CAVE_API_KEY=<token>

caveman wrap claude      # or: caveman claude
```

Any off-loopback `CAVE_GATEWAY_URL` puts the CLI in managed mode: it injects
`CAVE_API_KEY` into the wrapped agent (for Claude Code as `ANTHROPIC_AUTH_TOKEN`,
i.e. `Authorization: Bearer <token>`) and starts no local proxy.

SDKs talk to it directly:

```python
from openai import OpenAI
client = OpenAI(
    base_url="http://caveman.internal:8787/openai/v1",
    api_key="<token>",              # travels as Authorization: Bearer
)
```

```ts
import Anthropic from "@anthropic-ai/sdk";
const client = new Anthropic({
  baseURL: "http://caveman.internal:8787",
  apiKey: "no-key-required",        // the server's ANTHROPIC_API_KEY is used
  defaultHeaders: { "x-cave-api-key": "<token>" },
});
```

`Authorization: Bearer <token>` alone is enough for any OpenAI-protocol client
that cannot add a custom header. Send your own provider key instead if you want
the request billed to your account rather than the server's.

## Private endpoints and egress

- `CAVE_SSRF_ALLOWLIST` takes **exact** hostnames or `host:port`, comma
  separated. A private or loopback upstream is blocked until it appears there;
  the error message names the entry to add. Link-local and cloud metadata
  addresses stay blocked in every mode, with no allowlist escape.
- `CAVE_UPSTREAM_PROXY` (or `upstream_proxy:` in `caveman.yaml`) sends provider
  traffic through an egress proxy. `env` is the default and honours
  `HTTPS_PROXY`/`NO_PROXY`.
- `CAVE_CA_BUNDLE` (or `ca_bundle:`) adds private roots when a TLS-inspecting
  proxy re-signs provider certificates.

See [Configuration](configuration.md) and [Security and privacy](security-and-privacy.md).

## State and scaling

State is SQLite under `CAVEMAN_HOME` — spend records, the recovery store, and
the prefix cache. **One writer per volume.** Run one replica per volume, or give
each team its own instance and volume. Do not scale horizontally behind a shared
volume. If you run several instances behind one address, route each client
consistently to one of them, so a session's recovery handles stay reachable.

`CAVEMAN_MODE=record` is the default and is always a byte-safe pass-through.
`compress` is the savings mode.

## TLS

The proxy speaks plain HTTP. Terminate TLS at the load balancer, ingress, or
service mesh in front of it, and keep the listener inside a private network.

## Health and metrics

| Path | Purpose |
|---|---|
| `GET /health/live` | Process is up |
| `GET /health/ready` | Runtime identity and adapter count |
| `GET /metrics` | Prometheus text; `cave_proxy_inflight_requests`, `cave_proxy_unauthorized_total` |
| `POST /caveman/keepalive` | No-op beacon from older CLIs; changes nothing |

All four are unauthenticated. Do not expose them publicly. On a LOOPBACK
listener `/health/live` also carries an `X-Caveman-Instance` header that the
local CLI uses to match a run-state file; a shared listener publishes no such
header, and it authenticates nothing inbound either way.

Every rejected request increments `cave_proxy_unauthorized_total` and writes one
`inbound token rejected` warning with the request path and the caller's host —
never the presented token. Alert on that counter: it is the only signal that
someone is guessing at `CAVEMAN_AUTH_TOKEN`.

## Limits in this version

- Subscription and OAuth logins (Claude Pro/Max, ChatGPT) do not work through a
  token-authenticated shared proxy: the wrap sends the shared token where the
  OAuth bearer would go. A shared proxy is the BYOK / API-key path, or the
  Bedrock role path.
- Managed Gemini CLI routing is unsupported — the CLI cannot send a separate
  Caveman credential and upstream credential, and `caveman wrap gemini` refuses.
- In `compress` mode, leave `CAVEMAN_RECOVERY` unset on the server: recovery is
  then served by the proxy's own retrieve loop, which runs only for API-key
  traffic, on non-streaming requests, on supported routes — everything else is
  forwarded unchanged. The MCP recovery tool a local `caveman wrap` installs
  reads a local store and cannot reach a remote one.

## Checklist

1. `CAVEMAN_AUTH_TOKEN` generated with `openssl rand -hex 32`, set from a
   secret store, 16+ characters, not in YAML.
2. Listener inside a private network, TLS terminated in front of it.
3. Provider keys on the server, or an AWS role with no keys at all.
4. `/data` on a durable volume owned by uid 65532.
5. One replica per volume.
6. Health check on `/health/ready`; `/metrics` not publicly reachable.
7. `CAVE_SSRF_ALLOWLIST` entries only for the private endpoints you actually use.
8. Image pinned to a `bin-v*` tag (`bin-v1.1.7` or later), not `:latest`.
