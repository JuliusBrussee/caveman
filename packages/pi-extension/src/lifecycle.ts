// Bridge from Pi lifecycle events to the Caveman native runtime, by invoking the
// Caveman CLI's `native-hook pi <Event>` adapter — the same single protocol
// implementation every other host uses (no second event protocol in here).

import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { homedir } from "node:os";
import { join } from "node:path";
import { type PortableInvocation, portableInvocation } from "./portable-command.ts";
import { type HookEvent, type HookResponse, sanitizeHookResponse } from "./protocol.ts";

const HOOK_TIMEOUT_MS = 2000;
// SessionStart autostarts the proxy on the CLI side (spawn + up to 2s of port
// polling on top of Node startup), so it gets a budget the CLI can actually
// meet on a cold machine.
const SESSION_START_TIMEOUT_MS = 6000;
const HOOK_MAX_BUFFER = 2 * 1024 * 1024;

export type HookInvocation = { command: string; args: string[] };

// Wrap stamps CAVEMAN_PI_HOOK_CMD as a JSON argv array; the persistent door has
// no wrap env, so fall back to the CLI on PATH.
export function resolveHookInvocations(env: NodeJS.ProcessEnv = process.env): HookInvocation[] {
  const stamped = env.CAVEMAN_PI_HOOK_CMD;
  if (stamped) {
    try {
      const parsed = JSON.parse(stamped);
      if (Array.isArray(parsed) && parsed.length > 0 && parsed.every((part) => typeof part === "string" && part.length > 0)) {
        return [{ command: parsed[0], args: parsed.slice(1) }];
      }
    } catch {
      // fall through to PATH resolution
    }
  }
  const localBin = join(env.CAVEMAN_HOME || join(homedir(), ".caveman"), "bin", "caveman");
  return [
    { command: "caveman", args: [] },
    { command: "cave", args: [] },
    { command: localBin, args: [] },
  ];
}

export class HookBridge {
  private invocations: HookInvocation[];

  constructor(invocations: HookInvocation[] = resolveHookInvocations()) {
    this.invocations = invocations;
  }

  // call runs one native-hook event. Fail-open by contract: any spawn error,
  // timeout, or unparseable output resolves to undefined.
  async call(event: HookEvent, payload: Record<string, unknown>): Promise<HookResponse | undefined> {
    const input = JSON.stringify({ event_name: event, surface: "cli", ...payload });
    for (const invocation of this.invocations) {
      const raw = await this.invoke(invocation, event, input);
      if (raw === "ENOENT") continue; // try the next candidate binary
      if (raw === undefined || !raw.trim()) return undefined;
      try {
        return sanitizeHookResponse(JSON.parse(raw));
      } catch {
        return undefined;
      }
    }
    return undefined;
  }

  private invoke(invocation: HookInvocation, event: HookEvent, input: string): Promise<string | undefined | "ENOENT"> {
    return new Promise((resolve) => {
      // On Windows `caveman` on PATH is a .cmd shim (or a non-executable Unix
      // shim beside it), and ~/.caveman/bin/caveman is extensionless — execFile
      // cannot run either and dies with `spawn EFTYPE`, which broke every hook
      // call on Windows. Resolve to something spawnable first. A shim we cannot
      // parse throws, which the fail-open contract treats as "try the next
      // candidate" rather than taking down the caller.
      let portable: PortableInvocation;
      try {
        portable = portableInvocation(invocation.command, [...invocation.args, "native-hook", "pi", event]);
      } catch {
        resolve("ENOENT");
        return;
      }
      const child = execFile(
        portable.command,
        portable.args,
        { timeout: event === "SessionStart" ? SESSION_START_TIMEOUT_MS : HOOK_TIMEOUT_MS, maxBuffer: HOOK_MAX_BUFFER, encoding: "utf8" },
        (error, stdout) => {
          if (error && (error as NodeJS.ErrnoException).code === "ENOENT") return resolve("ENOENT");
          if (error && stdout === "") return resolve(undefined);
          resolve(stdout);
        },
      );
      child.stdin?.on("error", () => {});
      child.stdin?.end(input);
    });
  }
}

export function promptDigest(prompt: string): { bytes: number; sha256: string } {
  return {
    bytes: Buffer.byteLength(prompt, "utf8"),
    sha256: `sha256:${createHash("sha256").update(prompt, "utf8").digest("hex")}`,
  };
}

// Task profiling: same classifier the other host plugins embed, so the runtime
// sees one vocabulary. Raw prompt text never leaves the process — only these
// bounded derived terms and the digest above.
export function taskType(text: string): string {
  const lower = text.toLowerCase();
  const has = (...terms: string[]) => terms.some((term) => lower.includes(term));
  if (has("migration", "migrate", "schema change", "backfill", "rollback")) return "migration";
  if (has("bug", "fix", "broken", "regression", "crash", "error", "incorrect")) return "bugfix";
  if (has("investigate", "diagnose", "root cause", "why does", "trace")) return "investigation";
  if (has("refactor", "restructure", "reorganize", "cleanup")) return "refactor";
  if (has("review", "audit", "critique", "assess")) return "review";
  if (has("verify", "verification", "prove", "validate", "check that")) return "verification";
  if (has("build", "implement", "add", "create", "ship", "feature")) return "feature";
  return "general";
}

const TASK_STOPWORDS = new Set(["about", "after", "agent", "before", "build", "change", "code", "create", "from", "have", "help", "implement", "into", "make", "please", "project", "repository", "should", "spec", "task", "that", "then", "there", "these", "they", "this", "through", "user", "want", "what", "when", "where", "which", "with", "would", "your"]);

export function taskTerms(text: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of text.match(/[A-Za-z][A-Za-z0-9_./-]{2,63}/g) ?? []) {
    const term = raw.toLowerCase().replace(/^[-./]+|[-./]+$/g, "");
    if (!term || term.includes("..") || TASK_STOPWORDS.has(term) || seen.has(term)) continue;
    // Secret-looking tokens stay out of the wire entirely.
    if (/^(?:sk|pk|rk|ghp|github_pat|xox[baprs]|akia)[-_]/i.test(term) || /^[a-z0-9_-]{40,}$/i.test(term)) continue;
    seen.add(term);
    out.push(term);
    if (out.length === 12) break;
  }
  return out;
}

export function taskContinuation(text: string): boolean {
  const prompt = text.trim().toLowerCase();
  if (!prompt || prompt.length > 160 || prompt.split(/\s+/).length > 14) return false;
  return /^(?:please\s+)?(?:continue|go ahead|keep going|proceed|do (?:it|that)|fix (?:it|that)|retry|try again|explain (?:it|that)|what do you mean|yes|yep|yeah|why\??|how\??)[.!?\s]*$/.test(prompt);
}
