import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

/**
 * Durable, tenant-scoped local memory.
 *
 * `memory({ ttl: "30d" })` is a REAL 30-day promise: entries persist across
 * process restarts in a per-namespace JSON file written with an atomic
 * temp-write + rename, not a process-lifetime Map whose ttl was fiction. The
 * store is deliberately dep-free — @caveman-ai/agent keeps a tight dependency
 * surface, so this uses only node:fs rather than pulling in a SQLite driver.
 *
 * A durable, byte-exact-recoverable local store. It records memory state; it
 * does not measure provider usage or savings.
 */
export interface MemoryEntry {
  readonly text: string;
  readonly createdAt: number;
}

/**
 * Where durable memory lives, and for whom. Threaded from
 * `RunOptions.memory` so an embedding server can point at its own location AND
 * scope per tenant, so two tenants sharing an agent id + namespace never see
 * each other's memories.
 */
export interface MemoryStoreConfig {
  /**
   * Base directory. Defaults to `CAVE_AGENT_MEMORY_ROOT`, else
   * `~/.caveman/agent-memory`. A stable location so a 30-day ttl survives
   * reboots, not just process restarts.
   */
  readonly root?: string;
  /** Tenant scope. Defaults to `_` (single-tenant). Isolates per tenant. */
  readonly tenant?: string;
}

const TENANT_PATTERN = /^[a-z0-9][a-z0-9_-]{0,127}$/i;
const AGENT_PATTERN = /^[a-zA-Z][a-zA-Z0-9_-]{0,127}$/;
const NAMESPACE_PATTERN = /^[a-z0-9][a-z0-9_-]{0,95}$/;

function defaultRoot(): string {
  return process.env.CAVE_AGENT_MEMORY_ROOT ?? join(homedir(), ".caveman", "agent-memory");
}

/**
 * The durable file for (tenant, agentId, namespace). The three scoping
 * components are validated to a `[a-z0-9_-]`-class charset with no `.` or path
 * separator, so no component can traverse out of the memory root.
 */
export function memoryFilePath(
  config: MemoryStoreConfig | undefined,
  agentId: string,
  namespace: string,
): string {
  const tenant = config?.tenant ?? "_";
  if (tenant !== "_" && !TENANT_PATTERN.test(tenant)) {
    throw new Error("cave_memory_tenant_invalid");
  }
  if (!AGENT_PATTERN.test(agentId)) throw new Error("cave_memory_agent_invalid");
  if (!NAMESPACE_PATTERN.test(namespace)) throw new Error("cave_memory_namespace_invalid");
  return join(config?.root ?? defaultRoot(), tenant, agentId, `${namespace}.json`);
}

function isMemoryEntry(value: unknown): value is MemoryEntry {
  return value !== null && typeof value === "object" &&
    typeof (value as { text?: unknown }).text === "string" &&
    Number.isSafeInteger((value as { createdAt?: unknown }).createdAt);
}

/** Read the durable entries. A missing or corrupt file is an empty store, never a throw into a run. */
export async function readMemories(filePath: string): Promise<MemoryEntry[]> {
  try {
    const parsed: unknown = JSON.parse(await readFile(filePath, "utf8"));
    return Array.isArray(parsed) ? parsed.filter(isMemoryEntry) : [];
  } catch {
    return [];
  }
}

// Per-path serialization so a read-modify-write in one process cannot lose a
// concurrent one. Across processes, the atomic rename still prevents a torn
// file; the weaker guarantee there is last-writer-wins, which is acceptable for
// a per-tenant memory namespace and documented in the README.
const writeChains = new Map<string, Promise<unknown>>();

async function atomicWrite(filePath: string, entries: readonly MemoryEntry[]): Promise<void> {
  await mkdir(dirname(filePath), { recursive: true, mode: 0o700 });
  const tmp = `${filePath}.${process.pid}.${crypto.randomUUID()}.tmp`;
  // Memory may contain prompt, customer, or tool-result data. Keep both final
  // file and atomic temporary private, independent of process umask. `wx`
  // also refuses a pre-existing path instead of following it.
  try {
    await writeFile(tmp, JSON.stringify(entries), {
      encoding: "utf8",
      flag: "wx",
      mode: 0o600,
    });
    await rename(tmp, filePath);
  } finally {
    // A failed write/rename must not strand prompt or customer data in a
    // discoverable temp file.
    await rm(tmp, { force: true });
  }
}

/**
 * Serialized read-modify-write of one namespace file. The mutator runs on the
 * current durable entries and its result is persisted atomically; the resolved
 * value is the persisted set. Used for both remember (append) and recall
 * (evict-expired-on-read).
 */
export function mutateMemories(
  filePath: string,
  mutator: (entries: MemoryEntry[]) => MemoryEntry[],
): Promise<MemoryEntry[]> {
  const previous = writeChains.get(filePath) ?? Promise.resolve();
  const next = previous.catch(() => undefined).then(async () => {
    const current = await readMemories(filePath);
    const updated = mutator([...current]);
    await atomicWrite(filePath, updated);
    return updated;
  });
  writeChains.set(filePath, next);
  void next.catch(() => undefined).finally(() => {
    if (writeChains.get(filePath) === next) writeChains.delete(filePath);
  });
  return next;
}
