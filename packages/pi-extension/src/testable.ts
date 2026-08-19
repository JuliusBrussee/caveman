// Test-only surface: re-exports internals so the node --test suite can exercise
// them from the built dist without reaching into TS sources.
export * from "./protocol.ts";
export { HookBridge, promptDigest, resolveHookInvocations, taskContinuation, taskTerms, taskType } from "./lifecycle.ts";
export { RecoveryClient, resolveMcpBinary } from "./recovery.ts";
