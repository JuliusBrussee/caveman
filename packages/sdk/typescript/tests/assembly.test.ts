import type {
  AssembleOptions,
  AssemblyResult,
  AssemblySlot,
  Cave,
  EmitCacheHints,
} from "../src/index.js";

declare const cave: Cave;
declare const slots: AssemblySlot[];
declare const emitCacheHints: EmitCacheHints;

const options: AssembleOptions = {
  provider: "anthropic",
  model: "claude-test",
  sessionId: "session-1",
  slots,
  emitCacheHints,
};
const result: AssemblyResult = cave.assemble(options);
const _request: Record<string, unknown> = result.request;
const _header: string = result.headers["x-cave-assembly"];
const _basis: "inferred" = result.basis;
const _vbb: true = result.volatileBelowBreakpoint;
