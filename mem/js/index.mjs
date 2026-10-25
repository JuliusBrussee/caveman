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

// Replacement bytes that still fit in one command-line argument. Linux caps a
// single argv element at MAX_ARG_STRLEN (128 KiB) and Windows caps the whole
// command line near 32 KiB; stay under both with room for the binary path, the
// id and the environment.
const ARGV_TEXT_LIMIT = process.platform === "win32" ? 24 * 1024 : 96 * 1024;

async function supersedeStdinSupported() {
  try {
    const { capabilities } = await call(["version", "--json"]);
    return Array.isArray(capabilities) && capabilities.includes("supersede-stdin");
  } catch {
    return false;
  }
}

// Replacement text that cannot ride one command-line argument, for any of three
// reasons: it exceeds the platform's argv limit; it is exactly "--stdin", which
// the binary reads as the flag rather than as text (and then takes whatever our
// own stdin happens to hold); or it embeds a NUL, which no OS can carry in an
// argument. Each one needs the stdin path, so each one needs the capability.
function argvCannotCarry(text) {
  return Buffer.byteLength(text) > ARGV_TEXT_LIMIT || text === "--stdin" || text.includes("\0");
}

/** Replace one current memory while preserving its version history. */
export async function supersede(id, text) {
  // A cavemem older than `supersede --stdin` reads its second argument as the
  // replacement text, so handing one "--stdin" stores the literal flag, expires
  // the real memory and reports success. Use the argv form for everything it
  // can carry — every build understands that — and require the advertised
  // capability before sending a replacement over stdin.
  if (!argvCannotCarry(text)) return call(["supersede", id, text]);
  if (!(await supersedeStdinSupported())) {
    throw new Error(
      "cavemem cannot supersede with this replacement: it has to be sent over stdin " +
        `(${Buffer.byteLength(text)} bytes, the literal "--stdin", or an embedded NUL), ` +
        "and this build does not advertise the supersede-stdin capability. " +
        "Upgrade the cavemem binary.",
    );
  }
  return call(["supersede", id, "--stdin"], text);
}

/** Return oldest-to-newest versions for a memory lineage. */
export function history(id) {
  return call(["history", id]);
}

/** Delete a memory by id. Returns { forgotten }. */
export function forget(id) {
  return call(["forget", id]);
}
