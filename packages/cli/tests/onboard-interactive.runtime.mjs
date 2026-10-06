import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { PassThrough } from "node:stream";
import { fileURLToPath, pathToFileURL } from "node:url";
import { modulesFixture, runCli } from "./_modules.mjs";

// The interactive first run, two ways: in-process with a fake terminal (keys
// in, text out) for the flow itself, and end to end through the real CLI under
// expect(1) for sign-in against a Cloud that answers 403.
const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
// In-process: the module fixture's stub binaries and PATH (so apply neither
// downloads nor finds the host's agents), and index.js loaded first because it
// hands the module host to apply.
const fixture = modulesFixture();
const home = fixture.home;
for (const [key, value] of Object.entries(fixture.env)) process.env[key] = value;
delete process.env.CI;
await import(`${pathToFileURL(cli).href}?onboard-interactive`);
const { onboard } = await import(pathToFileURL(join(here, "..", "dist", "modules", "onboard.js")).href);
const configPath = join(fixture.env.CAVEMAN_HOME, "cloud.json");

function terminal() {
  const input = new PassThrough();
  input.isTTY = true;
  input.setRawMode = () => input;
  const output = new PassThrough();
  output.columns = 100;
  let text = "";
  // Keep what a terminal shows: drop cursor and redraw escapes and carriage returns.
  output.on("data", (chunk) => (text += String(chunk).replace(/\x1b\[[0-9;?]*[A-Za-z]|\r/g, "")));
  return {
    input,
    output,
    text: () => text,
    async waitFor(pattern) {
      for (let i = 0; i < 300 && !pattern.test(text); i++) await new Promise((resolve) => setTimeout(resolve, 10));
      assert.match(text, pattern);
    },
    async press(waitFor, keys) {
      await this.waitFor(waitFor);
      // Continue ignores a Yes in its first 400ms (a double-tapped Enter).
      if (/Continue/.test(waitFor.source) && /^[\ry]$/.test(keys)) await new Promise((resolve) => setTimeout(resolve, 450));
      input.write(keys);
    },
  };
}

function deps(tty, overrides = {}) {
  return {
    cmd: "caveman",
    agents: [],
    interactive: true,
    signedIn: async () => false,
    signIn: async () => ({ email: "you@example.com" }),
    discloseTelemetry: async () => tty.output.write("[disclosure]\n"),
    markFirstRun: async () => {},
    markDeclined: () => tty.output.write("[declined]\n"),
    input: tty.input,
    output: tty.output,
    ...overrides,
  };
}

test("routing ticked and sign-in closed: setup still succeeds and routing waits with its reason", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty, {
    signIn: async () => { throw Object.assign(new Error("Sign-in is not open on api.caveman.so yet."), { code: "sign_in_closed" }); },
  }));
  await tty.press(/space toggles/, "\r");
  await tty.press(/Continue\?/, "\r");
  const result = await run;
  assert.deepEqual([result.confirmed, result.ok], [true, true]);
  assert.match(tty.text(), /Routing needs a free Caveman account\.\n {2}! Sign-in is not open on api\.caveman\.so yet\.\n○ routing is on and starts once sign-in opens · caveman login\n/);
  assert.match(tty.text(), /✓ Ready\. Try: {2}caveman claude {6}See it: {2}caveman status\n\[disclosure\]\n$/, "telemetry disclosure is the last line");
  assert.equal(JSON.parse(readFileSync(configPath, "utf8")).modules.routing, true);
});

test("answering No writes nothing", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty));
  await tty.press(/space toggles/, "\r");
  await tty.press(/Continue\?/, "n");
  const result = await run;
  assert.deepEqual([result.confirmed, result.cancelled], [false, false]);
  assert.match(tty.text(), /Continue\? › No\n\[declined\]\nNothing changed · caveman setup when you want it\n$/);
  assert.equal(existsSync(configPath), false);
});

test("Enter typed ahead before Continue is shown never accepts the plan unseen", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty, {
    agents: [{ id: "claude", name: "Claude Code", installed: true, wired: false }],
  }));
  await tty.press(/space toggles/, "\r");
  await tty.press(/Agents\n/, "\r");
  // Two more Enters land while the plan is computed, before Continue draws,
  // and one just after it draws.
  tty.input.write("\r\r");
  await tty.waitFor(/Continue\? › Yes \/ No/);
  tty.input.write("\r");
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.doesNotMatch(tty.text(), /Continue\? › Yes\n/, "still waiting for a real answer");
  tty.input.write("n");
  const result = await run;
  assert.equal(result.confirmed, false, "buffered Enters must not answer Continue");
  assert.equal(existsSync(configPath), false);
});

