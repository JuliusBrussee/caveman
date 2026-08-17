import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

function envValue(env, name) {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

function resolveWindowsCommand(command, env) {
  if (isAbsolute(command) || /[\\/]/.test(command)) return existsSync(command) ? command : null;
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
  return null;
}

export function portableInvocation(command, args, platform = process.platform, env = process.env) {
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) ?? command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const stat = statSync(executable);
  if (!stat.isFile() || stat.size > 256 * 1024) throw new Error(`unsafe Windows command shim: ${executable}`);
  let relativeScript = null;
  for (const line of readFileSync(executable, "utf8").split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) { relativeScript = match[1]; break; }
  }
  if (!relativeScript) throw new Error(`non-Node Windows command shim: ${executable}`);
  const script = resolve(dirname(executable), ...relativeScript.split(/[\\/]+/));
  if (!statSync(script).isFile()) throw new Error(`Windows command shim target missing: ${script}`);
  return { command: process.execPath, args: [script, ...args] };
}

export function delegateSpawnOptions(platform = process.platform) {
  return {
    detached: platform !== "win32",
    windowsHide: true,
  };
}

export async function killProcessTree(
  child,
  platform = process.platform,
  taskkill = spawnSync,
  kill = process.kill,
  graceMs = 1500,
) {
  if (!child?.pid) return;
  if (platform === "win32") {
    const result = taskkill("taskkill.exe", ["/pid", String(child.pid), "/t", "/f"], {
      stdio: "ignore",
      windowsHide: true,
    });
    if (result?.error || (typeof result?.status === "number" && result.status !== 0)) {
      try { child.kill("SIGKILL"); } catch {}
    }
    return;
  }
  try { kill(-child.pid, "SIGTERM"); } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
  await new Promise((done) => setTimeout(done, graceMs));
  try { kill(-child.pid, "SIGKILL"); } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}
