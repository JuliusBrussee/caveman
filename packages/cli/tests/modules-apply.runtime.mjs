import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { harness, modulesFixture, runCli, snapshot } from "./_modules.mjs";

const planLines = (stdout) => stdout.split("\n").filter((line) => /^ {2}[A-Z]+ /.test(line)).map((line) => line.trim().split(/\s{2,}/));

test("fresh home: on --all --dry-run prints the whole plan and writes nothing", async () => {
  const fx = modulesFixture({ binaries: false });
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["on", "--all", "--dry-run"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^This will\n/);
    assert.deepEqual(planLines(out.stdout).map((line) => line.join(" | ").replace(/bin-v[\d.]+/, "bin-vX")), [
      "DOWNLOAD | caveman-proxy, caveman-mcp, caveman-engine, cavemem, caveman-shrink, caveman-browse | signed, bin-vX",
      "CREATE | ~/.caveman/cloud.json | modules on: output, input, waste-fixes, routing, scripts, browse",
      "CREATE | ~/.claude/settings.json | claude settings",
      "CREATE | ~/.claude.json | claude mcp",
      "CREATE | ~/.codex/hooks.json | codex hooks",
      "CREATE | ~/.codex/config.toml | codex config",
      "RUN | caveman-proxy | start local runtime",
      "RUN | caveman-blocks hooks install | skipped: caveman-blocks not installed",
    ]);
    assert.deepEqual(snapshot(fx.home), before, "--dry-run wrote to HOME");
  } finally {
    fx.cleanup();
  }
});

test("without --yes and without a terminal nothing is applied", async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["on", "--all"], fx.env);
    assert.equal(out.code, 1);
    assert.match(out.stderr, /nothing changed: pass --yes/);
    assert.deepEqual(snapshot(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("on → off → on round-trips harness files byte for byte", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    mkdirSync(join(fx.home, ".claude"), { recursive: true });
    writeFileSync(join(fx.home, ".claude", "settings.json"), JSON.stringify({ env: { KEEP: "yes" }, theme: "dark" }, null, 2) + "\n");
    mkdirSync(join(fx.home, ".codex"), { recursive: true });
    writeFileSync(join(fx.home, ".codex", "config.toml"), 'model = "gpt-5"\n');
    const original = harness(fx.home);

    const on = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(on.code, 0, on.stderr);
    const wired = harness(fx.home);
    assert.match(wired[".claude/settings.json"], /"ANTHROPIC_BASE_URL": "http:\/\/127\.0\.0\.1:9\/w\/claude"/);
    assert.match(wired[".claude/settings.json"], /"KEEP": "yes"/);
    assert.match(wired[".codex/config.toml"], /caveman:native-root/);
    assert.equal(readFileSync(join(fx.home, "blocks.log"), "utf8"), "install\n");

    const off = await runCli(["off", "--all", "--yes"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.deepEqual(harness(fx.home), original, "off --all left harness bytes behind");
    assert.equal(readFileSync(join(fx.home, "blocks.log"), "utf8"), "install\nuninstall\n");
    const config = JSON.parse(readFileSync(join(fx.home, ".caveman", "cloud.json"), "utf8"));
    assert.deepEqual(config.modules, { output: false, input: false, "waste-fixes": false, routing: false, scripts: false, browse: false });
    assert.deepEqual(config.think, { core: false, mode: "record", toon: false, shrink: false });
    assert.equal(config.execute.browse_tool, false);
    assert.equal(config.learnAutopilot, false);

    const again = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    assert.deepEqual(harness(fx.home), wired, "second on wrote different bytes");

    const idle = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(idle.code, 0, idle.stderr);
    assert.match(idle.stdout, /✓ every module already on/);
  } finally {
    fx.cleanup();
  }
});

test("a failed Blocks install prints Blocks' full output", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    writeFileSync(join(fx.home, ".blocks-fail"), "");
    const out = await runCli(["on", "scripts", "--yes"], fx.env);
    assert.equal(out.code, 1);
    assert.match(out.stderr, /^✗ caveman-blocks hooks install failed\nbinary: copied\ncodex: cannot write ~\/\.codex\/hooks\.json: permission denied$/m);
  } finally {
    fx.cleanup();
  }
});