test("a re-run shows the current state pre-checked, and Ctrl-C cancels", async () => {
  writeFileSync(configPath, JSON.stringify({ modules: { output: true, input: true, "waste-fixes": true, routing: true, scripts: true, browse: false } }));
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty));
  await tty.press(/space toggles/, "\x03");
  const result = await run;
  assert.equal(result.cancelled, true);
  assert.match(tty.text(), / ◻ browse {8}compressed pages for browser tools/);
  assert.match(tty.text(), / ◼ output {8}the agent says less/);
  assert.match(tty.text(), /Cancelled\. Nothing changed\.\n$/);
});

test("space unticks a module and esc skips sign-in", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty, {
    agents: [{ id: "claude", name: "Claude Code", installed: true, wired: false, version: "2.1.37" }, { id: "gemini", name: "Gemini", installed: false, wired: false }],
    signIn: (ui) => {
      ui.code("https://app.caveman.so/activate", "ABCD-EFGH", false);
      return new Promise((_, reject) => ui.signal.addEventListener("abort", () => reject(ui.signal.reason)));
    },
  }));
  await tty.press(/space toggles/, " ");
  await tty.press(/›◻ output/, "\r");
  await tty.press(/Agents\n.*◼ Claude Code {3}◻ Gemini \(not installed\)/, "\r");
  await tty.press(/Continue\?/, "y");
  await tty.press(/esc skips/, "\x1b");
  const result = await run;
  assert.equal(result.ok, true);
  assert.deepEqual(result.plan.agents, ["claude"]);
  assert.match(tty.text(), /Found Claude Code 2\.1\n/);
  assert.match(tty.text(), / {2}Open https:\/\/app\.caveman\.so\/activate and enter ABCD-EFGH {3}\(esc skips\)\n○ routing is on and starts after you sign in · caveman login\n/);
  assert.equal(JSON.parse(readFileSync(configPath, "utf8")).modules.output, false);
});

test("esc after the credentials are saved says signed in", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  let saved = false;
  const run = onboard({ yes: false, dryRun: false }, deps(tty, {
    signedIn: async () => saved,
    signIn: (ui) => {
      ui.code("https://app.caveman.so/activate", "ABCD-EFGH", false);
      saved = true; // the grant arrived and was stored; the ACK is in flight
      return new Promise((_, reject) => ui.signal.addEventListener("abort", () => reject(ui.signal.reason)));
    },
  }));
  await tty.press(/space toggles/, "\r");
  await tty.press(/Continue\?/, "\r");
  await tty.press(/esc skips/, "\x1b");
  await run;
  assert.match(tty.text(), /\(esc skips\)\n {2}✓ signed in\n/);
  assert.doesNotMatch(tty.text(), /after you sign in/);
});

test("a wired agent missing from PATH stays ticked, so the plan never unwires it silently", async () => {
  rmSync(configPath, { force: true });
  const tty = terminal();
  const run = onboard({ yes: false, dryRun: false }, deps(tty, {
    agents: [{ id: "claude", name: "Claude Code", installed: false, wired: true }, { id: "codex", name: "Codex", installed: true, wired: false }],
  }));
  await tty.press(/space toggles/, "\r");
  await tty.press(/Agents\n ◼ Claude Code/, "\r");
  await tty.press(/Continue\?/, "n");
  const result = await run;
  assert.deepEqual(result.plan.agents, ["claude"], "re-run keeps the wired agent and adds nothing unasked");
});

