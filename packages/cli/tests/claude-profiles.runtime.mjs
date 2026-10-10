// `caveman enable claude` wires every Claude Code profile (sibling config dirs
// picked with CLAUDE_CONFIG_DIR), not only the one active in the shell.
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, existsSync, lstatSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { modulesFixture, runCli, snapshot } from "./_modules.mjs";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const ROUTE = "http://127.0.0.1:9/w/claude";

// What Claude Code itself writes into a config dir it has run in.
const MARKER = { numStartups: 1 };
const put = (path, body) => {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, typeof body === "string" ? body : JSON.stringify(body, null, 2) + "\n");
};
const json = (path) => JSON.parse(readFileSync(path, "utf8"));
const routed = (dir) => existsSync(join(dir, "settings.json")) && json(join(dir, "settings.json")).env?.ANTHROPIC_BASE_URL === ROUTE;
const hasMcp = (file) => existsSync(file) && Boolean(json(file).mcpServers?.caveman);
const doctor = async (env) => JSON.parse((await runCli(["doctor", "claude"], env)).stdout);
const journal = (home) => json(join(home, ".caveman", "integrations", "claude.json"));
// Every profile file under HOME, path → bytes. The voice skills an explicit
// enable writes into the active profile are the user's afterwards and stay.
const profileFiles = (home) => Object.fromEntries(Object.entries(snapshot(home))
  .filter(([path]) => path.startsWith(".claude") && !path.split(/[\\/]/).includes("skills")));

// ~/.claude (default), ~/.claude-max20 (active in this shell) and ~/.claude-work
// (a login with no settings yet); ~/.claude-foo is not a Claude Code config dir.
function threeProfiles() {
  const fx = modulesFixture({ agents: ["claude"] });
  const dirs = { main: join(fx.home, ".claude"), max: join(fx.home, ".claude-max20"), work: join(fx.home, ".claude-work"), foo: join(fx.home, ".claude-foo") };
  put(join(dirs.main, "settings.json"), { theme: "dark" });
  put(join(fx.home, ".claude.json"), { mcpServers: { mine: { command: "x" } } });
  put(join(dirs.max, "settings.json"), '{\n  "env": { "KEEP": "yes" }\n}\n');
  put(join(dirs.work, ".credentials.json"), {});
  put(join(dirs.foo, "notes.txt"), "not a profile\n");
  return { ...fx, dirs, env: { ...fx.env, CLAUDE_CONFIG_DIR: dirs.max } };
}

