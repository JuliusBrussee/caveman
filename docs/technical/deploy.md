# Deploy the proxy for a team (VPC and cloud)

The same `caveman-proxy` binary that runs on a laptop can run as one shared
service inside a private network. Every developer points `CAVE_GATEWAY_URL` at
it instead of at `127.0.0.1:8787`. Provider keys live on the server, not on the
laptops.

## What changes versus the laptop proxy

| | Laptop (`caveman start`) | Shared service |
|---|---|---|
| Listen | `127.0.0.1:8787` | `0.0.0.0:8787` via `CAVEMAN_LISTEN` or `listen:` |
| Inbound auth | none | `CAVEMAN_AUTH_TOKEN`, or a [middleware identity](#identity) |
| Provider credential | the developer's env or inbound header | the server's env, or an AWS role |
| State | `~/.caveman` | a volume mounted at `CAVEMAN_HOME` |

The server reads `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY` and
`AWS_BEARER_TOKEN_BEDROCK` from its own environment. For Bedrock it can instead
use the AWS default credential chain, so an ECS task role, an EKS pod role or an
EC2 instance profile needs no keys at all.

An inbound credential is the switch. With `CAVEMAN_AUTH_TOKEN`, or any
[middleware identity](#identity) source (a token map, OIDC, or a TLS client CA),
the proxy accepts a non-loopback listen address; with none of them, a
non-loopback address is refused at startup, exactly as before. The provider
routes accept only `CAVEMAN_AUTH_TOKEN`, so a service without it serves the
middleware and nothing else. The token is at least 16 characters, no whitespace
or control characters, environment variable only — an `auth_token:` key in
`caveman.yaml` is refused at startup rather than ignored.

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

`/health/live` and `/health/ready` stay unauthenticated so a load balancer can
probe them; `/metrics` does too unless `CAVEMAN_METRICS_TOKEN` is set. The
startup log reports `inbound_auth: token`.

## One container

```bash
docker run -d --name caveman-proxy \
  -p 8787:8787 \
  -v caveman-data:/data \
  -e CAVEMAN_AUTH_TOKEN="$(openssl rand -hex 32)" \
  -e ANTHROPIC_API_KEY=<anthropic-key> \
  ghcr.io/juliusbrussee/caveman-proxy:bin-v2.0.0
```

The image sets `CAVEMAN_HOME=/data` and `CAVEMAN_LISTEN=0.0.0.0:8787`, runs as
non-root uid 65532, and exposes 8787. It is published multi-arch (amd64, arm64)
by the signed `bin-v*` release workflow; in production pin it by digest,
`ghcr.io/juliusbrussee/caveman-proxy:bin-vX.Y.Z@sha256:<digest>` (find the
digest with `docker buildx imagetools inspect <image>`), never `:latest`.
`bin-v1.1.7` is the first tag that publishes the image; the middleware
identity, TLS listener and Postgres store below need `bin-v2.0.0` or later,
which every example here names. The Kubernetes and ECS manifests in `deploy/`
carry `@sha256:REPLACE_AT_RELEASE` until the `bin-v2.0.0` release writes the
real digest in; until then their image pull fails instead of running an
unpinned image. To build it yourself, run `docker build -t caveman-proxy .` at
the repository root.

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

The `postgres` profile adds a Postgres container and keeps the middleware store
there instead of in SQLite under `/data`. Add two lines to `deploy/.env` and
start with the profile:

```bash
PW=$(openssl rand -hex 24)
cat >> deploy/.env <<EOF
CAVEMAN_POSTGRES_PASSWORD=$PW
CAVEMAN_MIDDLEWARE_DATABASE_URL=postgres://caveman:$PW@postgres:5432/caveman?sslmode=disable
EOF
docker compose -f deploy/docker-compose.yml --profile postgres up -d
```

`sslmode=disable` is acceptable only because the database is reachable on the
compose network alone (its port is not published). Any other database needs
`sslmode=verify-full`.

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
- The middleware store lives in RDS for PostgreSQL, referenced from `secrets`:
  `CAVEMAN_MIDDLEWARE_DATABASE_URL` (with `sslmode=verify-full`),
  `CAVE_POSTGRES_CA_CERT` (the
  [RDS CA bundle](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.SSL.html)
  PEM, since the image trusts only public roots) and
  `CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY` (`openssl rand -base64 32`).
- `/data` is task storage, not EFS. It holds the proxy's own spend rows,
  recovery store and log, which the record-mode proxy routes do not need to
  keep. **Why not EFS:** the proxy's SQLite files on a shared EFS volume are
  written by two tasks at once during every rolling deployment, and SQLite's
  WAL does not work across hosts. With the middleware in RDS nothing is shared
  on disk, so the service can run several tasks and deploy with the default
  rolling settings.
- If you need durable spend history instead, keep one task on EFS and set the
  **service's** `deploymentConfiguration` to `maximumPercent: 100`,
  `minimumHealthyPercent: 0` (the old task stops before the new one starts),
  with a `desiredCount` of 1. Deployments then have a short outage.
- **TLS all the way to the task.** Middleware requests carry tool results, so
  they must not cross the load balancer in cleartext. The task definition runs
  the proxy's own TLS listener. A short-lived `tls-files` container (busybox,
  pinned by digest) writes the certificate and key from Secrets Manager into a
  task-local volume as mode 0400 files owned by uid 65532, and the proxy starts
  once it has succeeded. Store the PEM files as two secrets, then replace their
  `REPLACE_` ARNs:

  ```bash
  aws secretsmanager create-secret --name caveman-proxy/tls-cert --secret-string file://tls.crt
  aws secretsmanager create-secret --name caveman-proxy/tls-key --secret-string file://tls.key
  ```

  The ALB does not verify target certificates, so a private-CA or self-signed
  certificate works. Its target group must speak HTTPS to the task, health
  check included:

  ```bash
  aws elbv2 create-target-group --name caveman-proxy --target-type ip \
    --protocol HTTPS --port 8787 --vpc-id <vpc-id> \
    --health-check-protocol HTTPS --health-check-path /health/ready
  ```

  The ALB's own listener is HTTPS too, with an ACM certificate. The certificate
  is read at task start; to rotate it, update the secrets and force a new
  deployment.
- Point the ALB target group health check at `/health/ready` on port 8787.
- Run the service in private subnets. The ALB is the only thing with a listener
  the developers reach.
- If provider traffic leaves through a VPC endpoint with private DNS, the
  provider hostname resolves to a private address and the SSRF guard blocks it.
  Allow that exact hostname:
  `CAVE_SSRF_ALLOWLIST=bedrock-runtime.<region>.amazonaws.com`.

## Kubernetes

`deploy/kubernetes.yaml` holds a Secret, a PVC, a single-replica Deployment, a
Service and a NetworkPolicy. For several replicas, use
`deploy/kubernetes-ha.yaml` instead (see [High availability](#high-availability)).

The Secret carries no `CAVEMAN_AUTH_TOKEN` or `CAVEMAN_METRICS_TOKEN` on
purpose: a placeholder long enough to look like a placeholder is also long
enough to pass validation and serve as a real token. Create them first, then
apply the file — the apply adds the provider key beside the tokens and leaves
them alone:

```bash
kubectl create secret generic caveman-proxy -n <namespace> \
  --from-literal=CAVEMAN_AUTH_TOKEN="$(openssl rand -hex 32)" \
  --from-literal=CAVEMAN_METRICS_TOKEN="$(openssl rand -hex 32)"
```

The NetworkPolicy admits only pods labelled `caveman-client: "true"` (label
your Prometheus too) and lets the proxy reach DNS and HTTPS alone. EKS Pod
Identity also needs egress to `169.254.170.23/32` on port 80.

Replace the remaining `REPLACE_...` provider key in the Secret, then:

```bash
kubectl apply -n <namespace> -f deploy/kubernetes.yaml
```

The pod runs as uid 65532 with `fsGroup: 65532` so the PVC is writable, probes
`/health/live` and `/health/ready` on 8787, and keeps `replicas: 1` with
`strategy: Recreate`: SQLite on a ReadWriteOnce volume takes exactly one
writer, and a rolling update would start the new pod on the same database. The
PVC is 5 Gi (see [Sizing](#sizing)). The proxy routes run in `record` mode and
the middleware in `compress` mode (`CAVEMAN_MIDDLEWARE_MODE`). See
[State and scaling](#state-and-scaling).

For Bedrock on EKS, set no AWS keys and give the ServiceAccount the role: either
the IRSA annotation
`eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/<role-name>`, or an
EKS Pod Identity association.

## Google Cloud Run

```bash
openssl rand -hex 32 | gcloud secrets create caveman-token --data-file=-
gcloud run deploy caveman-proxy \
  --image ghcr.io/juliusbrussee/caveman-proxy:bin-v2.0.0 \
  --port 8787 --ingress internal --allow-unauthenticated --max-instances 1 \
  --set-secrets CAVEMAN_AUTH_TOKEN=caveman-token:latest,ANTHROPIC_API_KEY=anthropic-key:latest
```

`--allow-unauthenticated` turns off Cloud Run's own IAM check: agents authenticate
with `CAVEMAN_AUTH_TOKEN`, not with a Google identity token. `--ingress internal`
then limits who can reach it to your VPC. `--max-instances 1` keeps a single
SQLite writer. Cloud Run's filesystem is not durable: spend history and recovery
originals do not survive a revision unless you mount a volume. For the framework
middleware, either set `CAVEMAN_MIDDLEWARE_DATABASE_URL` to a Cloud SQL for
PostgreSQL database (which also lifts the one-instance limit for middleware
traffic) or set `CAVEMAN_MIDDLEWARE_EPHEMERAL=true` so it does not claim
durable recovery it cannot keep.

## Fly.io

```bash
fly launch --image ghcr.io/juliusbrussee/caveman-proxy:bin-v2.0.0 \
  --internal-port 8787 --no-deploy
fly volumes create caveman_data --size 5
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

The proxy's own state is SQLite under `CAVEMAN_HOME`: spend records, the
recovery store, and the prefix cache. **One writer per volume.** Run one
replica per volume, or give each team its own instance and volume. Do not scale
horizontally behind a shared volume. If you run several instances behind one
address, route each client consistently to one of them, so a session's
recovery handles stay reachable.

The framework middleware's store is separate and can move to Postgres, which
removes that limit for middleware traffic; see
[Framework middleware](#framework-middleware).

`CAVEMAN_MODE=record` is the default and is always a byte-safe pass-through.
`compress` is the savings mode.

## Framework middleware

The same binary serves the framework middleware API
(`/caveman/v1/middleware/*`) that the SDKs and middleware adapters call. It
compresses tool results inside your application's own requests and stores the
exact originals so the model can fetch them back. The protocol is specified in
[middleware-protocol.md](middleware-protocol.md).

`CAVEMAN_MIDDLEWARE_MODE` (or `middleware.mode`) sets its mode independently of
the proxy routes; unset, it follows `CAVEMAN_MODE`. The shipped manifests run
the proxy routes in `record` and the middleware in `compress`.

### Topologies

| Topology | Middleware store | Replicas | Affinity needed | Manifest |
|---|---|---|---|---|
| Local (`caveman start`) | SQLite in `~/.caveman` | 1 | n/a | none |
| Single shared service | SQLite on a volume | exactly 1, `Recreate` | none (one replica) | `deploy/kubernetes.yaml`, compose |
| Sidecar per application pod | Postgres, one schema per application | one per pod | none | [recipe below](#sidecar) |
| High availability | Postgres | 2 or more, rolling updates | none | `deploy/kubernetes-ha.yaml`, ECS template |

**Affinity contract.** With Postgres, any replica serves any request of any
session: a handle minted by one replica is retrieved, continued and deleted
through any other, so a plain round-robin Service or load balancer is correct.
With SQLite there is exactly one replica, so the question does not arise. Never
point two replicas at one SQLite file.

**Durability.** Capabilities report `persistent: true` when the store survives
a restart. SQLite on a volume and Postgres both do. If you run SQLite on
storage that does not (an `emptyDir`, a container without a volume, Cloud Run's
filesystem), set `CAVEMAN_MIDDLEWARE_EPHEMERAL=true`: the runtime then reports
`persistent: false` and compresses nothing, since an original lost in a restart
could not be recovered.

### High availability

`deploy/kubernetes-ha.yaml` runs two replicas behind a Service with a rolling
update (`maxUnavailable: 0`), a PodDisruptionBudget, a NetworkPolicy, a TLS
listener, token-map identity and an encryption key, under the `RuntimeDefault`
seccomp profile with secret files mode 0440 (readable through `fsGroup` only).
Its header lists the four Secrets to create first; the file carries no secrets,
so pods wait until the Secrets exist. Its one placeholder is the image digest.

`CAVEMAN_MIDDLEWARE_DATABASE_URL` selects Postgres. It is read only from the
environment, because it carries a password; a `middleware.database_url:` key in
`caveman.yaml` is refused at startup. The connection goes through the same
hardened pool as the rest of Caveman. Use
`postgres://<user>:<password>@<host>:5432/<db>?sslmode=verify-full`: the server
certificate is verified against `CAVE_POSTGRES_CA_CERT` or
`CAVE_POSTGRES_CA_CERT_FILE` when one is set (an empty one counts as unset),
else against the system roots, which is enough only for a database whose
certificate a public CA signs. With `CAVE_ENV=prod` (as
`deploy/kubernetes-ha.yaml` sets) only `sslmode=verify-full` is accepted.
Without it, a database on a non-loopback host must still name its mode: a
missing `sslmode` (or `prefer`/`allow`) is refused, and `disable`, `require` or
`verify-ca` is accepted only as your explicit opt-out, for a private network or
a TLS sidecar. Loopback hosts and unix sockets accept any mode. A replica that
cannot reach Postgres at startup exits (and restarts) instead of serving errors
behind a ready probe. A running replica that loses Postgres stays ready while
its provider routes can serve (the readiness body says
`"middleware":"degraded"` and the middleware routes answer errors); only a
replica without `CAVEMAN_AUTH_TOKEN`, which serves the middleware alone, leaves
the Service. See [Health and metrics](#health-and-metrics).

Tables are created in the connection's schema (the first entry of
`search_path`, which you can set in the URL: `...?search_path=caveman`).
Migrations run at startup under an advisory lock, so replicas starting together
migrate one after another, and they are idempotent. The database role needs
`CREATE` on that schema for the first start and ordinary read/write afterwards:
a replica that finds the schema at its current version runs no DDL. An upgrade
to a release with a newer middleware schema version migrates at its first
start, so for that start the role needs `CREATE` again. Either grant it for the
rollout and revoke it afterwards, or start one replica of the new release once
with a role that has `CREATE` (for example, the schema owner) before the
rollout. With a read/write-only role the new replicas fail at startup and
restart; the old ones keep serving under `maxUnavailable: 0`.
The upgrade to middleware schema version 3 pauses writes to the store while it
runs: it waits up to 2 s for in-flight writes, then recounts the admission
counters. If live writers hold it up, the replica rolls back and retries for up
to 60 s before failing startup.
Supported: PostgreSQL 14 or later (tested on 17).

What several replicas guarantee:

- **One writer per session.** Writes to one session (optimize, delete,
  receipts) are serialized across replicas by a per-session advisory lock, and
  each writer re-reads after taking it, exactly like SQLite's single writer.
  Concurrent first turns publish one replacement; a turn that races a delete
  never revives the deleted session. Different sessions write in parallel.
- **One sweeper.** Expiry sweeps run on one replica at a time and skip any
  session another transaction is writing or renewing; skipped rows wait for the
  next pass.
- **Admission limits are approximate across replicas.** Each replica checks
  committed totals, so replicas admitting at the same moment can overshoot
  `max_rows` / `max_bytes` / per-principal quotas by what they have in flight.
  `quota_requests_per_minute` is counted per replica: N replicas admit up to N
  times the limit.
- **Clocks.** Expiry uses each replica's clock. Keep replicas on NTP; skew moves
  expiry by the skew.
- A lock conflict the database resolves by aborting one transaction (deadlock,
  serialization failure) answers `409 identity_conflict`, which clients retry.

Connection pool: each replica runs at most `queue_depth + retrieve_queue_depth`
(32 by default) middleware transactions at once, plus sweeps. The pool defaults
to the larger of 4 and the number of CPUs; set `pool_max_conns` in the URL
(for example `&pool_max_conns=16`) and keep replicas × `pool_max_conns` under
the server's `max_connections`. Behind PgBouncer in transaction mode, add
`&default_query_exec_mode=exec` (the advisory locks are transaction-scoped and
work there).

### Sidecar

A runtime per application pod keeps middleware calls on loopback. All pods of
one application share one Postgres schema; give each application its own schema
and database role, so applications cannot read each other's data even though
each sidecar is an open loopback listener:

```sql
CREATE ROLE caveman_billing LOGIN PASSWORD '<password>';
CREATE SCHEMA billing AUTHORIZATION caveman_billing;
```

Add to the application's pod spec (Kubernetes 1.29+ native sidecar; on older
clusters make it a regular container):

```yaml
initContainers:
  - name: caveman-middleware
    image: ghcr.io/juliusbrussee/caveman-proxy:bin-v2.0.0
    restartPolicy: Always
    env:
      # Loopback only: the application reaches it, nothing else does.
      - name: CAVEMAN_LISTEN
        value: 127.0.0.1:8787
      - name: CAVEMAN_MODE
        value: record
      - name: CAVEMAN_MIDDLEWARE_MODE
        value: compress
      - name: CAVEMAN_MIDDLEWARE_DATABASE_URL # ...?sslmode=verify-full&search_path=billing
        valueFrom:
          secretKeyRef: {name: billing-caveman, key: database-url}
      - name: CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY
        valueFrom:
          secretKeyRef: {name: billing-caveman, key: encryption-key}
    volumeMounts:
      - name: caveman-data
        mountPath: /data
    securityContext:
      runAsNonRoot: true
      runAsUser: 65532
      readOnlyRootFilesystem: true
      allowPrivilegeEscalation: false
      capabilities:
        drop: ["ALL"]
    resources:
      requests: {cpu: 100m, memory: 128Mi}
      limits: {cpu: "1", memory: 512Mi}
volumes:
  - name: caveman-data
    emptyDir: {}
```

The application points the SDK at `http://127.0.0.1:8787`. The kubelet cannot
probe a loopback listener, so the sidecar has no probes; until it answers, the
SDK passes requests through uncompressed.

### Identity

Every middleware route authenticates, `capabilities` included, and the server
decides who the caller is from the credential alone. The caller's **principal**
owns every session it creates: another principal gets `404 not_found` for its
handles, even with the same namespace and session names. Its allowed
**namespaces** are checked on every route, `sessions/delete` and `retrieve`
included, and a scope in any other namespace gets `403 forbidden_namespace`
before anything is read.

Four sources, checked in this order:

1. **Shared token** (`CAVEMAN_AUTH_TOKEN`): principal `single_operator`, every
   namespace. Existing deployments behave exactly as before.
2. **Token map** (`CAVEMAN_MIDDLEWARE_TOKEN_MAP_FILE` or
   `middleware.token_map_file`): SHA-256 hashes of tokens mapped to principals.
3. **OIDC / JWT bearer** (`middleware.oidc`).
4. **Client certificate** (mTLS), when the [TLS listener](#tls) has
   `CAVEMAN_TLS_CLIENT_CA_FILE`.

A bearer (`Authorization: Bearer`, or `x-cave-api-key` for static tokens)
decides when present; otherwise a verified client certificate does; otherwise
the request gets `401`. With none of the four configured, a loopback listener
accepts every caller as `single_operator`, as it always has. Any of the four
makes a non-loopback listener legal.

**Principal names carry their source.** Names are compared byte for byte, with
no case folding, and no source can produce another source's names. So a JWT
subject or a certificate that spells a token principal's name is still a
different principal, with its own sessions:

| Source | Principal name | Example |
|---|---|---|
| Shared token | `single_operator` | |
| Token map | the entry's `name` | `team-a` |
| OIDC | `oidc:<issuer>#<claim>`, with `issuer` exactly as configured | `oidc:https://login.example.com/#svc-a` |
| Certificate, URI SAN | `mtls:uri:<first URI SAN, as issued>` | `mtls:uri:spiffe://example.org/ns/ci/sa/agent` |
| Certificate, DNS SAN | `mtls:dns:<first DNS SAN>` (when there is no URI SAN) | `mtls:dns:agent.internal` |
| Certificate, CN | `mtls:cn:<subject CN>`, only with `CAVEMAN_TLS_CLIENT_CN_FALLBACK=true` | `mtls:cn:build-agent` |

**Token map.** YAML (JSON also parses). Only hashes are stored; unknown keys
are an error.

```yaml
principals:
  - name: team-a
    # Globs: * matches any run of characters, / included. Absent: no namespace.
    namespaces: ["team-a", "team-a/*"]
    # sha256 of each token, hex. Two entries while rotating.
    token_sha256:
      - 3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea
    # Optional overrides of the runtime-wide values; 0 or absent keeps them.
    quota:
      rows: 200000
      bytes: 134217728
      requests_per_minute: 1200
  # An OIDC or certificate principal, named with its source prefix:
  # namespaces and quota only, never token_sha256.
  - name: "mtls:uri:spiffe://example.org/ns/ci/sa/agent"
    namespaces: ["ci/*"]
  - name: "oidc:https://login.example.com/#svc-a"
    namespaces: ["svc-a/*"]
```

An entry named `oidc:…` or `mtls:…` that lists a `token_sha256`, or whose
prefix is malformed (`mtls:` must be followed by `uri:`, `dns:` or `cn:`;
`oidc:` needs `<issuer>#<claim>`), fails the load. An unprefixed entry with no
`token_sha256` matches no caller and logs a warning at load.

Create a token and its hash:

```bash
TOKEN=$(openssl rand -hex 32)
printf %s "$TOKEN" | sha256sum
```

**Rotation without an outage:** add the new hash beside the old one, hand out
the new token, then remove the old hash. The file is re-read when it changes
(checked every 10 s) and at once on `SIGHUP`. On Kubernetes the proxy sees a
Secret edit only once the kubelet has refreshed the mounted volume, which
follows its sync period and cache TTL: usually 1-2 minutes, and `SIGHUP` cannot
speed that up. To revoke a token at once, restart the pods after editing the
Secret (`kubectl rollout restart deployment/caveman-proxy-ha`); new pods mount
the current version. A file that no longer parses is logged and ignored: the previous
map stays in force, **including tokens you meant to revoke**. It is retried
every 10 s and counted in `caveman_identity_reload_failures_total`; alert on
that counter (see [Health and metrics](#health-and-metrics)).

**OIDC.** In `caveman.yaml`, or as `CAVEMAN_MIDDLEWARE_OIDC_<KEY>` variables
(`ALGORITHMS` comma separated):

```yaml
middleware:
  oidc:
    issuer: https://login.example.com/
    audience: caveman-middleware
    jwks_url: https://login.example.com/.well-known/jwks.json  # https only
    algorithms: [RS256, ES256]       # default both; nothing else is accepted
    clock_skew_seconds: 60           # default 60
    principal_claim: sub             # default sub
    namespaces_claim: caveman_namespaces  # optional: array, or space-separated string
```

Tokens must carry a `kid`, an `exp`, the exact `iss`, and the audience in
`aud`. `alg: none`, HMAC algorithms, unknown `crit` headers and a key whose type
does not match the header's algorithm are refused. RSA keys under 2048 bits are
ignored. The key set is cached for an hour; a token with an unknown `kid` makes
it refetch once, at most every 10 seconds, and a failed fetch keeps the old
keys. The refetch never blocks tokens whose `kid` is already known, and
concurrent unknown `kid`s share one fetch. A `jwks_url` redirect to anything
but https is refused. The issuer may not contain `#`. Without
`namespaces_claim` (or when a token lacks the claim), namespaces and quota come
from the token map entry named `oidc:<issuer>#<claim>`.

**mTLS.** With `CAVEMAN_TLS_CLIENT_CA_FILE`, a client certificate that verifies
against that CA names a principal from its first URI SAN, else its first DNS
SAN (see the table above). The subject CN counts only with
`CAVEMAN_TLS_CLIENT_CN_FALLBACK=true` (`tls.client_cn_fallback`); it is off by
default because a CN is free text that many CAs fill carelessly. Namespaces and
quota come from the token map entry of that name; with no entry the principal
may use no namespace. Clients without a certificate can still connect and use a
bearer.

A JWT subject or certificate name that is literally `single_operator` is
refused. The token map may define `single_operator`, to move the shared token's
sessions behind rotatable tokens.

**Provider routes are unchanged.** The token map, OIDC and mTLS apply to the
middleware routes only. Provider routes (`/v1/messages`, `/openai/...`) accept
only `CAVEMAN_AUTH_TOKEN`, because a caller there spends the server's provider
keys, which is the operator's shared authority. On a non-loopback listener
without `CAVEMAN_AUTH_TOKEN`, provider routes refuse every request, so a replica
authenticated only by a token map serves the middleware and nothing else.

The capabilities document reports `trust_mode: resolver` when a token map,
OIDC or mTLS is configured, and `single_operator` otherwise.

**Audit.** Every middleware request writes one JSON log line with the route,
status, error code, principal, how it authenticated (`auth`: `open`, `token`,
`token_map`, `oidc`, `mtls`), truncated hashes of the scope and handle, sizes
and latency. Never content, credentials or raw scope values.

### Encryption at rest

`CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY` (comma separated) or
`CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY_FILE` (one per line) holds base64 32-byte
keys; generate one with `openssl rand -base64 32`. Originals are sealed with
AES-256-GCM under the first key, bound to their session, and any listed key
opens. Without a key, originals rely on file or database permissions. A key
that does not parse stops startup rather than storing plaintext. Keys are read
at startup.

Rotation with several replicas takes three rollouts, so no replica ever meets a
key it does not hold:

1. Add the new key **second** everywhere. Every replica can now open it; all
   still seal with the old one.
2. Move the new key **first**. New originals are sealed with it.
3. After `max_retention_seconds` (7 days by default) no original sealed with
   the old key remains; remove it.

Back the keys up apart from the database. A restored database without its keys
answers `recovery_unavailable` for every sealed original.

Adding a key to a store that already holds plaintext originals: with a key
configured, those originals are refused (`recovery_unavailable`, counted in
`caveman_middleware_plaintext_originals_total{outcome="refused"}`). Set
`middleware.allow_plaintext_originals: true`
(`CAVEMAN_MIDDLEWARE_ALLOW_PLAINTEXT_ORIGINALS=true`) to keep them readable,
then remove it once `max_retention_seconds` has passed since the key was added.

### Retention

| Key (`middleware.*`, or `CAVEMAN_MIDDLEWARE_<KEY>`) | Default | Meaning |
|---|---|---|
| `retention_seconds` | 86400 | A session lives this long after its last use |
| `max_retention_seconds` | 604800 | Hard cap from creation, whatever the use |

Expired or deleted sessions lose their originals, replacement text, plans and
receipts at the next sweep (every minute). A metadata-only tombstone stays 7
more days, so a new turn on the session gets `410 expired` or `410 deleted`
instead of silently starting over, and is then removed. `sessions/delete`
removes the session's originals at once and reports `originals_deleted: true`;
its handles answer `410 deleted` for those 7 days.

### Sizing

| Setting | Default | Covers |
|---|---|---|
| `max_bytes` | 576 MiB | Payload the middleware store admits: originals, replacements, manifests, plans, receipts |
| `max_rows` | 1,000,000 | Rows the middleware store admits |
| `quota_bytes` / `quota_rows` | a quarter of `max_bytes` / `max_rows` with a token map, OIDC or mTLS; none with one shared token | The same per principal; the token map can override per principal |
| `principal_in_flight` | half of each queue with a token map, OIDC or mTLS; unbounded with one shared token | Slots one principal may hold in each request queue (`queue_depth`, `retrieve_queue_depth`); its other requests wait for its own slots |

When a limit is reached, optimize answers with a `capacity` decision (the
request passes through uncompressed) while retrieval of stored originals keeps
working. The store reports its usage in `/metrics`.

For **SQLite on a volume**, size for both capped stores plus what is not
capped: 576 MiB of middleware payload, the proxy's recovery store (512 MiB,
`CAVEMAN_CCR_MAX_BYTES`), SQLite indexes, free pages and the write-ahead log,
the spend database (one metadata row per proxied request, never capped) and
32 MiB of rotated `proxy.log`. `deploy/kubernetes.yaml` asks for 5 Gi; raise it
with the caps, and alert when the volume passes 80 %.

For **Postgres**, the middleware tables hold up to `max_bytes` of payload plus
indexes and bloat between autovacuum runs; provision at least twice
`max_bytes`. Each replica's `/data` then holds only the proxy's own state
(`sizeLimit: 2Gi` in the HA manifest).

### Backup and restore

- **SQLite:** scale to zero (or stop the container), copy `caveman.db` (and
  `caveman.db-wal` if present) from the volume, or take a volume snapshot, then
  scale back up. The image has no shell or `sqlite3`, so copy from a helper pod
  that mounts the volume.
- **Postgres:** use your database's backups (`pg_dump` of the schema, or managed
  snapshots). Restoring rolls sessions back to the backup: sessions created
  since are gone and their handles answer `404`.
- Either way, back up the encryption keys separately (see above).

### TLS

Set `CAVEMAN_TLS_CERT_FILE` and `CAVEMAN_TLS_KEY_FILE` (or `tls.cert_file` /
`tls.key_file` in `caveman.yaml`) and the listener speaks TLS 1.2 or later,
with only forward-secret AEAD cipher suites in TLS 1.2. The files are re-read
when they change (checked every 10 s) and on `SIGHUP`, so a renewed certificate
applies without a restart; files that no longer load are logged and the
current certificate stays. `CAVEMAN_TLS_CLIENT_CA_FILE` adds client certificate
verification (see [Identity](#identity)). A client certificate is checked
against the client CA in force on every request, not only at the handshake, so
replacing the CA file cuts off keep-alive connections opened under the old CA
at their next request (`401`).

Without these, the proxy speaks plain HTTP: terminate TLS at the load balancer,
ingress, or service mesh in front of it, and keep the listener inside a private
network. Probes can use `scheme: HTTPS` against the TLS listener; the kubelet
does not verify the certificate.

## Health and metrics

| Path | Purpose |
|---|---|
| `GET /health/live` | Process is up |
| `GET /health/ready` | Ready to serve. The body's `middleware` is `ok`, `unavailable` (no runtime) or `degraded` (the store cannot take a write — SQLite: a write transaction; Postgres: the primary is reachable and writable). A degraded middleware answers `503` only on a listener that serves nothing else (non-loopback, no `CAVEMAN_AUTH_TOKEN`, so the provider routes refuse every request); otherwise readiness stays `200`, so a database outage does not take provider inference down with it |
| `GET /metrics` | Prometheus text; gated by `CAVEMAN_METRICS_TOKEN` when set |
| `POST /caveman/keepalive` | No-op beacon from older CLIs; changes nothing |

The health paths are unauthenticated. `/metrics` is too, unless
`CAVEMAN_METRICS_TOKEN` is set: then it needs `Authorization: Bearer <token>`.
Do not expose any of them publicly. On a LOOPBACK listener `/health/live` also
carries an `X-Caveman-Instance` header that the local CLI uses to match a
run-state file; a shared listener publishes no such header, and it
authenticates nothing inbound either way.

Metrics: `cave_proxy_inflight_requests`, `cave_proxy_unauthorized_total`, and
for the middleware `caveman_middleware_requests_total` (route, status, code),
`caveman_middleware_decisions_total`, `caveman_middleware_request_duration_seconds`,
`caveman_middleware_unauthorized_total`, `caveman_middleware_queue_depth` /
`_capacity`, and `caveman_middleware_store_rows` / `_bytes` / `_limit`. With a
token map or a TLS listener, `caveman_identity_reload_failures_total{source}`
counts reloads that failed and `caveman_identity_reload_last_success_timestamp_seconds{source}`
is when the configuration in force was loaded (process start, or the last good
reload); `source` is `token_map` or `tls`.

A failed reload keeps the previous token map, so a revocation that does not
parse leaves the revoked token working. A broken file is retried every 10 s, so
the counter keeps rising until the file is fixed. Page on it:

```yaml
- alert: CavemanIdentityReloadFailing
  expr: increase(caveman_identity_reload_failures_total[5m]) > 0
- alert: CavemanTokenRotationNotApplied   # after a rotation at time T
  expr: caveman_identity_reload_last_success_timestamp_seconds{source="token_map"} < <T>
```

Every rejected request increments `cave_proxy_unauthorized_total` and writes one
`inbound token rejected` warning with the request path and the caller's host —
never the presented token. Alert on that counter: it is the only signal that
someone is guessing at a credential. Alert on `code="forbidden_namespace"` in
`caveman_middleware_requests_total` too: a principal is asking for another
team's sessions.

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

## Migration notes

**Principal names now carry their source** (runtime `bin-v2.0.0`). Before, a
JWT subject or a certificate name shared one name space with token map
principals, so a certificate with `CN=team-a` or a JWT with `sub=team-a` could
reach the sessions of the token map principal `team-a`. Now OIDC and
certificate principals are named `oidc:<issuer>#<claim>` and
`mtls:{uri,dns,cn}:<name>` (see [Identity](#identity)). If you run a pre-release
build with OIDC or mTLS, change the token map before you upgrade:

1. Rename every entry that configures an OIDC or certificate principal:

   | Before | After |
   |---|---|
   | `name: spiffe://example.org/ns/ci/sa/agent` (URI SAN) | `name: "mtls:uri:spiffe://example.org/ns/ci/sa/agent"` |
   | `name: agent.internal` (DNS SAN) | `name: "mtls:dns:agent.internal"` |
   | `name: build-agent` (subject CN) | `name: "mtls:cn:build-agent"`, and set `CAVEMAN_TLS_CLIENT_CN_FALLBACK=true` |
   | `name: svc-a` (JWT `sub`) | `name: "oidc:<middleware.oidc.issuer>#svc-a"`, e.g. `"oidc:https://login.example.com/#svc-a"` |

   Quote the names, and use the issuer byte for byte as configured, trailing
   slash included.
2. Remove any `token_sha256` from those renamed entries; the load now fails if
   one is present. A token holder that needs the same namespaces gets its own
   unprefixed entry.
3. A certificate whose URI SAN differs from the entry only in case (for example
   `SPIFFE://`) no longer matches it; names are compared byte for byte.
4. Sessions that OIDC or certificate principals created before the upgrade
   belong to the old names. They answer `404` and expire under the normal
   retention; they are not migrated.

An entry you forget to rename fails closed: its caller gets
`403 forbidden_namespace`, and the load logs `token map principal has no
token_sha256 and matches no caller` with the entry's name.

**Manifests.** `deploy/kubernetes.yaml` now has a NetworkPolicy: label client
pods (and Prometheus) `caveman-client: "true"`, and add `CAVEMAN_METRICS_TOKEN`
to the `caveman-proxy` Secret. The ECS task definition now serves TLS to the
task: create the `caveman-proxy/tls-cert` and `caveman-proxy/tls-key` secrets
and switch the ALB target group to HTTPS (see [AWS ECS Fargate](#aws-ecs-fargate)).

## Checklist

1. `CAVEMAN_AUTH_TOKEN` generated with `openssl rand -hex 32`, set from a
   secret store, 16+ characters, not in YAML; or a token map with one principal
   per team.
2. Listener inside a private network, with the TLS listener or TLS terminated
   in front of it.
3. Provider keys on the server, or an AWS role with no keys at all.
4. `/data` on a durable volume owned by uid 65532, or the middleware store in
   Postgres.
5. One replica per SQLite volume; any number with Postgres.
6. Health check on `/health/ready`; `/metrics` gated by `CAVEMAN_METRICS_TOKEN`
   or not publicly reachable.
7. `CAVE_SSRF_ALLOWLIST` entries only for the private endpoints you actually use.
8. An encryption key for middleware originals, backed up apart from the data.
9. Image pinned by digest to a `bin-v*` release (`bin-v2.0.0` or later for the
   middleware identity, TLS and Postgres store), not `:latest`.
10. An alert on `caveman_identity_reload_failures_total`.