function hasExpect() {
  try {
    execFileSync("sh", ["-c", "command -v expect"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function expectRun(script, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("expect", [script], { env, stdio: ["ignore", "pipe", "pipe"] });
    let text = "";
    child.stdout.on("data", (d) => (text += d));
    child.stderr.on("data", (d) => (text += d));
    child.on("exit", (code) => resolve({ code, text: text.replace(/\x1b\[[0-9;?]*[A-Za-z]|\r/g, "") }));
    child.on("error", reject);
  });
}

test("end to end: a No at the agent door is remembered; caveman claude stops asking", { skip: hasExpect() ? false : "expect(1) not installed" }, async () => {
  const box = modulesFixture();
  const env = { ...box.env, TERM: "xterm" };
  delete env.CI;
  const first = join(box.home, "first.exp");
  writeFileSync(first, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} claude`,
    'expect "space toggles"', "sleep 0.2", 'send "\\r"',
    'expect "Agents"', "sleep 0.2", 'send "\\r"',
    'expect "Continue?"', "sleep 0.6", 'send "n"',
    "expect eof",
    "",
  ].join("\n"));
  const second = join(box.home, "second.exp");
  writeFileSync(second, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} claude`,
    'expect { "space toggles" { puts "\\nASKED-AGAIN"; exit 3 } eof { exit 0 } }',
    "",
  ].join("\n"));
  try {
    const declined = await expectRun(first, env);
    assert.match(declined.text, /Nothing changed · Claude Code runs this session only · caveman setup when you want it/);
    assert.equal(existsSync(join(box.home, ".claude", "settings.json")), false);
    const again = await expectRun(second, env);
    assert.equal(again.code, 0, again.text);
    assert.doesNotMatch(again.text, /ASKED-AGAIN|space toggles/);
  } finally {
    box.cleanup();
  }
});

// Real gemini and opencode write their home (~/.gemini, ~/.local/share/opencode)
// on any run, `--version` included, so nothing may run an agent before Continue,
// and status before setup reads agents from PATH alone.
test("end to end: before Continue nothing runs a detected agent, through a declined first run, setup, --dry-run, on and status", { skip: hasExpect() ? false : "expect(1) not installed", timeout: 60_000 }, async () => {
  const box = modulesFixture({ agents: [] });
  const ran = join(box.home, "agents-ran");
  for (const agent of ["claude", "codex", "gemini", "opencode"]) {
    writeFileSync(join(box.bin, agent), `#!/bin/sh\necho "${agent} $*" >> "${ran}"\necho '${agent} 1.0.0'\n`, { mode: 0o755 });
  }
  const script = join(box.home, "decline.exp");
  writeFileSync(script, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} setup`,
    'expect "space toggles"', "sleep 0.2", 'send "\\r"',
    'expect "Agents"', "sleep 0.2", 'send "\\r"',
    'expect "Continue?"', "sleep 0.6", 'send "n"',
    "expect eof",
    "",
  ].join("\n"));
  const env = { ...box.env, TERM: "xterm" };
  delete env.CI;
  const agentRuns = () => existsSync(ran) ? readFileSync(ran, "utf8") : "";
  try {
    const declined = await expectRun(script, env);
    assert.match(declined.text, /Found Claude Code, Codex, Gemini and opencode\n/, "no version without running the agent");
    assert.match(declined.text, /CREATE +~\/\.config\/opencode\/plugins\/caveman-native\.js/, "the plan still names every file");
    assert.match(declined.text, /Nothing changed · caveman setup when you want it/);
    assert.equal(agentRuns(), "", "a declined first run ran an agent");
    for (const argv of [["setup"], ["setup", "--dry-run"], ["on", "output"], ["status"], ["status", "--json"]]) {
      const out = await runCli(argv, box.env);
      assert.equal(agentRuns(), "", `caveman ${argv.join(" ")} ran an agent`);
      if (argv[1] === "--json" && argv[0] === "status") {
        const gemini = JSON.parse(out.stdout).native_integrations.find((item) => item.agent === "gemini");
        assert.deepEqual([gemini.binary_present, gemini.state], [true, "available"], "an agent on PATH still shows as present");
      }
    }
  } finally {
    box.cleanup();
  }
});

test("end to end: at the agent door, unticking every wiring module never wires the agent", { skip: hasExpect() ? false : "expect(1) not installed", timeout: 60_000 }, async () => {
  const box = modulesFixture();
  const env = { ...box.env, TERM: "xterm" };
  delete env.CI;
  const script = join(box.home, "door.exp");
  // Untick output, input, waste fixes and routing (the four that wire agents);
  // keep scripts and browse, keep Claude Code ticked, confirm.
  writeFileSync(script, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} claude`,
    'expect "space toggles"', "sleep 0.2",
    'send " "', "sleep 0.1", 'send "j"', "sleep 0.1",
    'send " "', "sleep 0.1", 'send "j"', "sleep 0.1",
    'send " "', "sleep 0.1", 'send "j"', "sleep 0.1",
    'send " "', "sleep 0.1", 'send "\\r"',
    'expect "Agents"', "sleep 0.2", 'send "\\r"',
    'expect "Continue?"', "sleep 0.6", 'send "y"',
    "expect eof",
    "",
  ].join("\n"));
  try {
    const out = await expectRun(script, env);
    assert.doesNotMatch(out.text, /UPDATE {4}~\/\.claude|CREATE {4}~\/\.claude/, "the plan never offered to wire Claude Code");
    assert.equal(existsSync(join(box.home, ".claude", "settings.json")), false, out.text);
    assert.equal(existsSync(join(box.home, "integrations", "claude.json")), false, out.text);
  } finally {
    box.cleanup();
  }
});

