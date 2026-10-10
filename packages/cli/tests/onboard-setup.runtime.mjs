import { test } from "node:test";
import { createServer } from "node:net";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isolatedCliEnv, runCli as runIsolated } from "./_cli.mjs";
import { modulesFixture, runCli, snapshot } from "./_modules.mjs";

// Non-interactive first run (no TTY, or CI): the plan is printed, and only
// --yes applies it — every module on, every detected agent, no sign-in prompt. Module state
// lands in $CAVEMAN_HOME/cloud.json. The fixture stubs every module binary, so
// nothing downloads.
const skip = process.platform === "win32" ? "shell agent stubs" : false;
const here = dirname(fileURLToPath(import.meta.url));

function modules(fx) {
  return JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8")).modules;
}

test("setup --yes on a fresh home turns every module on for the detected agents", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^caveman · make your coding agent cheaper\n\nFound Claude Code and Codex\n/);
    assert.match(out.stdout, /\nModules {2}output · input · waste fixes · routing · scripts · browse\nAgents {3}Claude Code · Codex\n/);
    assert.match(out.stdout, /\nThis will\n(?: {2}[A-Z]+ .*\n)*  CREATE +~\/\.caveman\/cloud\.json +modules on: output, input, waste-fixes, routing, scripts, browse\n/, "the plan is printed");
    assert.match(out.stdout, /\n {2}CREATE +~\/\.claude\/settings\.json +claude settings\n/);
    assert.match(out.stdout, /\n✓ Claude Code wired\n✓ Codex wired\n/, "one progress line per step");
    assert.doesNotMatch(out.stderr, /planned user-scoped writes|native Caveman enabled/, "enable's own report stays out of the first run");
    assert.match(out.stdout, /routing is on and starts after you sign in · caveman login/, "no sign-in prompt without a terminal");
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude {6}See it: {2}caveman status\n/);
    assert.deepEqual(modules(fx), { output: true, input: true, "waste-fixes": true, routing: true, scripts: true, browse: true });
  } finally {
    fx.cleanup();
  }
});

// Codex runs none of Caveman's hooks until the user trusts them in /hooks, and
// Caveman never trusts them itself: without them nothing restarts the runtime
// after a reboot. Setup says so once; bare doctor keeps it as a note.
test("setup --yes tells a Codex user once to trust Caveman's hooks in /hooks", { skip }, async () => {
  const fx = modulesFixture({ agents: ["codex"] });
  const ask = "○ Caveman's hooks do not run until Codex trusts them · open /hooks in Codex once and trust them, so the local runtime restarts by itself\n";
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout.split(ask).length, 2, out.stdout);
    const doctor = await runCli(["doctor"], fx.env);
    assert.match(doctor.stdout, /^· codex: Caveman's hooks do not run until Codex trusts them · open \/hooks in Codex once and trust them, so the local runtime restarts by itself$/m);
  } finally {
    fx.cleanup();
  }
});

// With no agent on PATH there is nothing to try: never suggest `caveman claude`.
test("setup --yes with no agent installed says to install one instead of naming an agent", { skip }, async () => {
  const fx = modulesFixture({ agents: [] });
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.doesNotMatch(out.stdout, /Try:/);
    assert.match(out.stdout, /✓ Ready\. No agent set up yet · install one \(for example Claude Code\), then caveman setup\n/);
  } finally {
    fx.cleanup();
  }
});

// Signed in, setup turns routing on and it starts right away: setup says what
// routing sends.
test("setup --yes signed in says what routing sends", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const out = await runCli(["setup", "--yes"], { ...fx.env, CAVE_TOKEN: "test-token" });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /^routing is on · adds Auto to your agent's model picker; each request on Auto sends your latest ask \(with what your agent attaches to it\), the one before it, the end of the agent's last reply and request facts \(tools, effort, agent headers, token counts\) to Caveman Cloud to pick the model and effort; on the Free plan Caveman may keep them to improve routing; requests go to your providers on your keys, only ones routed to a Caveman Cloud model pass through it · caveman off routing to stop$/m);
    assert.doesNotMatch(out.stdout, /starts after you sign in/);
    assert.equal(modules(fx).routing, true);
  } finally {
    fx.cleanup();
  }
});

