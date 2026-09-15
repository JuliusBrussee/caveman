#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { cmpVersion } from "./version.mjs";
import { accessSync, constants, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { createRequire } from "node:module";
import { delimiter, dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const { portableInvocation, resolveWindowsCommand } = require("../bin/lib/portable-process.js");
const registry = JSON.parse(readFileSync(join(here, "agents.json"), "utf8"));
const args = process.argv.slice(2);
const required = new Set();
let json = false;
let allowNewer = false;
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--require" && args[i + 1]) required.add(args[++i]);
  else if (args[i] === "--all") for (const profile of registry.agents) required.add(profile.id);
  else if (args[i] === "--json") json = true;
  else if (args[i] === "--allow-newer") allowNewer = true;
  else {
    process.stderr.write(`usage: node agents/probe-installed.mjs [--require <id>] [--all] [--json] [--allow-newer]\n`);
    process.exit(2);
  }
}


function which(names, pathValue) {
  for (const name of names) {
    if (process.platform === "win32") {
      const found = resolveWindowsCommand(name, { PATH: pathValue, PATHEXT: process.env.PATHEXT });
      if (found) return found;
      continue;
    }
    const executable = (candidate) => {
      try { accessSync(candidate, constants.X_OK); return statSync(candidate).isFile(); }
      catch { return false; }
    };
    if (name.includes("/") && executable(name)) return name;
    for (const dir of String(pathValue || "").split(delimiter)) {
      const candidate = join(dir, name);
      if (executable(candidate)) return candidate;
    }
  }
  return undefined;
}

function firstVersion(text) {
  return text.match(/(?:^|[^0-9A-Za-z])v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\b|$)/m)?.[1] || "";
}

function run(binary, argv, env) {
  let invocation;
  try { invocation = portableInvocation(binary, argv, { env }); }
  catch (error) { return { ok: false, status: null, error: error.message, output: "" }; }
  const result = spawnSync(invocation.command, invocation.args, { env, encoding: "utf8", timeout: 15_000 });
  const output = `${result.stdout || ""}${result.stderr || ""}`.trim();
  return {
    ok: result.status === 0 && !result.error,
    status: result.status,
    signal: result.signal,
    error: result.error?.message,
    output,
  };
}

function probeEnvironment(home) {
  const env = {
    PATH: process.env.PATH || "",
    HOME: home,
    USERPROFILE: home,
    APPDATA: join(home, "AppData", "Roaming"),
    LOCALAPPDATA: join(home, "AppData", "Local"),
    XDG_CONFIG_HOME: join(home, ".config"),
    XDG_DATA_HOME: join(home, ".local", "share"),
    XDG_CACHE_HOME: join(home, ".cache"),
    CODEX_HOME: join(home, ".codex"),
    OPENCLAW_STATE_DIR: join(home, ".openclaw"),
    NO_COLOR: "1",
  };
  for (const key of [
    "ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP", "TMPDIR",
    "LANG", "LC_ALL", "LC_CTYPE", "SHELL", "TERM", "TZ", "CI",
  ]) {
    if (process.env[key] !== undefined) env[key] = process.env[key];
  }
  // Codex validates an explicit CODEX_HOME before loading commands. Materialize
  // the isolated home rather than pointing its help/version probe at a missing dir.
  for (const key of ["CODEX_HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"]) {
    mkdirSync(env[key], { recursive: true });
  }
  return env;
}

const unknown = [...required].filter((id) => !registry.agents.some((profile) => profile.id === id));
if (unknown.length) {
  process.stderr.write(`unknown agent profile(s): ${unknown.join(", ")}\n`);
  process.exit(2);
}

const results = [];
let failed = false;
for (const profile of registry.agents) {
  const binary = which(profile.binary_names, process.env.PATH);
  const mustExist = required.has(profile.id);
  if (!binary) {
    const result = { id: profile.id, status: mustExist ? "missing" : "not-installed", tested: profile.tested_agent_version };
    results.push(result);
    if (mustExist) failed = true;
    continue;
  }
  const isolatedHome = mkdtempSync(join(tmpdir(), `cave-probe-${profile.id}-`));
  try {
    const env = probeEnvironment(isolatedHome);
    const versionProbe = run(binary, ["--version"], env);
    const helpProbe = run(binary, ["--help"], env);
    const observed = firstVersion(versionProbe.output);
    const versionMatches = profile.tested_agent_version === "x" || observed === profile.tested_agent_version;
    const launchable = versionProbe.ok && helpProbe.ok;
    // With --allow-newer a launchable binary that is strictly newer than the pin is
    // `drift` (report it, do not fail); an older/unknown one is still `broken`.
    const isNewer = allowNewer && !versionMatches && observed && profile.tested_agent_version !== "x" && cmpVersion(observed, profile.tested_agent_version) > 0;
    let status;
    if (!launchable) status = "broken";
    else if (versionMatches) status = "ok";
    else if (isNewer) status = "drift";
    else status = "broken";
    const clean = status === "ok" || status === "drift";
    results.push({
      id: profile.id,
      status,
      binary,
      tested: profile.tested_agent_version,
      observed,
      version_ok: versionProbe.ok,
      help_ok: helpProbe.ok,
      version_matches: versionMatches,
      ...(clean ? {} : { version_error: versionProbe.error || versionProbe.output, help_error: helpProbe.error || helpProbe.output }),
    });
    if (status === "broken" && (mustExist || required.size === 0)) failed = true;
  } finally {
    rmSync(isolatedHome, { recursive: true, force: true });
  }
}

if (json) process.stdout.write(JSON.stringify({ schema: "caveman.agent-probe.v1", results }, null, 2) + "\n");
else {
  for (const result of results) {
    const detail = result.observed ? ` ${result.observed} (tested ${result.tested})` : "";
    const glyph = result.status === "ok" ? "ok" : result.status === "drift" ? "~" : result.status === "not-installed" ? "-" : "x";
    process.stdout.write(`${glyph} ${result.id}${detail}\n`);
  }
}
process.exit(failed ? 1 : 0);