test("end to end: CI=1 in a terminal (install.sh in CI) never applies without --yes", { skip: hasExpect() ? false : "expect(1) not installed" }, async () => {
  const box = modulesFixture();
  const script = join(box.home, "ci.exp");
  writeFileSync(script, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} setup`,
    "expect eof",
    "catch wait result",
    "exit [lindex $result 3]",
    "",
  ].join("\n"));
  try {
    const out = await expectRun(script, { ...box.env, TERM: "xterm", CI: "1" });
    assert.equal(out.code, 0, out.text);
    assert.doesNotMatch(out.text, /space toggles/);
    assert.match(out.text, /Nothing changed: pass --yes to apply/);
    assert.equal(existsSync(join(box.env.CAVEMAN_HOME, "cloud.json")), false);
  } finally {
    box.cleanup();
  }
});

test("end to end: caveman setup in a terminal against a Cloud that refuses sign-in (403)", { skip: hasExpect() ? false : "expect(1) not installed" }, async () => {
  const server = createServer((req, res) => {
    req.resume();
    res.writeHead(req.url === "/api/v1/auth/device/code" ? 403 : 404, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { code: "cave_device_login_disabled" } }));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = server.address().port;
  const box = modulesFixture();
  const script = join(box.home, "drive.exp");
  writeFileSync(script, [
    "set timeout 20",
    `spawn -noecho ${process.execPath} ${cli} setup`,
    'expect "space toggles"', "sleep 0.2", 'send "\\r"',
    'expect "Agents"', "sleep 0.2", 'send "\\r"',
    'expect "Continue?"', "sleep 0.6", 'send "\\r"',
    "expect eof",
    "catch wait result",
    "exit [lindex $result 3]",
    "",
  ].join("\n"));
  const env = { ...box.env, TERM: "xterm", CAVE_API_URL: `http://127.0.0.1:${port}` };
  delete env.CI;
  try {
    const out = await new Promise((resolve, reject) => {
      const child = spawn("expect", [script], { env, stdio: ["ignore", "pipe", "pipe"] });
      let text = "";
      child.stdout.on("data", (d) => (text += d));
      child.stderr.on("data", (d) => (text += d));
      child.on("exit", (code) => resolve({ code, text: text.replace(/\x1b\[[0-9;?]*[A-Za-z]|\r/g, "") }));
      child.on("error", reject);
    });
    assert.equal(out.code, 0, out.text);
    assert.match(out.text, /Found Claude Code and Codex\n/, "no version before the agent was ever wired");
    // One line per step between Continue and sign-in; enable's own report stays out.
    assert.match(out.text, /Continue\? › Yes\n✓ Claude Code wired\n✓ Codex wired\n○ local runtime starts with your next agent session\n○ scripts: caveman-blocks not installed yet\n\nRouting needs a free Caveman account\.\n/);
    assert.doesNotMatch(out.text, /planned user-scoped writes|native Caveman enabled|→ /);
    assert.match(out.text, new RegExp(`! Sign-in is not open on 127\\.0\\.0\\.1:${port} yet\\.`));
    assert.match(out.text, /routing is on and starts once sign-in opens · caveman login/);
    assert.match(out.text, /✓ Ready\./);
    assert.equal(JSON.parse(readFileSync(join(box.env.CAVEMAN_HOME, "cloud.json"), "utf8")).modules.routing, true);
  } finally {
    server.close();
    box.cleanup();
  }
});

test.after(() => fixture.cleanup());