// Blocks' installer talks (binary copied, one line per harness, trust notes);
// setup says one line, plus the one thing the user still has to do.
test("setup --yes says one line for scripts and keeps only the caveat that needs the user", { skip }, async () => {
  const fx = modulesFixture({ blocks: true });
  try {
    const out = await runCli(["setup", "--yes"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\n✓ scripts ready \(Claude Code, Codex\)\n {2}Codex asks once: open \/hooks in Codex and approve the caveman-blocks hook\n/);
    assert.doesNotMatch(out.stdout + out.stderr, /binary:|: installed|trusts each/, "Blocks' own output stays out");
    const again = await runCli(["off", "scripts", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    const on = await runCli(["on", "scripts", "--yes"], fx.env);
    assert.match(on.stdout, /^✓ scripts ready \(Claude Code, Codex\)$/m);
  } finally {
    fx.cleanup();
  }
});

test("setup without --yes and no terminal prints the plan, applies nothing, exits 0", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["setup"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\nThis will\n/);
    assert.match(out.stdout, /Nothing changed: pass --yes to apply · caveman setup --yes\n$/);
    assert.deepEqual(snapshot(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("setup --dry-run prints the plan and writes nothing", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const before = snapshot(fx.home);
    const out = await runCli(["setup", "--dry-run"], fx.env);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /\nThis will\n/);
    assert.match(out.stdout, /Dry run: nothing was written\.\n$/);
    assert.deepEqual(snapshot(fx.home), before);
  } finally {
    fx.cleanup();
  }
});

test("--only, --skip and --agents pick modules and agents; a re-run keeps the current state", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const first = await runCli(["setup", "--yes", "--only", "output,input,browse", "--skip=browse", "--agents", "codex"], fx.env);
    assert.equal(first.code, 0, first.stderr);
    assert.match(first.stdout, /\nModules {2}output · input\nAgents {3}Codex\n/);
    assert.doesNotMatch(first.stdout, /routing is on/, "routing off means no sign-in line");
    assert.match(first.stdout, /Try: {2}caveman codex /);
    assert.deepEqual(modules(fx), { output: true, input: true, "waste-fixes": false, routing: false, scripts: false, browse: false });

    const again = await runCli(["setup", "--yes"], fx.env);
    assert.equal(again.code, 0, again.stderr);
    assert.match(again.stdout, /\nModules {2}output · input\n/, "re-running starts from what is on, not the defaults");
    assert.match(again.stdout, /\nFound Claude Code and Codex 1\.0\n/, "a wired agent's version comes from its journal, not from running it");
  } finally {
    fx.cleanup();
  }
});

// Under npx nothing named caveman is on PATH: hints name the npx form until
// setup has installed the command (tests/setup-from-runner covers that), and
// without npm to install it nothing is wired from the runner's cache.
test("under npx the hints print the npx command, and setup without npm writes nothing", { skip }, async () => {
  const fx = modulesFixture({ agents: ["codex", "claude"] });
  const npxDist = join(fx.home, "_npx", "0a1b", "node_modules", "@caveman-ai", "cli");
  cpSync(join(here, "..", "dist"), join(npxDist, "dist"), { recursive: true });
  cpSync(join(here, "..", "package.json"), join(npxDist, "package.json"));
  const run = (...argv) => new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [join(npxDist, "dist", "index.js"), "setup", ...argv], { env: fx.env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.on("exit", (code) => resolve({ code, stdout }));
    child.on("error", reject);
  });
  try {
    const asked = await run("--agents", "codex,claude");
    assert.equal(asked.code, 0, asked.stdout);
    assert.match(asked.stdout, /Nothing changed: pass --yes to apply · npx @caveman-ai\/cli setup --yes\n$/);
    const out = await run("--yes", "--agents", "codex,claude");
    assert.equal(out.code, 1, out.stdout);
    assert.match(out.stdout, /✗ npm not found: install the CLI yourself \(npm install -g @caveman-ai\/cli\), then run caveman setup\nNothing else changed\.\n$/);
    assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  } finally {
    fx.cleanup();
  }
});

