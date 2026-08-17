import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

function windowsEnvValue(env, name) {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key ? env[key] : undefined;
}

export function resolveWindowsCommand(command, env = process.env) {
  if (isAbsolute(command) || /[\\/]/.test(command)) return existsSync(command) ? command : null;
  const pathExt = windowsEnvValue(env, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics; the bare name next to a .CMD shim is a non-executable Unix shim.
  const names = extname(command)
    ? [command]
    : pathExt.split(";").map((extension) =>
      `${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  for (const directory of (windowsEnvValue(env, "PATH") ?? "").split(";")) {
    if (!directory) continue;
    for (const name of names) {
      const candidate = join(directory, name);
      if (existsSync(candidate)) return candidate;
    }
  }
  return null;
}

export function parseWindowsNodeShim(source) {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1];
  }
  return null;
}

export function portableProcessInvocation(
  command,
  args,
  { platform = process.platform, env = process.env, execPath = process.execPath } = {},
) {
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env);
  if (!executable) throw Object.assign(new Error(`command not found: ${command}`), { code: "ENOENT" });
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
  return { command: execPath, args: [script, ...args] };
}

export function harnessSpawnOptions(platform = process.platform) {
  return {
    detached: platform !== "win32",
    windowsHide: true,
  };
}

export function forceKillTree(
  child,
  {
    platform = process.platform,
    kill = process.kill,
    taskkill = spawnSync,
  } = {},
) {
  if (!child?.pid) return;
  if (platform === "win32") {
    const result = taskkill(
      "taskkill.exe",
      ["/pid", String(child.pid), "/t", "/f"],
      { stdio: "ignore", windowsHide: true },
    );
    if ((result?.error && result.error.code !== "ESRCH") ||
        (typeof result?.status === "number" && result.status !== 0)) {
      try { child.kill(); } catch { /* process already exited */ }
    }
    return;
  }
  try { kill(-child.pid, "SIGKILL"); } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}

export async function stopTree(
  child,
  {
    platform = process.platform,
    kill = process.kill,
    taskkill = spawnSync,
    graceMs = 1500,
  } = {},
) {
  if (!child?.pid) return;
  if (platform === "win32") {
    forceKillTree(child, { platform, kill, taskkill });
    return;
  }
  try { kill(-child.pid, "SIGTERM"); } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
  await new Promise((done) => {
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      done();
    };
    const timer = setTimeout(() => {
      forceKillTree(child, { platform, kill, taskkill });
      finish();
    }, graceMs);
    child.once("exit", finish);
  });
}