// rc.1 and older answer --json with "unknown flag" and exit 2. Such a copy on
// PATH is passed over for the signed one; when that cannot download, nothing
// claims scripts is ready and every surface says what to do.
test("a Blocks older than rc.2 on PATH is never called ready", async () => {
  const fx = modulesFixture();
  const env = { ...fx.env };
  delete env.CAVEMAN_BLOCKS_BIN;
  try {
    writeFileSync(join(fx.bin, "caveman-blocks"), "#!/bin/sh\ncase \"$*\" in *--json*) echo 'caveman-blocks: unknown flag: --json' >&2; exit 2 ;; esac\necho ok\n", { mode: 0o755 });
    const old = "~/bin/caveman-blocks is older than Blocks rc.2 · update or remove it";
    const on = await runCli(["on", "scripts", "--yes"], env);
    assert.match(on.stdout, /^ {2}DOWNLOAD +caveman-blocks +signed, if bin-\S+ carries it$/m, "the plan never promises a Blocks download the release may not carry");
    assert.doesNotMatch(on.stdout, /scripts ready/);
    assert.ok(on.stdout.includes(`○ scripts: ${old}\n`), on.stdout);
    const scripts = JSON.parse((await runCli(["status", "--json"], env)).stdout).modules.find((state) => state.id === "scripts");
    assert.deepEqual([scripts.active, scripts.reason], [false, old]);
    const doctor = await runCli(["doctor"], env);
    assert.ok(doctor.stdout.includes(`✗ scripts: ${old}, or caveman off scripts\n`), doctor.stdout);
  } finally {
    fx.cleanup();
  }
});

test("turning scripts off on a fresh hub config leaves hooks Blocks' own installer wrote", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    writeFileSync(join(fx.home, ".blocks-claude-code"), "");
    const out = await runCli(["setup", "--yes", "--skip", "scripts"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.doesNotMatch(out.stdout, /hooks uninstall/);
    assert.ok(existsSync(join(fx.home, ".blocks-claude-code")), "the user's own hook was removed");
    assert.equal(existsSync(join(fx.home, "blocks.log")), false);
  } finally {
    fx.cleanup();
  }
});

test("Blocks hooks only the selected agents, and an agent wired later gets its hook in that run", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    mkdirSync(join(fx.home, ".codex"), { recursive: true });
    const first = await runCli(["setup", "--yes", "--only", "scripts", "--agents", "claude"], fx.env);
    assert.equal(first.code, 0, first.stderr);
    assert.match(first.stdout, /^ {2}RUN +caveman-blocks hooks install +Claude Code$/m);
    assert.match(first.stdout, /^✓ scripts ready \(Claude Code\)$/m);
    assert.doesNotMatch(first.stdout, /Codex asks once/);
    assert.equal(existsSync(join(fx.home, ".blocks-codex")), false, "Codex was hooked without being selected");

    // scripts is not named, but output wires Codex in this run.
    const output = await runCli(["on", "output", "--yes"], fx.env);
    assert.equal(output.code, 0, output.stderr);
    assert.match(output.stdout, /^✓ Codex wired\n(?:.*\n)*✓ scripts ready \(Claude Code, Codex\)\n {2}Codex asks once/m);
    assert.ok(existsSync(join(fx.home, ".blocks-codex")));
  } finally {
    fx.cleanup();
  }
});

test("scripts with only agents Blocks cannot hook says it stays idle", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    writeFileSync(join(fx.bin, "gemini"), "#!/bin/sh\necho gemini\n", { mode: 0o755 });
    const out = await runCli(["setup", "--yes", "--only", "scripts", "--agents", "gemini"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^note: scripts stays idle: none of the selected agents takes its hook \(Claude Code, Codex, opencode\)$/m, out.stdout);
    assert.doesNotMatch(out.stdout, /scripts ready/);
  } finally {
    fx.cleanup();
  }
});

