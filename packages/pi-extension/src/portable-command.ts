// Windows-safe command invocation.
//
// `execFile("caveman", …)` on Windows resolves to whatever PATH offers first.
// npm/pnpm .bin directories park a NON-EXECUTABLE Unix shim under the bare name
// right next to the real `.CMD`, and `~/.caveman/bin/caveman` is likewise an
// extensionless script. Handing either to execFile fails with `spawn EFTYPE`,
// which is what took down every pi-extension hook call on Windows.
//
// Resolve through PATHEXT the way Windows does, then, for a `.cmd`/`.bat` Node
// shim, read the shim and launch its target script with the current Node binary
// directly — no shell, so no quoting surface for user-controlled paths (#834).
//
// This is a deliberate fourth copy of the helper that already lives in
// bin/lib/portable-process.js, packages/cli/src/portable-command.ts, and
// packages/agent/src/portable-process.ts. This package publishes with zero
// runtime dependencies and is esbuild-bundled, so importing across packages
// would mean taking on a dependency purely for ~50 lines. Keep the four in sync.

import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

export type PortableInvocation = { command: string; args: string[] };

export function parseWindowsNodeShim(source: string): string | null {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    // Shim-relative target (npm cmd-shim, pnpm/yarn-classic @zkochan forms), or
    // a drive-absolute target (pnpm emits one when the global bin dir and the
    // store sit on different drives — path.relative crosses drives as absolute).
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i)
      ?? line.match(/"([A-Za-z]:[\\/][^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1]!;
  }
  return null;
}

function envValue(env: NodeJS.ProcessEnv, name: string): string | undefined {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

export function resolveWindowsCommand(command: string, env: NodeJS.ProcessEnv): string | undefined {
  const pathExt = envValue(env, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
  // Extensionless names resolve ONLY through PATHEXT — probing the bare name
  // first picks the unusable Unix shim sitting beside the real .CMD (#834).
  const names = extname(command)
    ? [command]
    : pathExt.split(";").map((extension) =>
      `${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  // A path (~/.caveman/bin/caveman, the hook bridge's third candidate) skips
  // PATH lookup but NOT PATHEXT: returning the extensionless file straight to
  // execFile is the exact EFTYPE this file exists to prevent.
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

// Returns the command/args pair to hand to execFile/spawn. Non-win32 is a
// pass-through, so this is safe to route every invocation through.
//
// Fail-open by contract: the hook bridge treats a throw the same as a spawn
// error, and an unresolvable command falls through to the next candidate, so a
// shim we cannot parse must never take down the caller.
export function portableInvocation(
  command: string,
  args: readonly string[],
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
): PortableInvocation {
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) ?? command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const stat = statSync(executable);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${executable}`);
  }
  const shimScript = parseWindowsNodeShim(readFileSync(executable, "utf8"));
  if (!shimScript) {
    throw new Error(`cannot safely launch non-Node Windows command shim: ${executable}; install a native .exe`);
  }
  const script = /^[A-Za-z]:[\\/]/.test(shimScript)
    ? shimScript
    : resolve(dirname(executable), ...shimScript.split(/[\\/]+/));
  if (!statSync(script).isFile()) {
    throw new Error(`Windows command shim target is missing: ${script}`);
  }
  return { command: process.execPath, args: [script, ...args] };
}