test("one enable wires every profile, a non-profile dir is ignored, and disable restores each byte for byte", async () => {
  const fx = threeProfiles();
  try {
    const before = profileFiles(fx.home);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(routed(dir), `${dir} routed`);
    assert.ok(hasMcp(join(fx.home, ".claude.json")), "the default profile keeps its MCP config beside ~/.claude");
    assert.ok(hasMcp(join(fx.dirs.max, ".claude.json")));
    assert.ok(hasMcp(join(fx.dirs.work, ".claude.json")));
    assert.equal(json(join(fx.dirs.main, "settings.json")).theme, "dark");
    assert.equal(json(join(fx.dirs.max, "settings.json")).env.KEEP, "yes");
    assert.deepEqual(Object.keys(profileFiles(fx.home)).filter((path) => path.startsWith(".claude-foo")), [join(".claude-foo", "notes.txt")]);
    assert.equal(journal(fx.home).operations[0].file, join(fx.dirs.max, "settings.json"), "the active profile comes first");
    assert.equal(journal(fx.home).operations.length, 6);
    const status = await doctor(fx.env);
    assert.equal(status.state, "installed");
    assert.deepEqual(status.unwired_profiles, []);
    // Another profile's shell sees the same whole install.
    assert.equal((await doctor({ ...fx.env, CLAUDE_CONFIG_DIR: fx.dirs.work })).state, "installed");
    const again = await runCli(["enable", "claude"], { ...fx.env, CLAUDE_CONFIG_DIR: fx.dirs.work });
    assert.match(again.stderr, /already enabled/);

    const off = await runCli(["disable", "claude"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.deepEqual(profileFiles(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("a profile created after enable reads as not whole until the next enable wires it", async () => {
  const fx = threeProfiles();
  try {
    assert.equal((await runCli(["enable", "claude"], fx.env)).code, 0);
    const late = join(fx.home, ".claude-late");
    put(join(late, "settings.json"), { theme: "light" });
    put(join(late, ".claude.json"), MARKER);
    const status = await doctor(fx.env);
    assert.equal(status.state, "degraded");
    assert.deepEqual(status.unwired_profiles, [late]);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.doesNotMatch(out.stderr, /already enabled/);
    assert.ok(routed(late));
    assert.ok(hasMcp(join(late, ".claude.json")));
    assert.equal(json(join(late, "settings.json")).theme, "light");
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(routed(dir), `${dir} still routed`);
    assert.equal((await doctor(fx.env)).state, "installed");
  } finally {
    fx.cleanup();
  }
});

test("the quiet module path wires a profile the journal does not cover", async () => {
  const fx = threeProfiles();
  try {
    assert.equal((await runCli(["enable", "claude"], fx.env)).code, 0);
    const late = join(fx.home, ".claude-late");
    put(join(late, ".credentials.json"), {});
    // `doctor --fix` and `caveman claude`'s own repair take the same branch.
    const out = await runCli(["doctor", "claude", "--fix"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(out.stdout).fix.result, "repaired");
    assert.ok(routed(late));
  } finally {
    fx.cleanup();
  }
});

test("setup run again wires a login that appeared since, and its first lines name every login", async () => {
  const fx = threeProfiles();
  try {
    const first = await runCli(["setup", "--yes"], fx.env);
    assert.equal(first.code, 0, first.stderr + first.stdout);
    assert.match(first.stdout, /Claude logins +~[\\/]\.claude-max20 · ~[\\/]\.claude · ~[\\/]\.claude-work\n/);
    const late = join(fx.home, ".claude-late");
    put(join(late, "settings.json"), {});
    put(join(late, ".claude.json"), MARKER);
    const dry = await runCli(["setup", "--dry-run"], fx.env);
    assert.match(dry.stdout, /wire the new claude login/);
    assert.equal(routed(late), false, "a dry run writes nothing");
    const again = await runCli(["setup", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr + again.stdout);
    assert.ok(routed(late), again.stdout);
    assert.deepEqual((await doctor(fx.env)).unwired_profiles, []);
  } finally {
    fx.cleanup();
  }
});

test("a malformed profile is skipped with a warning; a malformed active profile still fails", async () => {
  const fx = threeProfiles();
  try {
    const bad = join(fx.home, ".claude-bad", "settings.json");
    put(bad, "[]\n");
    put(join(dirname(bad), ".claude.json"), MARKER);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /claude profile \S*\.claude-bad skipped: \S+/);
    assert.equal(readFileSync(bad, "utf8"), "[]\n");
    assert.deepEqual(json(join(fx.home, ".claude-bad", ".claude.json")), MARKER);
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(routed(dir), `${dir} routed`);
    assert.equal((await doctor(fx.env)).state, "installed", "a profile enable skips does not degrade the install");
    // Disable reads every discovered profile. One it never wrote and cannot
    // read holds no hook Claude Code runs: it is named and left as is.
    const off = await runCli(["disable", "claude"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.match(off.stderr, /left \S*\.claude-bad\/settings\.json as is: it is not a JSON object/);
    assert.equal(readFileSync(bad, "utf8"), "[]\n");
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(!routed(dir), `${dir} unrouted`);

    const before = profileFiles(fx.home);
    const loud = await runCli(["enable", "claude"], { ...fx.env, CLAUDE_CONFIG_DIR: dirname(bad) });
    assert.notEqual(loud.code, 0);
    assert.match(loud.stderr, /is not a JSON object/);
    assert.deepEqual(profileFiles(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

// What login and logout run once the credential is stored or gone.
const syncAuto = (env) => new Promise((resolve, reject) => {
  const child = spawn(process.execPath, ["--input-type=module", "-e", `const cli = await import(${JSON.stringify(pathToFileURL(cli).href)}); cli.syncAutoEntries();`], { env });
  let stderr = "";
  child.stderr.on("data", (chunk) => (stderr += chunk));
  child.on("exit", (code) => resolve({ code, stderr }));
  child.on("error", reject);
});

test("Auto comes and goes in every wired profile", async () => {
  const fx = threeProfiles();
  try {
    const dirs = [fx.dirs.main, fx.dirs.max, fx.dirs.work];
    const option = (dir) => json(join(dir, "settings.json")).env?.ANTHROPIC_CUSTOM_MODEL_OPTION;
    const cloud = join(fx.home, ".caveman", "cloud.json");
    assert.equal((await runCli(["enable", "claude"], fx.env)).code, 0);
    for (const dir of dirs) assert.equal(option(dir), undefined, `${dir} starts without Auto`);

    put(cloud, { modules: { routing: true }, baseURL: "https://api.caveman.so", tokenStore: "file" });
    let out = await syncAuto(fx.env);
    assert.equal(out.code, 0, out.stderr);
    for (const dir of dirs) assert.equal(option(dir), "caveman-auto[1m]", `${dir} gets Auto`);

    put(cloud, { modules: { routing: true } });
    out = await syncAuto(fx.env);
    assert.equal(out.code, 0, out.stderr);
    for (const dir of dirs) assert.equal(option(dir), undefined, `${dir} loses Auto`);
    assert.equal((await runCli(["disable", "claude"], fx.env)).code, 0, "the journal still reverses cleanly");
    for (const dir of dirs) assert.ok(!routed(dir));
  } finally {
    fx.cleanup();
  }
});

test("a journal from before profiles (one settings op, one MCP op) still disables cleanly", async () => {
  const fx = modulesFixture({ agents: ["claude"] });
  try {
    const settings = join(fx.home, ".claude", "settings.json");
    put(settings, { theme: "dark" });
    const before = profileFiles(fx.home);
    assert.equal((await runCli(["enable", "claude"], fx.env)).code, 0);
    assert.deepEqual(journal(fx.home).operations.map((operation) => operation.kind), ["claude-settings", "claude-mcp"]);
    // A second login appears; the old journal knows nothing of it.
    const work = join(fx.home, ".claude-work");
    put(join(work, "settings.json"), { theme: "light" });
    put(join(work, ".claude.json"), MARKER);
    const withWork = { ...before, ...Object.fromEntries(Object.entries(profileFiles(fx.home)).filter(([path]) => path.startsWith(".claude-work"))) };
    assert.deepEqual((await doctor(fx.env)).unwired_profiles, [work]);
    const off = await runCli(["disable", "claude"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.deepEqual(profileFiles(fx.home), withWork);
  } finally {
    fx.cleanup();
  }
});

test("a ~/.claude-* dir that is not a Claude Code login is left alone", async () => {
  const fx = threeProfiles();
  try {
    // A plugin's own dir: settings and projects, nothing Claude Code wrote.
    const mem = join(fx.home, ".claude-mem");
    put(join(mem, "settings.json"), { model: "x" });
    put(join(mem, "projects", "a.json"), {});
    put(join(mem, ".claude.json"), { theirs: true });
    const before = profileFiles(fx.home);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    const after = profileFiles(fx.home);
    for (const path of Object.keys(before).filter((path) => path.startsWith(".claude-mem"))) assert.equal(after[path], before[path], path);
    assert.equal((await doctor(fx.env)).state, "installed");
  } finally {
    fx.cleanup();
  }
});

test("another profile that routes elsewhere on purpose keeps its route", async () => {
  const fx = threeProfiles();
  try {
    const glm = join(fx.home, ".claude-glm");
    put(join(glm, ".claude.json"), MARKER);
    put(join(glm, "settings.json"), { env: { ANTHROPIC_BASE_URL: "https://api.z.ai/api/anthropic", ANTHROPIC_AUTH_TOKEN: "theirs" } });
    const aws = join(fx.home, ".claude-aws");
    put(join(aws, ".claude.json"), MARKER);
    put(join(aws, "settings.json"), { env: { CLAUDE_CODE_USE_BEDROCK: "1" } });
    const before = profileFiles(fx.home);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /claude profile \S*\.claude-glm skipped: \S+/);
    const after = profileFiles(fx.home);
    for (const path of Object.keys(after).filter((path) => /^\.claude-(glm|aws)/.test(path))) assert.equal(after[path], before[path], path);
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(routed(dir), `${dir} routed`);
    const status = await doctor(fx.env);
    assert.equal(status.state, "installed");
    assert.deepEqual(status.unwired_profiles, []);
    // The active profile keeps its own endpoint too: the proxy would send its
    // token to api.anthropic.com. The refusal names the endpoint and the opt-in.
    assert.equal((await runCli(["disable", "claude"], fx.env)).code, 0);
    const glmBefore = profileFiles(fx.home);
    const active = await runCli(["enable", "claude"], { ...fx.env, CLAUDE_CONFIG_DIR: glm });
    assert.notEqual(active.code, 0);
    assert.match(active.stderr, /sends its requests to its own endpoint https:\/\/api\.z\.ai\/api\/anthropic \(ANTHROPIC_BASE_URL in \S*\.claude-glm\/settings\.json\)/);
    assert.match(active.stderr, /remove ANTHROPIC_BASE_URL there and run `caveman enable claude`/);
    assert.deepEqual(profileFiles(fx.home), glmBefore);
    assert.equal(existsSync(join(fx.home, ".caveman", "integrations", "claude.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("a wired profile deleted afterwards does not strand the others", async () => {
  const fx = threeProfiles();
  try {
    const old = join(fx.home, ".claude-old");
    put(join(old, ".claude.json"), MARKER);
    put(join(old, "settings.json"), { theme: "old" });
    const before = profileFiles(fx.home);
    assert.equal((await runCli(["enable", "claude"], fx.env)).code, 0);
    assert.ok(routed(old));
    rmSync(old, { recursive: true });
    assert.equal((await doctor(fx.env)).state, "installed");
    const fixed = await runCli(["doctor", "claude", "--fix"], fx.env);
    assert.equal(fixed.code, 0, fixed.stderr);
    const again = await runCli(["enable", "claude"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    // A new login makes the next enable re-wire; the deleted one is dropped.
    const late = join(fx.home, ".claude-late");
    put(join(late, ".claude.json"), MARKER);
    const more = await runCli(["enable", "claude"], fx.env);
    assert.equal(more.code, 0, more.stderr);
    assert.ok(routed(late));
    assert.ok(!journal(fx.home).operations.some((operation) => operation.file.includes(".claude-old")));
    assert.ok(!existsSync(old));
    rmSync(late, { recursive: true });
    const off = await runCli(["disable", "claude"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    const rest = Object.fromEntries(Object.entries(before).filter(([path]) => !path.startsWith(".claude-old")));
    assert.deepEqual(profileFiles(fx.home), rest);
  } finally {
    fx.cleanup();
  }
});

test("a profile that cannot be written is skipped and the rest are wired", { skip: process.platform === "win32" || process.getuid?.() === 0 }, async () => {
  const fx = threeProfiles();
  const ro = join(fx.home, ".claude-ro");
  try {
    put(join(ro, ".claude.json"), MARKER);
    put(join(ro, "settings.json"), { theme: "ro" });
    const before = profileFiles(fx.home);
    chmodSync(ro, 0o500);
    const out = await runCli(["enable", "claude"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stderr, /claude profile \S*\.claude-ro skipped: \S+/);
    for (const dir of [fx.dirs.main, fx.dirs.max, fx.dirs.work]) assert.ok(routed(dir), `${dir} routed`);
    const after = profileFiles(fx.home);
    for (const path of Object.keys(after).filter((path) => path.startsWith(".claude-ro"))) assert.equal(after[path], before[path], path);
    assert.ok(!journal(fx.home).operations.some((operation) => operation.file.includes(".claude-ro")));
    assert.equal((await doctor(fx.env)).state, "installed");
    const off = await runCli(["disable", "claude"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.deepEqual(profileFiles(fx.home), before);
  } finally {
    try { chmodSync(ro, 0o700); } catch { /* never created */ }
    fx.cleanup();
  }
});

test("a settings.json linked to another profile's stays a link and is wired once", { skip: process.platform === "win32" }, async () => {
  const fx = threeProfiles();
  try {
    const b = join(fx.home, ".claude-b");
    const link = join(b, "settings.json");
    put(join(b, ".claude.json"), MARKER);
    symlinkSync(join(fx.dirs.main, "settings.json"), link);
    const env = { ...fx.env, CLAUDE_CONFIG_DIR: b };
    const before = profileFiles(fx.home);
    const out = await runCli(["enable", "claude"], env);
    assert.equal(out.code, 0, out.stderr);
    assert.ok(lstatSync(link).isSymbolicLink(), "the link survives enable");
    assert.ok(routed(fx.dirs.main));
    assert.equal(journal(fx.home).operations.filter((operation) => operation.kind === "claude-settings" && /\.claude(-b)?[\\/]settings\.json$/.test(operation.file)).length, 1);
    assert.ok(hasMcp(join(b, ".claude.json")));
    assert.ok(hasMcp(join(fx.home, ".claude.json")));
    assert.equal((await doctor(env)).state, "installed");
    const off = await runCli(["disable", "claude"], env);
    assert.equal(off.code, 0, off.stderr);
    assert.ok(lstatSync(link).isSymbolicLink(), "the link survives disable");
    assert.deepEqual(profileFiles(fx.home), before);
  } finally {
    fx.cleanup();
  }
});
