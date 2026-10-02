import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

export type PortableInvocation = { command: string; args: string[] };

export function parseWindowsNodeShim(source: string): string | null {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    // Shim-relative target (npm cmd-shim, pnpm/yarn-classic @zkochan forms), or
    // a drive-absolute target (pnpm emits one when the global bin dir and the
    // store sit on different drives — path.relative crosses drives as absolute).
    // %~dp0 already ends with a separator; managed Pi adds none before its target.
    const match = line.match(/"%(?:dp0%|~dp0)\\?([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i)
      ?? line.match(/"([A-Za-z]:[\\/][^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1]!;
  }
  return null;
}

// Accept only a single shim-relative forwarding command, optionally preceded by
// @echo off. Never evaluate batch syntax or interpolate caller-controlled argv.
function parseWindowsNestedShim(source: string): string | null {
  const match = source.match(/^(?:@echo[ \t]+off[ \t]*\r?\n)?[ \t]*"%~dp0\\?([\w .@+\-\\/]+\.(?:cmd|bat))"[ \t]+%\*[ \t]*(?:\r?\n)?$/i);
  return match?.[1] ?? null;
}

function resolveWindowsNodeShim(executable: string, depth = 0, seen = new Set<string>()): string {
  if (depth > 4) throw new Error("Windows command shim nesting too deep");
  const normalized = resolve(executable);
  if (seen.has(normalized.toLowerCase())) throw new Error("Windows command shim cycle");
  seen.add(normalized.toLowerCase());
  const stat = statSync(normalized);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${normalized}`);
  }
  const source = readFileSync(normalized, "utf8");
  // Batch forwarding must match the whole wrapper, not a later Node command.
  const nested = parseWindowsNestedShim(source);
  const forwardsToBatch = /"[^"\r\n]+\.(?:cmd|bat)"[ \t]+%\*/i.test(source);
  const jsTarget = forwardsToBatch ? null : parseWindowsNodeShim(source);
  const child = jsTarget ?? nested;
  if (!child) {
    throw new Error(`cannot safely launch non-Node Windows command shim: ${normalized}; install a native .exe`);
  }
  const target = /^[A-Za-z]:[\\/]/.test(child)
    ? child
    : resolve(dirname(normalized), ...child.split(/[\\/]+/));
  if (!existsSync(target) || !statSync(target).isFile()) throw new Error(`Windows command shim target is missing: ${target}`);
  return jsTarget ? target : resolveWindowsNodeShim(target, depth + 1, seen);
}

function envValue(env: NodeJS.ProcessEnv, name: string): string | undefined {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

// Resolve a bare command through PATH/PATHEXT the way Windows does. This used
// to be the caller's job, and the contract was invisible: hand this function a
// bare "npx" on win32 and the .cmd test below missed, so it handed the bare
// name straight to spawn(), which fails with EINVAL rather than launching
// anything. Every other copy of this helper in the repo resolves internally;
// so does this one now. Extensionless names resolve only through PATHEXT —
// npm/pnpm .bin dirs park a non-executable Unix shim under the bare name next
// to the real .CMD, and probing the bare name first picks the wrong one (#834).
export function resolveWindowsCommand(command: string, env: NodeJS.ProcessEnv): string | undefined {
  const pathExt = envValue(env, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
  const names = extname(command)
    ? [command]
    : pathExt.split(";").map((extension) =>
      `${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  // A path skips PATH lookup but NOT PATHEXT: handing an extensionless file
  // straight to execFile is the exact EFTYPE this function exists to prevent.
  if (isAbsolute(command) || /[\\/]/.test(command)) {
    for (const name of names) if (existsSync(name)) return name;
    return existsSync(command) ? command : undefined;
  }
  for (const directory of (envValue(env, "PATH") ?? "").split(";")) {
    if (!directory) continue;
    for (const name of names) {
      const candidate = join(directory, name);
      if (existsSync(candidate)) return candidate;
    }
  }
  return undefined;
}

export function portableInvocation(
  command: string,
  args: readonly string[],
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
): PortableInvocation {
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) ?? command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const script = resolveWindowsNodeShim(executable);
  return { command: process.execPath, args: [script, ...args] };
}
