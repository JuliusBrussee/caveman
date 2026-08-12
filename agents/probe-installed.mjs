#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { delimiter, dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
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
    process.stderr.write(`usage: node public/agents/probe-installed.mjs [--require <id>] [--all] [--json] [--allow-newer]\n`);
    process.exit(2);
  }
}

// cmpSemver: -1/0/1 by dotted numeric-then-lexical segments. Not full SemVer prerelease
// ordering, but enough to tell "a working newer binary" apart from an older/unknown one.
function cmpSemver(a, b) {
  const norm = (v) => String(v).split(/[.+-]/).map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  const A = norm(a);
  const B = norm(b);
  for (let i = 0; i < Math.max(A.length, B.length); i++) {
    const x = A[i];
    const y = B[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (typeof x === "number" && typeof y === "number") {
      if (x !== y) return x < y ? -1 : 1;
    } else if (String(x) !== String(y)) {
      return String(x) < String(y) ? -1 : 1;
    }
  }
  return 0;
}

function which(names, pathValue) {
  for (const name of names) {
    if (name.includes("/") && existsSync(name)) return name;
    for (const dir of String(pathValue || "").split(delimiter)) {
      const candidate = join(dir, name);
      if (existsSync(candidate)) return candidate;
    }
  }
  return undefined;
}

function firstVersion(text) {
  return text.match(/(?:^|[^0-9A-Za-z])v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\b|$)/m)?.[1] || "";
}

function run(binary, argv, env) {
  const result = spawnSync(binary, argv, { env, encoding: "utf8", timeout: 15_000 });
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
    CODEX_HOME: join(home, ".codex"),
    OPENCLAW_STATE_DIR: join(home, ".openclaw"),
    NO_COLOR: "1",
  };
  for (const key of [
    "ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP", "TMPDIR", "USERPROFILE",
    "LANG", "LC_ALL", "LC_CTYPE", "SHELL", "TERM", "TZ", "CI",
  ]) {
    if (process.env[key] !== undefined) env[key] = process.env[key];
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
    const isNewer = allowNewer && !versionMatches && observed && profile.tested_agent_version !== "x" && cmpSemver(observed, profile.tested_agent_version) > 0;
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
