import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { PassThrough } from "node:stream";
import { fileURLToPath, pathToFileURL } from "node:url";
import { modulesFixture } from "./_modules.mjs";

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
    async press(waitFor, keys) {
      for (let i = 0; i < 300 && !waitFor.test(text); i++) await new Promise((resolve) => setTimeout(resolve, 10));
      assert.match(text, waitFor);
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
  assert.match(tty.text(), /Continue\? › No\nNothing changed\.\n$/);
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
    'expect "Continue?"', "sleep 0.2", 'send "\\r"',
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
      child.on("exit", (code) => resolve({ code, text: text.replace(/\r/g, "") }));
      child.on("error", reject);
    });
    assert.equal(out.code, 0, out.text);
    assert.match(out.text, /Found Claude Code 1\.0 and Codex 1\.0/);
    assert.match(out.text, /Continue\? › Yes/);
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
