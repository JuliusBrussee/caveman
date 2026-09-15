// cavemem — thin TypeScript/JS client. It shells out to the `cavemem` Go binary
// (the single source of truth for storage, BM25 recall, and engine compression);
// it does not reimplement any of that. Resolve the binary via CAVEMEM_BIN or PATH.
import { execFile } from "node:child_process";

/** Process exit code returned when one memory exceeds the binary's byte cap. */
export const MEMORY_TOO_LARGE_EXIT_CODE = 65;

function binary() {
  return process.env.CAVEMEM_BIN || "cavemem";
}

async function call(args, input) {
  const { stdout } = await new Promise((resolve, reject) => {
    const child = execFile(binary(), args, { maxBuffer: 32 * 1024 * 1024 }, (error, out, err) => {
      if (error) reject(error);
      else resolve({ stdout: out, stderr: err });
    });
    child.stdin.on("error", (error) => {
      // Oversized input may make the binary close stdin after MaxMemoryBytes+1.
      // Its exit 65 is the contract; an EPIPE while finishing the write is not a
      // second failure and must not replace that code.
      if (error.code !== "EPIPE") reject(error);
    });
    child.stdin.end(input);
  });
  return JSON.parse(stdout);
}

/** Store a memory. Returns { id, created_at, basis }. Idempotent on identical text. */
export function remember(text) {
  return call(["remember", "--stdin"], text);
}

/** Recall memories relevant to a query. tokenBudget defaults to 2000; 0 is unlimited. */
export function recall(query, limit, tokenBudget) {
  const args = ["recall", query];
  if (limit !== undefined || tokenBudget !== undefined) args.push(String(limit ?? 0));
  if (tokenBudget !== undefined) args.push(String(tokenBudget));
  return call(args);
}

/** Replace one current memory while preserving its version history. */
export function supersede(id, text) {
  return call(["supersede", id, text]);
}

/** Return oldest-to-newest versions for a memory lineage. */
export function history(id) {
  return call(["history", id]);
}

/** Delete a memory by id. Returns { forgotten }. */
export function forget(id) {
  return call(["forget", id]);
}