test("off keeps agent wiring while another module needs it", async () => {
  const fx = modulesFixture({ agents: ["claude"], blocks: true });
  const journal = () => existsSync(join(fx.home, ".caveman", "integrations", "claude.json"));
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    assert.ok(journal());

    const input = await runCli(["off", "input", "--yes"], fx.env);
    assert.equal(input.code, 0, input.stderr);
    assert.deepEqual(planLines(input.stdout), [
      ["UPDATE", "~/.caveman/cloud.json", "modules off: input · think.mode = record · think.toon = false · think.shrink = false"],
      ["UPDATE", "~/.claude/settings.json", "refresh claude hooks"],
      ["UPDATE", "~/.claude.json", "refresh claude hooks"],
    ]);
    assert.match(input.stdout, /^note: output also pauses: it runs through input$/m);
    assert.ok(journal(), "off input removed wiring output still needs");
    assert.doesNotMatch(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8"), /shrink-hook/);
    // The refreshed hooks match think.shrink, and output pausing is a choice.
    const doctor = await runCli(["doctor"], fx.env);
    assert.equal(doctor.code, 0, doctor.stdout);
    assert.match(doctor.stdout, /^· output: paused while input is off · caveman on input$/m);

    assert.equal((await runCli(["off", "output", "waste-fixes", "--yes"], fx.env)).code, 0);
    assert.ok(journal(), "wiring must stay while routing is on");

    const before = snapshot(fx.home);
    const dry = await runCli(["off", "routing", "--dry-run"], fx.env);
    assert.equal(dry.code, 0, dry.stderr);
    assert.deepEqual(planLines(dry.stdout), [
      ["UPDATE", "~/.caveman/cloud.json", "modules off: routing"],
      ["UPDATE", "~/.claude/settings.json", "remove claude wiring"],
      ["UPDATE", "~/.claude.json", "remove claude wiring"],
    ]);
    assert.deepEqual(snapshot(fx.home), before, "--dry-run wrote to HOME");

    const last = await runCli(["off", "routing", "--yes"], fx.env);
    assert.equal(last.code, 0, last.stderr);
    assert.equal(journal(), false, "last agent-wired module off must unwire");
    assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("on/off reject unknown modules and bad flags", async () => {
  const fx = modulesFixture();
  try {
    const unknown = await runCli(["on", "nope"], fx.env);
    assert.equal(unknown.code, 2);
    assert.match(unknown.stderr, /unknown module: nope · modules: output, input, waste-fixes, routing, scripts, browse/);
    assert.equal((await runCli(["off"], fx.env)).code, 2);
    assert.equal((await runCli(["on", "--all", "output"], fx.env)).code, 2);
    assert.equal((await runCli(["on", "output", "--force"], fx.env)).code, 2);
  } finally {
    fx.cleanup();
  }
});

test("on <module> touches only that module: its key, its binary, no harness files", async () => {
  const missing = modulesFixture({ binaries: false });
  try {
    const dry = await runCli(["on", "browse", "--dry-run"], missing.env);
    assert.equal(dry.code, 0, dry.stderr);
    assert.deepEqual(planLines(dry.stdout).map((line) => line.join(" | ").replace(/bin-v[\d.]+/, "bin-vX")), [
      "DOWNLOAD | caveman-browse | signed, bin-vX",
      "CREATE | ~/.caveman/cloud.json | modules on: browse",
    ]);
  } finally {
    missing.cleanup();
  }

  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["on", "browse", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^✓ browse on$/m);
    const after = snapshot(fx.home);
    const changed = Object.keys(after).filter((file) => after[file] !== before[file]);
    assert.deepEqual(changed, [".caveman/cloud.json"]);
    assert.deepEqual(JSON.parse(readFileSync(join(fx.home, ".caveman", "cloud.json"), "utf8")), { modules: { browse: true } });

    // A missing external binary is said in the plan and after apply, not skipped silently.
    const scripts = await runCli(["on", "scripts", "--yes"], fx.env);
    assert.equal(scripts.code, 0, scripts.stderr);
    assert.match(scripts.stdout, /RUN +caveman-blocks hooks install +skipped: caveman-blocks not installed/);
    assert.match(scripts.stdout, /^✓ scripts on · caveman-blocks not installed$/m);
  } finally {
    fx.cleanup();
  }
});

test("off <module> leaves the user's other config keys alone and reads old configs as off", async () => {
  const fx = modulesFixture();
  try {
    // Written where a pre-v4 CLI kept it; the first read copies it to ~/.caveman/cloud.json.
    mkdirSync(join(fx.home, ".caveman-cloud"), { recursive: true });
    writeFileSync(join(fx.home, ".caveman-cloud", "config.json"), JSON.stringify({ think: { mode: "record" }, execute: { browse_tool: false } }));
    const configPath = join(fx.home, ".caveman", "cloud.json");
    const out = await runCli(["off", "scripts", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.deepEqual(planLines(out.stdout), [["UPDATE", "~/.caveman/cloud.json", "modules off: scripts"]]);
    assert.deepEqual(JSON.parse(readFileSync(configPath, "utf8")), {
      think: { mode: "record" }, execute: { browse_tool: false }, modules: { scripts: false },
    });
    const modules = JSON.parse((await runCli(["status", "--json"], fx.env)).stdout).modules;
    assert.deepEqual(modules.filter((state) => !state.on).map((state) => state.id), ["input", "scripts", "browse"]);
  } finally {
    fx.cleanup();
  }
});

test("an invalid value leaves its module inactive, fails doctor, and on rewrites it", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    mkdirSync(join(fx.home, ".caveman-cloud"), { recursive: true });
    writeFileSync(join(fx.home, ".caveman-cloud", "config.json"), JSON.stringify({ think: { mode: "comprss" } }));
    const input = JSON.parse((await runCli(["status", "--json"], fx.env)).stdout).modules.find((state) => state.id === "input");
    assert.equal(input.on, true);
    assert.equal(input.active, false);
    assert.equal(input.reason, "think.mode has an invalid value: comprss");
    const doctor = await runCli(["doctor"], fx.env);
    assert.equal(doctor.code, 1);
    assert.match(doctor.stdout, /^✗ think.mode has an invalid value: comprss · fix: caveman on input$/m);
    const plan = await runCli(["on", "input", "--dry-run"], fx.env);
    assert.match(plan.stdout, /think\.mode = compress/);
  } finally {
    fx.cleanup();
  }
});

test("a project or env override shows the module on but inactive, with the reason", async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    const project = join(fx.home, "project");
    mkdirSync(join(project, ".caveman"), { recursive: true });
    writeFileSync(join(project, ".caveman", "config.json"), JSON.stringify({ execute: { browse_tool: false } }));
    const out = await runCli(["status", "--json"], { ...fx.env, CAVEMAN_CORE: "0" }, { cwd: project });
    const modules = JSON.parse(out.stdout).modules;
    assert.deepEqual(modules.find((state) => state.id === "browse"), {
      id: "browse", on: true, active: false, reason: "overridden by project config", perAgent: { claude: "n/a", codex: "n/a" },
    });
    assert.equal(modules.find((state) => state.id === "output").reason, "overridden by CAVEMAN_CORE");
    const doctor = await runCli(["doctor"], { ...fx.env, CAVEMAN_CORE: "0" }, { cwd: project });
    assert.equal(doctor.code, 0, doctor.stdout);
    assert.match(doctor.stdout, /^· browse: overridden by project config$/m);
  } finally {
    fx.cleanup();
  }
});

