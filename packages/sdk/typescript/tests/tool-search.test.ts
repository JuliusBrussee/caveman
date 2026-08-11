/**
 * Type-level tests for the TS SDK tool-search surface.
 *
 * Compiled by `tsc --noEmit` (the package's "test" script).
 * Runtime assertions require `pnpm test:node` (node:test, see package.json).
 */
import type { Cave, CaveTool, ToolSearchResult } from "../src/index.js";

// ─── Type assertions ────────────────────────────────────────────────────────

declare const cave: Cave;
declare const catalog: CaveTool[];

// tools().search() must be async → returns Promise<ToolSearchResult>
const _searchReturn: Promise<ToolSearchResult> = cave.tools({ catalog }).search("find customer");

// cave.toolSearch() is the direct variant
const _directReturn: Promise<ToolSearchResult> = cave.toolSearch(catalog, "find customer");

// Options are optional
const _withOpts: Promise<ToolSearchResult> = cave.tools({ catalog, strategy: "deferred" }).search("q", { maxTools: 5, context: "ctx", workflow: "wf", toolSessionId: "tool-session-1" });

// ToolSearchResult fields exist with the right types
declare const r: ToolSearchResult;
const _tools: CaveTool[] = r.tools;
const _sent: number = r.sentSchemaTokens;
const _full: number = r.fullSchemaTokens;
const _deferred: number = r.deferredCount;
const _method: string = r.method;
const _session: string | undefined = r.sessionId;
const _saved: number = r.savedTokens;
const _pct: number = r.reductionPct;

// tools() still exposes strategy and initial (backward-compat surface)
const handle = cave.tools({ catalog, strategy: "deferred", initialToolCount: 4 });
const _strategy: "all" | "deferred" = handle.strategy;
const _initial: CaveTool[] = handle.initial;
