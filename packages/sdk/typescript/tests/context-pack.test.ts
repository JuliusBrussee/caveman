import type { Cave, ContextPackItem, ContextPackResult } from "../src/index.js";

declare const cave: Cave;
declare const items: ContextPackItem[];

const _result: Promise<ContextPackResult> = cave.context.pack("deploy failure", items, {
  maxTokens: 8_000,
  reserveTokens: 1_000,
});

declare const result: ContextPackResult;
const _items: ContextPackItem[] = result.items;
const _deferredIds: string[] = result.deferredIds;
const _basis: "inferred" = result.basis;
