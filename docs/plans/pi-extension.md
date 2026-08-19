# Plan — native Pi integration (`@caveman-ai/pi`)

Target: `caveman wrap pi` (ephemeral `pi -e <bundled-extension>`), `caveman enable pi`
(persistent, journaled), and plain `pi` afterward — proxy routing, `caveman_retrieve`
recovery, native lifecycle events, Core injection, tool-output shrinking. Pi pin: 0.84.2
(`@earendil-works/pi-coding-agent`, verified against the locally installed binary).

Ground truth discovered during recon (deviations from the original spec sketch):

- **Proxy needs zero code.** `/w/<slug>` is a generic attribution prefix
  (`proxy/providers/agentpath.go`); `/w/pi/openai/v1/chat/completions`,
  `/w/pi/v1/messages`, `/w/pi/v1beta/models/...` all already route to the right
  adapters. Slug `pi` is valid.
- **The Go native runtime validates no host ids** — `agent.id` is free-form. Only
  `directOutputRewriteAgent` (runtime.go) enumerates hosts; `pi` is added there for
  `output_replacement`.
- **The extension does NOT speak the socket protocol itself.** Like the generated
  opencode plugin, it shells out to `caveman native-hook pi <Event>` (JSON on stdin,
  hook JSON on stdout) — one protocol implementation, reused. `native-hook-fast.ts`
  gains `pi` with a raw-response output contract (opencode-style).
- **`enable pi` reuses the existing file-mutation journal machinery** instead of the
  spec's `pi install npm:@caveman-ai/pi` path: the extension file is written to
  `~/.pi/agent/extensions/caveman-native.js` (Pi auto-discovers that dir), journaled
  with sha256 ownership like the opencode plugin. This keeps `~/.pi/agent/settings.json`
  byte-identical (stronger than the spec asked), works offline, and gets
  crash-recovery/ownership-refusal for free. `pi install npm:@caveman-ai/pi` remains a
  documented manual alternative once the package is published to npm.

## Stages

1. **Profile + compiler** (`agents/`): add `native-extension` injection method
   (closed fields: `host` ∈ {pi}, `asset` ∈ {caveman-pi-extension}, `loader_flag` ∈
   {--extension}) to `profiles/schema.json` + `compile.mjs` (INJECTION_METHODS,
   validation branch, emitted `Injection` union, completeness gate: `native-extension`
   ⇒ exactly `builder-assisted`, checked before the builder-manifest branch);
   `pi-extension` command_hook method; `agents/profiles/pi.json`.
2. **`packages/pi-extension`** (new, self-contained): the real extension.
   - `src/index.ts` — factory; no background resources in the factory (Pi docs rule).
   - `src/protocol.ts` — bounded shared types + validation caps (context 64KiB,
     message 4KiB, output_replacement 2MiB, recovery_ref 1KiB, decision_id 256B).
   - `src/lifecycle.ts` — Pi events → `caveman native-hook pi <Event>` subprocess
     (session_start→SessionStart, before_agent_start→UserPromptSubmit,
     turn_start→ModelBefore, turn_end→ModelAfter+Stop, tool_call→PreToolUse,
     tool_result→PostToolUse/PostToolUseFailure, session_before_compact→PreCompact,
     session_compact→PostCompact, session_shutdown→SessionEnd). Digest-only prompt
     payloads; 2s timeout; fail-open everywhere.
   - `src/provider.ts` — baseUrl-only `pi.registerProvider` overrides per model API:
     anthropic-messages→`/w/pi`, openai-completions/openai-responses→`/w/pi/openai/v1`,
     google-generative-ai→`/w/pi/v1beta`. Route only after gate passes; OAuth-authed
     credentials and unsupported APIs (azure/bedrock/vertex/mistral/codex) refuse →
     pass-through with visible off-state notice. `model_select` re-resolves +
     `pi.setModel` once, recursion-guarded; unsupported target restores direct.
   - `src/recovery.ts` — `RecoveryClient`: lazy spawn of `caveman-mcp`
     (CAVEMAN_MCP_BIN → PATH → ~/.caveman/bin), `version --json` probe requires
     `mcp_recovery` capability, MCP `initialize` (2024-11-05), `tools/call
     caveman_retrieve` only; one child per session (anti-storm ledger lives in the
     child); restart after unexpected exit; abort-signal cancellation; stdin close +
     terminate on `session_shutdown`; MCP errors surface as Pi tool errors verbatim.
   - `src/tool-output.ts` — `tool_result` → PostToolUse; replace content only when the
     runtime returns a valid `output_replacement`, preserving details/shape.
   - Gate (in index.ts): overrides register only when SessionStart hook succeeded AND
     run-state (`~/.caveman/run/<port>.json`, schema caveman.proxy.run.v1) shows a
     listening proxy whose `recovery_via_mcp` equals our own MCP-child readiness AND
     the MCP child initialized. Anything else → direct mode, one notice, no savings
     claims.
   - Build: tsc + esbuild bundle to `dist/index.mjs` with pi peers + typebox external.
     Manifest: `pi.extensions: ["./dist/index.mjs"]`, keyword `pi-package`, peerDeps
     `"*"` per Pi packaging rules.
3. **CLI wiring** (`packages/cli`, one Go line in `proxy/`):
   - `pi` in every host union (native-hook-fast.ts ×3; index.ts NativeAgent + capability
     matrix + post_tool_rewrite set + doctor/status/enable/disable/expectedRoute/
     nativeIntegrationStatus/nativeAgentId/native-hook guard sites), raw-response
     output contract for `pi` in both hooks, `directOutputRewriteAgent` + "pi" in
     runtime.go, `validAgent` in nativehook/hook.go.
   - Wrap: `buildPiWrapArgs` builder → childArgs `["-e", <resolved extension>, ...]`,
     fail-open on resolution failure (direct + warning). Resolution:
     `CAVEMAN_PI_EXTENSION` → `dist/caveman-pi-extension.mjs` next to the CLI.
   - `scripts/bundle-pi-extension.mjs` in the build/test chain (delegate-MCP pattern).
   - Enable/disable: `piNativeMutations` writes the bundled extension to
     `~/.pi/agent/extensions/caveman-native.js` (kind `pi-extension`), journaled;
     disable restores via existing machinery; drift ⇒ refusal, not deletion.
   - `pnpm-workspace.yaml` + capability rows + status/doctor text.
4. **Tests** — package unit tests (protocol bounds, recovery client vs stub MCP server,
   route table, gate) + a live integration test against the pinned local `pi` binary
   (`pi -e dist/index.mjs -p` with a custom provider pointed at a local HTTP stub;
   skipped when `pi` absent) + CLI runtime tests (wrap argv/env via fake `pi` bin,
   enable/disable journal + refusal, status row, registry gates).
5. **Review** — independent multi-lens review + adversarial verify before done.

## Honesty rails carried over

- Profile `wire_protocol: "openai-chat"` (single value; the multi-API routing is code,
  labeled builder-assisted). No savings after direct fallback (`summaryKind` machinery
  already enforces; extension never claims compression when passing through).
- Proxy books no compression savings under MCP recovery (existing rule, unchanged).
- No OAuth token copying into provider config; OAuth-authed models refuse routing.