// Agent wiring is machine-wide, so it follows the global think.shrink: a
// project that turns shrink off for itself must not strip the global hook
// when `on`/`off` runs inside it.
test("on and off inside a repo with project think.shrink=false keep the global shrink hook", async () => {
  const fx = modulesFixture();
  const project = join(fx.home, "repo");
  mkdirSync(join(project, ".caveman"), { recursive: true });
  writeFileSync(join(project, ".caveman", "config.json"), JSON.stringify({ think: { shrink: false } }));
  try {
    const on = await runCli(["on", "--all", "--yes"], fx.env, { cwd: project });
    assert.equal(on.code, 0, on.stderr);
    assert.match(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8"), /shrink-hook/);
    assert.equal((await runCli(["off", "input", "--yes"], fx.env, { cwd: project })).code, 0);
    const back = await runCli(["on", "input", "--yes"], fx.env, { cwd: project });
    assert.equal(back.code, 0, back.stderr);
    assert.match(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8"), /shrink-hook/, "the project value never reaches global wiring");
  } finally {
    fx.cleanup();
  }
});

test("an old config with only think.toon or think.shrink off still reads input as on", async () => {
  const fx = modulesFixture();
  try {
    mkdirSync(join(fx.home, ".caveman-cloud"), { recursive: true });
    writeFileSync(join(fx.home, ".caveman-cloud", "config.json"), JSON.stringify({ think: { toon: false, shrink: false } }));
    const tuned = JSON.parse((await runCli(["status", "--json"], fx.env)).stdout).modules.find((state) => state.id === "input");
    assert.equal(tuned.on, true, "input follows its primary key think.mode only");
    writeFileSync(join(fx.home, ".caveman", "cloud.json"), JSON.stringify({ think: { mode: "record" } }));
    const off = JSON.parse((await runCli(["status", "--json"], fx.env)).stdout).modules.find((state) => state.id === "input");
    assert.equal(off.on, false, "think.mode=record is input off");
  } finally {
    fx.cleanup();
  }
});

test("on and off report one line per step; enable's own report stays out", async () => {
  const fx = modulesFixture();
  try {
    const on = await runCli(["on", "--all", "--yes"], fx.env);
    assert.equal(on.code, 0, on.stderr);
    assert.match(on.stdout, /^✓ Claude Code wired\n✓ Codex wired\n/m);
    assert.match(on.stdout, /^○ scripts: caveman-blocks not installed yet$/m);
    assert.doesNotMatch(on.stderr, /planned user-scoped writes|native Caveman enabled|→ /);
    const off = await runCli(["off", "--all", "--yes"], fx.env);
    assert.equal(off.code, 0, off.stderr);
    assert.match(off.stdout, /^✓ Claude Code unwired\n✓ Codex unwired$/m);
    assert.doesNotMatch(off.stderr, /Caveman disabled/);
  } finally {
    fx.cleanup();
  }
});

// A deliberate setting under a module that stays on is left alone: only a
// state change (or a primary key that contradicts the state) rewrites keys.
test("setup and on keep think.toon=false under input on; off then on restores it", async () => {
  const fx = modulesFixture();
  const config = () => JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8"));
  try {
    mkdirSync(fx.env.CAVEMAN_HOME, { recursive: true });
    writeFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), JSON.stringify({ think: { toon: false } }));
    const setup = await runCli(["setup", "--yes"], fx.env);
    assert.equal(setup.code, 0, setup.stderr);
    assert.doesNotMatch(setup.stdout, /think\.toon/, "no plan line for a key whose module stays on");
    assert.equal(config().think.toon, false);
    assert.equal(config().modules.input, true);

    const on = await runCli(["on", "input", "--yes"], fx.env);
    assert.equal(on.code, 0, on.stderr);
    assert.match(on.stdout, /^✓ input already on$/m);
    assert.equal(config().think.toon, false);

    assert.equal((await runCli(["off", "input", "--yes"], fx.env)).code, 0);
    assert.equal(config().think.mode, "record");
    const back = await runCli(["on", "input", "--yes"], fx.env);
    assert.equal(back.code, 0, back.stderr);
    assert.match(back.stdout, /think\.toon = true/, "a state change applies every on value");
    assert.equal(config().think.toon, true);
    assert.equal(config().think.mode, "compress");
  } finally {
    fx.cleanup();
  }
});

test("a primary key that contradicts the module state is reconciled", async () => {
  const fx = modulesFixture();
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    // Switched off by hand under the module's back: input says on, think.mode says record.
    const path = join(fx.env.CAVEMAN_HOME, "cloud.json");
    const doc = JSON.parse(readFileSync(path, "utf8"));
    writeFileSync(path, JSON.stringify({ ...doc, think: { ...doc.think, mode: "record" } }));
    const plan = await runCli(["on", "input", "--dry-run"], fx.env);
    assert.match(plan.stdout, /think\.mode = compress/);
  } finally {
    fx.cleanup();
  }
});

