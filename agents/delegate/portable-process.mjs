import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, isAbsolute, join, resolve } from "node:path";

// A JS port of packages/cli/src/portable-command.ts: this file is copied
// verbatim into the CLI's dist/ beside the delegate, so it cannot import the
// TypeScript. packages/cli/tests/portable-command.runtime.mjs fails if the two
// launch any shim differently.

function envValue(env, name) {
  const key = Object.keys(env).find((candidate) => candidate.toLowerCase() === name.toLowerCase());
  return key === undefined ? undefined : env[key];
}

function resolveWindowsCommand(command, env) {
  const pathExt = envValue(env, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD";
  // Extensionless commands resolve only through PATHEXT, matching Windows
  // semantics; the bare name next to a .CMD shim is a non-executable Unix shim.
  const names = extname(command)
    ? [command]
    : pathExt.split(";").map((extension) =>
      `${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  // A path skips PATH lookup but not PATHEXT.
  if (isAbsolute(command) || /[\\/]/.test(command)) {
    for (const name of names) if (existsSync(name)) return name;
    return existsSync(command) ? command : null;
  }
  for (const directory of (envValue(env, "PATH") ?? "").split(";")) {
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
    // Shim-relative target (npm cmd-shim, pnpm/yarn-classic @zkochan forms), or
    // a drive-absolute target (pnpm emits one when the global bin dir and the
    // store sit on different drives — path.relative crosses drives as absolute).
    // %~dp0 already ends with a separator; managed Pi adds none before its target.
    const match = line.match(/"%(?:dp0%|~dp0)\\?([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i)
      || line.match(/"([A-Za-z]:[\\/][^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1];
  }
  // Node's own npm.cmd / npx.cmd set
  //   SET "NPX_CLI_JS=%~dp0\node_modules\npm\bin\npx-cli.js"
  // and launch `"%NODE_EXE%" "%NPX_CLI_JS%" %*`. Only that pair is accepted.
  // ponytail: takes the npm bundled with Node, not a globally upgraded one.
  const npm = source.match(/SET\s+"(NP[MX])_CLI_JS=%~dp0\\([^"\r\n]+\.js)"[\s\S]*"%NODE_EXE%"\s+"%\1_CLI_JS%"\s+%\*/i);
  return npm ? npm[2] : null;
}

// A shim that forwards to a native executable: npm's shim for an .exe bin and
// pnpm's `@"<target>" %*` form. The executable is run directly.
export function parseWindowsExeShim(source) {
  for (const line of source.split(/\r?\n/)) {
    const match = line.match(/^[ \t]*@?"(?:%(?:dp0%|~dp0)\\?([^"\r\n]+\.exe)|([A-Za-z]:[\\/][^"\r\n]+\.exe))"[ \t]+%\*[ \t]*$/i);
    if (match) return match[1] ?? match[2];
  }
  return null;
}

// Accept only a single shim-relative forwarding command, optionally preceded by
// @echo off. Never evaluate batch syntax or interpolate caller-controlled argv.
function parseWindowsNestedShim(source) {
  const match = source.match(/^(?:@echo[ \t]+off[ \t]*\r?\n)?[ \t]*"%~dp0\\?([\w .@+\-\\/]+\.(?:cmd|bat))"[ \t]+%\*[ \t]*(?:\r?\n)?$/i);
  return match?.[1] ?? null;
}

function resolveWindowsNodeShim(executable, depth = 0, seen = new Set()) {
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
  const exeTarget = forwardsToBatch || jsTarget ? null : parseWindowsExeShim(source);
  const child = jsTarget ?? nested ?? exeTarget;
  if (!child) throw new Error(`cannot safely launch non-Node Windows command shim: ${normalized}`);
  const target = /^[A-Za-z]:[\\/]/.test(child)
    ? child
    : resolve(dirname(normalized), ...child.split(/[\\/]+/));
  if (!existsSync(target) || !statSync(target).isFile()) throw new Error(`Windows command shim target is missing: ${target}`);
  return jsTarget || (!nested && exeTarget) ? target : resolveWindowsNodeShim(target, depth + 1, seen);
}

export function portableInvocation(command, args, platform = process.platform, env = process.env) {
  if (platform !== "win32") return { command, args: [...args] };
  const executable = resolveWindowsCommand(command, env) ?? command;
  if (!/\.(?:cmd|bat)$/i.test(executable)) return { command: executable, args: [...args] };
  const target = resolveWindowsNodeShim(executable);
  // A shim's target is a Node script to run under this Node, or a native executable.
  return /\.exe$/i.test(target) ? { command: target, args: [...args] } : { command: process.execPath, args: [target, ...args] };
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
