import { spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

export type PortableInvocation = Readonly<{ command: string; args: readonly string[] }>;

function envValue(env: NodeJS.ProcessEnv, name: string): string | undefined {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

export function resolveWindowsCommand(command: string, env: NodeJS.ProcessEnv): string | undefined {
  if (isAbsolute(command) || /[\\/]/.test(command)) return existsSync(command) ? command : undefined;
  const pathExt = envValue(env, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics; the bare name next to a .CMD shim is a non-executable Unix shim.
  const names = extname(command)
    ? [command]
    : pathExt.split(";").map((extension) =>
      `${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  for (const directory of (envValue(env, "PATH") ?? "").split(";")) {
    if (!directory) continue;
    for (const name of names) {
      const candidate = join(directory, name);
      if (existsSync(candidate)) return candidate;
    }
  }
  return undefined;
}

export function parseWindowsNodeShim(source: string): string | undefined {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match?.[1]) return match[1];
  }
  return undefined;
}

export function portableInvocation(
  command: string,
  args: readonly string[],
  options: {
    platform?: NodeJS.Platform;
    env?: NodeJS.ProcessEnv;
    execPath?: string;
  } = {},
): PortableInvocation {
  const platform = options.platform ?? process.platform;
  const env = options.env ?? process.env;
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) ?? command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const stat = statSync(executable);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${executable}`);
  }
  const relativeScript = parseWindowsNodeShim(readFileSync(executable, "utf8"));
  if (!relativeScript) {
    throw new Error(`cannot safely launch non-Node Windows command shim: ${executable}`);
  }
  const script = resolve(dirname(executable), ...relativeScript.split(/[\\/]+/));
  if (!statSync(script).isFile()) throw new Error(`Windows command shim target is missing: ${script}`);
  return { command: options.execPath ?? process.execPath, args: [script, ...args] };
}

export function hostShellInvocation(
  source: string,
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
): PortableInvocation {
  if (platform === "win32") {
    return {
      command: envValue(env, "ComSpec") ?? "cmd.exe",
      args: ["/d", "/s", "/c", source],
    };
  }
  return { command: "/bin/sh", args: ["-c", source] };
}

export function killProcessTree(
  child: ChildProcess,
  signal: NodeJS.Signals = "SIGKILL",
  options: {
    platform?: NodeJS.Platform;
    kill?: typeof process.kill;
    taskkill?: typeof spawnSync;
  } = {},
): void {
  if (child.pid === undefined) return;
  const platform = options.platform ?? process.platform;
  if (platform === "win32") {
    const result = (options.taskkill ?? spawnSync)(
      "taskkill.exe",
      ["/pid", String(child.pid), "/t", "/f"],
      { stdio: "ignore", windowsHide: true },
    );
    if (result.error || (typeof result.status === "number" && result.status !== 0)) {
      try { child.kill(signal); } catch { /* already exited */ }
    }
    return;
  }
  try {
    (options.kill ?? process.kill)(-child.pid, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
  }
}