// wrangler dev, a container, anything: 8787 is a popular port. Wiring an agent
// to it while someone else answers there sends them every request.
test("a first setup moves the runtime off a port another program holds, and later runs keep that port", { skip }, async () => {
  const holder = createServer();
  // Already held by something on this machine is the same case.
  await new Promise((resolve) => holder.once("error", resolve).listen(8787, "127.0.0.1", resolve));
  const fx = modulesFixture({ agents: ["claude"] });
  const fresh = modulesFixture({ agents: ["claude"] });
  const env = { ...fx.env };
  delete env.CAVE_GATEWAY_URL;
  delete env.CAVEMAN_LISTEN;
  try {
    const dry = await runCli(["setup", "--dry-run"], env);
    const port = dry.stdout.match(/RUN +local runtime on port (\d+) {2}127\.0\.0\.1:8787 is in use by another program\n/)?.[1];
    assert.ok(port && port !== "8787", dry.stdout);
    assert.equal(existsSync(join(env.CAVEMAN_HOME, "cloud.json")), false, "a dry run records nothing");
    const out = await runCli(["setup", "--yes"], env);
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.match(out.stdout, new RegExp(`○ 127\\.0\\.0\\.1:8787 is in use by another program · local runtime on port ${port}\\n✓ Claude Code wired\\n`));
    const route = () => JSON.parse(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8")).env.ANTHROPIC_BASE_URL;
    assert.equal(route(), `http://127.0.0.1:${port}/w/claude`);
    assert.equal(JSON.parse(readFileSync(join(env.CAVEMAN_HOME, "cloud.json"), "utf8")).localPort, Number(port));
    // Wired now: the address is settled, and nothing moves again.
    const again = await runCli(["setup", "--yes"], env);
    assert.equal(again.code, 0, again.stdout + again.stderr);
    assert.doesNotMatch(again.stdout, /in use by another program/);
    assert.equal(route(), `http://127.0.0.1:${port}/w/claude`);
    // An address the user chose is theirs: nothing is moved for it.
    const chosen = await runCli(["setup", "--dry-run"], { ...fresh.env, CAVE_GATEWAY_URL: "http://127.0.0.1:8787" });
    assert.doesNotMatch(chosen.stdout, /in use by another program/);
  } finally {
    holder.close();
    fx.cleanup();
    fresh.cleanup();
  }
});

// The runtime's port as setup recorded it (localPort), for a test to hold.
function runtimeOn(fx, port) {
  const env = { ...fx.env };
  delete env.CAVE_GATEWAY_URL;
  delete env.CAVEMAN_LISTEN;
  mkdirSync(env.CAVEMAN_HOME, { recursive: true });
  writeFileSync(join(env.CAVEMAN_HOME, "cloud.json"), JSON.stringify({ localPort: port }));
  return env;
}

// enable, on and setup --agent-native wire agents too: each moves the runtime
// off a held port before the first agent is wired, as setup does.
test("every door that wires a first agent moves the runtime off a port another program holds", { skip }, async () => {
  const holder = createServer();
  await new Promise((resolve) => holder.listen(0, "127.0.0.1", resolve));
  const held = holder.address().port;
  const fixtures = [];
  try {
    for (const argv of [["enable", "claude"], ["on", "--all", "--yes"], ["setup", "--agent-native", "claude"]]) {
      const fx = modulesFixture({ agents: ["claude"] });
      fixtures.push(fx);
      const out = await runCli(argv, runtimeOn(fx, held));
      const said = `${out.stdout}${out.stderr}`;
      assert.equal(out.code, 0, `${argv.join(" ")}: ${said}`);
      const port = said.match(new RegExp(`○ 127\\.0\\.0\\.1:${held} is in use by another program · local runtime on port (\\d+)\\n`))?.[1];
      assert.ok(port, `${argv.join(" ")}: ${said}`);
      assert.equal(JSON.parse(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8")).env.ANTHROPIC_BASE_URL, `http://127.0.0.1:${port}/w/claude`, argv.join(" "));
    }
  } finally {
    holder.close();
    for (const fx of fixtures) fx.cleanup();
  }
});

// Nothing answers on a port inside a Windows excluded range (Hyper-V, WSL,
// Docker reserve them), yet the runtime cannot bind it. A socket bound without
// listening is the same case on any OS.
test("a first setup moves the runtime off a port it cannot bind even though nothing answers there", { skip }, async () => {
  const python = spawn("python3", ["-c", "import socket, sys\ns = socket.socket()\ns.bind(('127.0.0.1', 0))\nprint(s.getsockname()[1], flush=True)\nsys.stdin.read()"], { stdio: ["pipe", "pipe", "inherit"] });
  const held = Number(String(await new Promise((resolve, reject) => { python.stdout.once("data", resolve); python.once("error", reject); })).trim());
  const fx = modulesFixture({ agents: ["claude"] });
  try {
    const dry = await runCli(["setup", "--dry-run"], runtimeOn(fx, held));
    assert.match(dry.stdout, new RegExp(`RUN +local runtime on port \\d+ {2}127\\.0\\.0\\.1:${held} is in use by another program\\n`));
  } finally {
    python.kill();
    fx.cleanup();
  }
});

test("setup refuses unknown modules and agents that are not installed", { skip }, async () => {
  const fx = modulesFixture();
  try {
    const badModule = await runCli(["setup", "--only", "output,teleport"], fx.env);
    assert.equal(badModule.code, 2);
    assert.match(badModule.stderr, /unknown module teleport · modules: output, input, waste-fixes, routing, scripts, browse/);
    const badAgent = await runCli(["setup", "--yes", "--agents", "gemini"], fx.env);
    assert.equal(badAgent.code, 1);
    assert.match(badAgent.stderr, /--agents: gemini is not installed here · found: claude, codex/);
    assert.equal(existsSync(join(fx.env.CAVEMAN_HOME, "cloud.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("setup --json still reports binary status for scripts", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runIsolated(["setup", "--json"], { env: isolated.env });
    const report = JSON.parse(out.stdout);
    assert.ok(Array.isArray(report.binaries) && report.binaries.some((b) => b.name === "caveman-proxy"));
    assert.equal(typeof report.ready, "boolean");
    assert.equal(existsSync(join(isolated.home, "cloud.json")), false, "--json is read-only");
  } finally {
    isolated.cleanup();
  }
});

// The reference sends readers to command help for the accepted flags: it must
// list every documented form, setup's older verbs and bare doctor included.
test("setup --help prints every setup form and doctor's usage names bare doctor", async () => {
  const isolated = isolatedCliEnv();
  try {
    const setupHelp = await runIsolated(["setup", "--help"], { env: isolated.env });
    assert.equal(setupHelp.code, 0, setupHelp.stderr);
    assert.match(setupHelp.stdout, /^usage: caveman setup \[--yes\].* \| setup --install \[--json\] \| setup --json \| setup --agent-native <claude\|codex> \[--remove\]\n$/);
    assert.equal(existsSync(join(isolated.home, "cloud.json")), false, "--help writes nothing");
    const doctorHelp = await runIsolated(["doctor", "--help"], { env: isolated.env });
    assert.match(doctorHelp.stderr, /^usage: caveman doctor \[<claude\|codex\|hermes\|gemini\|opencode\|pi\|aider\|generic> \[--fix\]\]\n$/);
  } finally {
    isolated.cleanup();
  }
});
