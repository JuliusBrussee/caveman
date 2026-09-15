# @caveman-ai/agent — tool sandbox threat model

What the tool sandbox does and does **not** contain, per platform, and where the
real boundaries are. Read this before changing anything under
`executeSandboxedTool`, `networkIsolatedNode`, `installNetworkDeny`, or the
`sandboxProfile` type. The honesty rule that governs this file: **name the real
boundary, and name every gap** — a sandbox that claims more than it enforces is
a correctness bug, not a doc nit.

## Modes

- `fixture` — trusted test tools; effects (`effect: "write"`/`"external"`) are
  blocked, not executed. Not a security boundary against hostile code; a
  convenience for trusted evals.
- `required` — untrusted tool code runs in a **separate OS-isolated subprocess**
  (`executeSandboxedTool`). This is the mode the boundaries below describe.
- `host` — explicit opt-in for interactive/coding agents; closures run
  **in-process with no isolation at all** and `effect: "write"` executes. Host
  mode is uncontained by design (documented as such), is refused under a
  `required` ancestor (`cave_host_sandbox_nested_under_required`), and makes a
  build lock-ineligible (`cave_host_sandbox_lock_ineligible`).

## The real boundary (`required` mode)

`executeSandboxedTool` spawns the tool worker under **two** independent kernel
mechanisms:

1. **OS network isolation** (`networkIsolatedNode`):
   - **Linux**: `unshare --user --map-root-user --net` — a fresh network
     namespace with no interfaces, so no IP egress (TCP/UDP/DNS) is possible.
   - **macOS**: `sandbox-exec -p '(version 1)(allow default)(deny network*)'` —
     kernel denial of network operations.
   - **Other platforms**: `cave_sandbox_os_network_isolation_unavailable` — the
     tool cannot run at all (fails closed).
2. **Node permission model** (`--permission` + `--allow-fs-read`/`--allow-fs-write`):
   filesystem reads restricted to the staged source graph, the framework
   package root, the dependency closure, and an ephemeral workspace; writes
   restricted to the workspace. `child_process` is denied entirely
   (`cave_sandbox_child_process_containment_unavailable`).

`sandbox_passed` / `verifySandboxConformance` spawns a probe under mechanism (1)
and asserts home-read denial, child-process denial, and network/DNS/UDP denial.
The probe runs under the real boundary, so a pass reflects the kernel boundary,
not the in-process layer.

## `installNetworkDeny` is DEFENSE-IN-DEPTH, not a boundary

`installNetworkDeny` monkeypatches `globalThis.fetch`, `WebSocket`, and the
module-level `connect`/`request`/`lookup` exports of `node:http|https|net|...`.
It is bypassable by design of the JS runtime and **must never be relied on as a
boundary**:

- `new net.Socket().connect(...)` never touches the patched module export.
- A fresh `import`/`createRequire` of a core module returns unpatched bindings.
- `node:dns/promises` is a different module object than the patched `node:dns`.
- `new http.ClientRequest(...)` bypasses the patched `http.request`.

It stays only as a redundant second layer inside the worker, which already runs
under the kernel boundary. The attack tests deliberately include a `tcp_connect`
vector (`net.Socket`) whose block therefore proves the OS boundary, not the
patch.

## Known gaps (tracked, not silently accepted)

- **Linux unix-domain sockets** — a network namespace does **not** cover
  `AF_UNIX`. A tool that can see a host unix socket path (e.g.
  `/var/run/docker.sock`) can still `connect` to it, and the Node permission
  model does not gate `AF_UNIX` connect. Closing this needs a **mount namespace**
  that hides such paths (bwrap / `unshare --mount` direction). Until then,
  `required` mode does not contain unix-socket egress on Linux. **Tracked
  follow-up: bwrap migration.**
- **Scoped network egress** — `sandboxProfile.network: true` used to spawn with
  **no** OS boundary (unrestricted egress with credentials in env). It now
  **fails closed** (`cave_sandbox_network_egress_unbounded`). Real scoped egress
  needs a parent-owned CONNECT proxy bound to an allow-list. **Tracked
  follow-up.**
- **`child_process`** — fails closed (`cave_sandbox_child_process_containment_unavailable`)
  until portable, verifiable descendant containment exists.
- **Non-Linux/macOS** — no OS isolation available; tools fail closed.

## Credentials

A live profile may receive exactly one provider credential family
(`SANDBOX_CREDENTIAL_ENV_BY_CAPABILITY`); everything else is stripped, and the
child starts from a fixed baseline env, not a spread of the parent's. Combined
with the kernel network boundary, a leaked key cannot be exfiltrated over IP
from `required` mode — subject to the unix-socket gap above on Linux.
