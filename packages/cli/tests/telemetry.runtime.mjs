import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function isolatedEnv(extra = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, ...extra };
  delete env.DO_NOT_TRACK;
  delete env.CAVEMAN_TELEMETRY;
  delete env.CAVEMAN_TELEMETRY_URL;
  // GitHub Actions exports CI=1, which force-disables telemetry regardless of
  // the pty — the interactive-disclosure tests then fail only on CI. A test
  // that wants CI semantics passes CI explicitly via `extra` (reapplied below).
  delete env.CI;
  Object.assign(env, extra);
  return { env, home, caveDir };
}

function runCli(argv, env, opts = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    const started = Date.now();
    let timer;
    if (opts.timeoutMs) {
      timer = setTimeout(() => {
        child.kill("SIGKILL");
        reject(new Error(`CLI timed out after ${opts.timeoutMs}ms`));
      }, opts.timeoutMs);
    }
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => {
      if (timer) clearTimeout(timer);
      resolve({ code, stdout, stderr, elapsedMs: Date.now() - started });
    });
    child.on("error", reject);
    if (opts.input !== undefined) child.stdin.end(opts.input);
    else child.stdin.end();
  });
}

function startTelemetryStub({ hang = false } = {}) {
  const posts = [];
  const sockets = new Set();
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      posts.push({ method: req.method, url: req.url, body });
      if (hang) return;
      res.writeHead(202, { "content-type": "application/json" });
      res.end("{}");
    });
  });
  server.on("connection", (socket) => {
    // Same reason as the listener unref in listenOrSkip: a socket the hang-mode
    // stub keeps open must not pin the event loop after a failed assertion.
    socket.unref();
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  return { server, posts, close: () => { for (const socket of sockets) socket.destroy(); server.close(); } };
}

function listen(server) {
  return new Promise((resolve, reject) => {
    const onError = (error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolve(server.address().port);
    };
    server.once("error", onError);
    server.listen(0, "127.0.0.1", onListening);
  });
}

async function listenOrSkip(t, stub) {
  try {
    const port = await listen(stub.server);
    // A test that fails an assertion never reaches its stub.close(); an
    // un-unref'd listener then holds this file's event loop open forever and
    // node --test waits on it — one failed assert hung the whole suite for
    // 6 hours on CI. unref makes a failure fail instead of hang.
    stub.server.unref();
    return port;
  } catch (error) {
    stub.close();
    if (error?.code === "EPERM") {
      t.skip("local HTTP server listen denied in this sandbox");
      return null;
    }
    throw error;
  }
}

test("non-TTY run does not prompt or post telemetry", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["compress"], { ...env, CAVEMAN_ENGINE_BIN: join(tmpdir(), "missing-caveman-engine") }, { input: "hello" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "hello");
  assert.doesNotMatch(out.stderr, /Help improve Caveman|Send anonymous usage data/);
  assert.equal(stub.posts.length, 0, "telemetry must stay off in non-interactive runs unless env opts in");

  stub.close();
});

// runCliPty runs the CLI under a real pty via script(1) so TTY-gated behavior
// (default-on persistence + disclosure) is exercised. Returns null when the
// platform's script(1) is unavailable or fails to allocate a pty.
function runCliPty(argv, env) {
  const cmd = process.platform === "darwin"
    ? ["script", ["-q", "/dev/null", "node", cli, ...argv]]
    : ["script", ["-qec", ["node", cli, ...argv].map((part) => `'${part}'`).join(" "), "/dev/null"]];
  return new Promise((resolve) => {
    let child;
    try {
      // stdin must be a real fd, not a socketpair: macOS script(1) runs
      // tcgetattr on it and dies with "Operation not supported on socket".
      child = spawn(cmd[0], cmd[1], { env, stdio: ["ignore", "pipe", "pipe"] });
    } catch {
      resolve(null);
      return;
    }
    let output = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), 30000);
    child.stdout.on("data", (d) => (output += d));
    child.stderr.on("data", (d) => (output += d));
    child.on("exit", (code) => {
      clearTimeout(timer);
      resolve({ code, output });
    });
    child.on("error", () => {
      clearTimeout(timer);
      resolve(null);
    });
  });
}

test("interactive first command persists default-on with disclosure and a stable id", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const first = await runCliPty(["tools", "config", "get"], env);
  if (first === null || first.code !== 0) {
    stub.close();
    t.skip("script(1) pty unavailable in this environment");
    return;
  }
  assert.match(first.output, /anonymous usage stats on/, "first interactive run must print the disclosure");
  const cfg = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  assert.equal(cfg.telemetry?.enabled, true);
  assert.match(cfg.telemetry?.anonymousId ?? "", uuidRe, "persisted decision must carry a stable anonymous id");

  const second = await runCliPty(["tools", "config", "get"], env);
  assert.ok(second && second.code === 0, "second run failed");
  assert.doesNotMatch(second.output, /anonymous usage stats on/, "disclosure prints once, not per run");

  const ids = new Set(stub.posts.map((p) => JSON.parse(p.body)[0]?.anonymous_id));
  assert.ok(stub.posts.length >= 2, `expected posts from both runs, got ${stub.posts.length}`);
  assert.equal(ids.size, 1, `all events must carry the persisted id, saw ${[...ids].join(", ")}`);
  assert.equal([...ids][0], cfg.telemetry.anonymousId);

  stub.close();
});

test("non-TTY run never persists the default-on telemetry decision", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["compress"], { ...env, CAVEMAN_ENGINE_BIN: join(tmpdir(), "missing-caveman-engine") }, { input: "hello" });
  assert.equal(out.code, 0, out.stderr);
  assert.doesNotMatch(out.stderr, /anonymous usage stats on/, "disclosure line is TTY-only");
  let persisted = {};
  try {
    persisted = JSON.parse(readFileSync(join(home, ".caveman-cloud", "config.json"), "utf8"));
  } catch {
    // no config written at all is the expected outcome
  }
  assert.ok(!("telemetry" in persisted), "automation must never mint a default-on decision or anonymous id");
  assert.equal(stub.posts.length, 0);

  stub.close();
});

test("persisted v1 opt-out survives the default-on era", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const optOut = { enabled: false, decidedAt: "2026-07-03T00:00:00.000Z", promptVersion: 1 };
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ telemetry: optOut }));

  const out = await runCli(["telemetry", "status"], env);
  assert.equal(out.code, 0, out.stderr);
  const status = JSON.parse(out.stdout);
  assert.equal(status.state, "off", "an old explicit No must never be flipped by the new default");
  const cfg = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.deepEqual(cfg.telemetry, optOut, "the v1 decision must not be rewritten");
  assert.equal(stub.posts.length, 0);

  stub.close();
});

test("welcome degrades to a silent no-op without a TTY", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });

  const out = await runCli(["welcome"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.doesNotMatch(out.stderr, /caveman|scanning|would have cut/i, "non-TTY welcome prints nothing");

  stub.close();
});

test("DO_NOT_TRACK=1 overrides CAVEMAN_TELEMETRY=1", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    DO_NOT_TRACK: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(stub.posts.length, 0, "DO_NOT_TRACK must suppress env opt-in telemetry");

  stub.close();
});

test("CAVEMAN_TELEMETRY=1 emits one allowlisted command_run event", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version", "leaky-argv-sentinel", "/tmp/secret-path"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(stub.posts.length, 1, "exactly one telemetry POST");
  assert.doesNotMatch(stub.posts[0].body, /leaky-argv-sentinel|secret-path/, "payload must not contain raw argv strings");
  const events = JSON.parse(stub.posts[0].body);
  assert.equal(events.length, 1);
  assert.equal(events[0].schema, "cli/v1");
  assert.equal(events[0].event, "command_run");
  assert.equal(events[0].command, "version");
  assert.match(events[0].anonymous_id, uuidRe);
  assert.equal(events[0].exit_class, "ok");
  assert.equal(typeof events[0].duration_ms, "number");

  stub.close();
});

// A resolved agent binary the OS refuses to launch (Windows POSIX-shim class,
// ENOEXEC here) must be booked as exec_failed, not lost inside "other" — that
// blindness is how the win32 launch bug hid in the dashboard.
test("agent spawn failure books error_class exec_failed", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
  });
  const binDir = mkdtempSync(join(tmpdir(), "cave-noexec-bin-"));
  // darwin: shebang-less garbage makes spawn throw ENOEXEC *synchronously* —
  // the exact path this guards. Linux execvp instead re-runs such a file under
  // /bin/sh (no spawn error at all), so there a nonexistent shebang interpreter
  // forces the async ENOENT 'error' event through the same wrapped message.
  const unlaunchable = process.platform === "darwin"
    ? "\x00\x01 not launchable\n"
    : "#!/caveman-no-such-interpreter\n";
  writeFileSync(join(binDir, "codex"), unlaunchable, { mode: 0o755 });
  env.PATH = `${binDir}:${env.PATH}`;
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(
    join(home, ".caveman-cloud", "config.json"),
    JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false } }),
  );

  const out = await runCli(["wrap", "codex"], env, { timeoutMs: 30000 });
  assert.notEqual(out.code, 0, "wrap must exit non-zero when the agent cannot launch");
  assert.match(out.stderr, /failed to exec .*codex/);
  const events = stub.posts.flatMap((post) => JSON.parse(post.body));
  const run = events.find((event) => event.event === "command_run");
  assert.ok(run, `no command_run event posted; events: ${JSON.stringify(events)}`);
  assert.equal(run.command, "wrap");
  assert.equal(run.exit_class, "error");
  assert.equal(run.error_class, "exec_failed");

  stub.close();
});

test("telemetry POST timeout does not hold the CLI past roughly two seconds", async (t) => {
  const stub = startTelemetryStub({ hang: true });
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });

  const out = await runCli(["version"], env, { timeoutMs: 5000 });
  assert.equal(out.code, 0, out.stderr);
  assert.ok(out.elapsedMs < 2500, `CLI should exit after AbortSignal timeout, elapsed=${out.elapsedMs}ms`);
  assert.equal(stub.posts.length, 1, "the hung endpoint should still receive the attempted POST");

  stub.close();
});

// ensureTelemetryDefault must never rewrite a persisted decision — pin that an
// interactive run over a stale-version OPT-OUT leaves the config byte-identical,
// prints nothing, and sends nothing.
test("an interactive stale-version opt-out stays byte-identical and silent", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const optOut = { enabled: false, decidedAt: "2026-07-03T00:00:00.000Z", promptVersion: 1 };
  const raw = JSON.stringify({ telemetry: optOut });
  writeFileSync(join(configDir, "config.json"), raw);

  const out = await runCliPty(["tools", "config", "get"], env);
  if (out === null || out.code !== 0) {
    stub.close();
    t.skip("script(1) pty unavailable in this environment");
    return;
  }
  assert.doesNotMatch(out.output, /anonymous usage stats on/, "an opt-out must never be re-disclosed");
  assert.equal(readFileSync(join(configDir, "config.json"), "utf8"), raw, "an opt-out config must stay byte-identical");
  assert.equal(stub.posts.length, 0, "an opt-out must never send");

  stub.close();
});

// stubProxyStats writes a fake caveman-proxy that answers `stats --json` with a
// fixed aggregate, plus the store file whose existence gates the read.
function stubProxyStats({ env, caveDir }, { tokensIn, tokensSaved, basis = "inferred" }) {
  const bin = join(mkdtempSync(join(tmpdir(), "cave-bin-")), "caveman-proxy");
  const body = JSON.stringify({ tokens_in: tokensIn, compression_tokens_saved: tokensSaved, basis });
  writeFileSync(bin, `#!/bin/sh\ncat <<'CAVE_EOF'\n${body}\nCAVE_EOF\n`, { mode: 0o755 });
  chmodSync(bin, 0o755);
  writeFileSync(join(caveDir, "caveman.db"), "");
  // The 400ms default budget is about real UX, not correctness; a loaded CI
  // runner can blow it just spawning the stub, flaking every token assertion.
  return { ...env, CAVEMAN_PROXY_BIN: bin, CAVEMAN_TELEMETRY_TOKEN_READ_TIMEOUT_MS: "10000" };
}

test("command_run carries the proxy token delta, then stops repeating it", async (t) => {
  if (process.platform === "win32") {
    t.skip("sh stub is POSIX-only");
    return;
  }
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const iso = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });
  const env = stubProxyStats(iso, { tokensIn: 184320, tokensSaved: 41200 });

  // First sight of the store only seeds the watermark. Whatever it already holds
  // predates this disclosure and must never be reported retroactively.
  const first = await runCli(["version"], env);
  assert.equal(first.code, 0, first.stderr);
  const firstEvent = JSON.parse(stub.posts[0].body)[0];
  assert.ok(!("tokens_processed" in firstEvent), "pre-existing history must not be swept up by the first event");
  const watermark = JSON.parse(readFileSync(join(iso.home, ".caveman-cloud", "config.json"), "utf8")).telemetryTokens;
  assert.equal(watermark.tokensIn, 184320, "the baseline is still recorded");
  assert.equal(watermark.tokensSaved, 41200);

  // Same totals on the next run: the delta is zero, so the fields stay absent
  // rather than reporting a zero.
  const second = await runCli(["version"], env);
  assert.equal(second.code, 0, second.stderr);
  const secondEvent = JSON.parse(stub.posts[1].body)[0];
  assert.ok(!("tokens_processed" in secondEvent), "an unchanged store must not resend the same tokens");
  assert.ok(!("tokens_saved" in secondEvent));
  assert.equal(secondEvent.event, "command_run", "the event itself still ships");

  // Store grew: only the increment goes out.
  const grown = stubProxyStats(iso, { tokensIn: 200000, tokensSaved: 45000 });
  const third = await runCli(["version"], grown);
  assert.equal(third.code, 0, third.stderr);
  const thirdEvent = JSON.parse(stub.posts[2].body)[0];
  assert.equal(thirdEvent.tokens_processed, 15680);
  assert.equal(thirdEvent.tokens_saved, 3800);
  assert.equal(thirdEvent.tokens_basis, "inferred", "token volume must ship with its basis, never bare");

  stub.close();
});

// The seeding rule has to survive the off/on boundary: tokens processed while
// telemetry was off belong to the opt-out window and are never reported later.
test("telemetry off drops the token watermark so re-enabling re-seeds", async (t) => {
  if (process.platform === "win32") {
    t.skip("sh stub is POSIX-only");
    return;
  }
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const iso = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });
  const configDir = join(iso.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    telemetry: { enabled: true, anonymousId: "123e4567-e89b-12d3-a456-426614174000", decidedAt: "2026-08-01T00:00:00.000Z", promptVersion: 4 },
    telemetryTokens: { tokensIn: 1000, tokensSaved: 200, at: "2026-08-01T00:00:00.000Z" },
  }));

  const off = await runCli(["telemetry", "off"], iso.env);
  assert.equal(off.code, 0, off.stderr);
  const afterOff = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.ok(!("telemetryTokens" in afterOff), "the watermark must not outlive the opt-out");

  // Traffic accumulated while off. The next run that can send must re-seed
  // against the grown store rather than report the opt-out window.
  const env = stubProxyStats(iso, { tokensIn: 900000, tokensSaved: 300000 });
  const back = await runCli(["version"], { ...env, CAVEMAN_TELEMETRY: "1" });
  assert.equal(back.code, 0, back.stderr);
  const events = stub.posts.flatMap((p) => JSON.parse(p.body));
  assert.ok(events.length > 0, "the run must still send its command_run event");
  for (const event of events) {
    assert.ok(!("tokens_processed" in event), "opt-out window traffic must never be reported on re-enable");
  }
  const watermark = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8")).telemetryTokens;
  assert.equal(watermark.tokensIn, 900000, "re-enabling re-seeds from the current store");

  stub.close();
});

// caveman-proxy points its JSON logger at stdout, the same stream the payload
// uses. A log line ahead of the object must not silence token reporting — and a
// log object must never be mistaken for the payload, which would read as a
// rewound store and replay lifetime history.
test("a log line on the proxy's stdout does not break or poison the token read", async (t) => {
  if (process.platform === "win32") {
    t.skip("sh stub is POSIX-only");
    return;
  }
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const iso = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
    // Same generous budget as stubProxyStats: this test hand-rolls its stub
    // bin, and the 400ms default flakes under test concurrency.
    CAVEMAN_TELEMETRY_TOKEN_READ_TIMEOUT_MS: "10000",
  });
  const bin = join(mkdtempSync(join(tmpdir(), "cave-bin-")), "caveman-proxy");
  const noisy = [
    '{"time":"2026-08-16T00:00:00Z","level":"WARN","msg":"store migration applied"}',
    JSON.stringify({ tokens_in: 5000, compression_tokens_saved: 1000, basis: "inferred" }, null, 2),
    // A deferred Close() error prints after the payload, so the scan cannot
    // assume the object runs to the end of the stream.
    '{"time":"2026-08-16T00:00:01Z","level":"WARN","msg":"close failed"}',
  ].join("\n");
  writeFileSync(bin, `#!/bin/sh\ncat <<'CAVE_EOF'\n${noisy}\nCAVE_EOF\n`, { mode: 0o755 });
  chmodSync(bin, 0o755);
  writeFileSync(join(iso.caveDir, "caveman.db"), "");
  const configDir = join(iso.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    telemetryTokens: { tokensIn: 4000, tokensSaved: 800, at: "2026-08-01T00:00:00.000Z" },
  }));

  const out = await runCli(["version"], { ...iso.env, CAVEMAN_PROXY_BIN: bin });
  assert.equal(out.code, 0, out.stderr);
  const event = JSON.parse(stub.posts[0].body)[0];
  assert.equal(event.tokens_processed, 1000, "the payload must still be found behind the log line");
  assert.equal(event.tokens_saved, 200);
  const watermark = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8")).telemetryTokens;
  assert.equal(watermark.tokensIn, 5000, "the log object must never become the watermark");

  stub.close();
});

test("a rewound proxy store re-baselines instead of replaying or going negative", async (t) => {
  if (process.platform === "win32") {
    t.skip("sh stub is POSIX-only");
    return;
  }
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const iso = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });
  const configDir = join(iso.home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    telemetryTokens: { tokensIn: 500000, tokensSaved: 90000, at: "2026-08-01T00:00:00.000Z" },
  }));
  const env = stubProxyStats(iso, { tokensIn: 1200, tokensSaved: 300 });

  const out = await runCli(["version"], env);
  assert.equal(out.code, 0, out.stderr);
  const event = JSON.parse(stub.posts[0].body)[0];
  assert.ok(!("tokens_processed" in event), "a deleted/restored store must not report its history a second time");
  assert.ok(!("tokens_saved" in event));
  const watermark = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8")).telemetryTokens;
  assert.equal(watermark.tokensIn, 1200, "the watermark re-baselines to the smaller store");
  assert.equal(watermark.tokensSaved, 300);

  stub.close();
});

test("a missing proxy binary drops the token fields, not the event", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env } = isolatedEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
    CAVEMAN_PROXY_BIN: join(tmpdir(), "missing-caveman-proxy"),
  });

  const out = await runCli(["version"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(stub.posts.length, 1);
  const event = JSON.parse(stub.posts[0].body)[0];
  assert.equal(event.command, "version");
  assert.ok(!("tokens_processed" in event), "no local store means no token claim, not a zero");

  stub.close();
});

// A v3 "yes" was consent for command counts alone. v4 widened the payload with
// token volume, so the decision stands but the new wording prints once.
test("a stale-version opt-in is re-disclosed once and never re-asked", async (t) => {
  const stub = startTelemetryStub();
  const port = await listenOrSkip(t, stub);
  if (port === null) return;
  const { env, home } = isolatedEnv({ CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry` });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const optIn = {
    enabled: true,
    anonymousId: "123e4567-e89b-12d3-a456-426614174000",
    decidedAt: "2026-07-03T00:00:00.000Z",
    promptVersion: 3,
  };
  writeFileSync(join(configDir, "config.json"), JSON.stringify({ telemetry: optIn }));

  const first = await runCliPty(["tools", "config", "get"], env);
  if (first === null || first.code !== 0) {
    stub.close();
    t.skip("script(1) pty unavailable in this environment");
    return;
  }
  assert.match(first.output, /token totals/, "the widened scope must be disclosed");
  assert.doesNotMatch(first.output, /\[y\/N\]/, "an existing decision is never re-asked");
  const cfg = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.equal(cfg.telemetry.promptVersion, 4);
  assert.equal(cfg.telemetry.anonymousId, optIn.anonymousId, "re-disclosure must not rotate the id");
  assert.equal(cfg.telemetry.decidedAt, optIn.decidedAt, "the original decision date stands");

  const second = await runCliPty(["tools", "config", "get"], env);
  assert.ok(second && second.code === 0, "second run failed");
  assert.doesNotMatch(second.output, /anonymous usage stats on/, "re-disclosure prints once, not per run");

  stub.close();
});

test("logout preserves telemetry config", async () => {
  const { env, home } = isolatedEnv({ CAVE_NO_KEYCHAIN: "1" });
  const configDir = join(home, ".caveman-cloud");
  mkdirSync(configDir, { recursive: true });
  const telemetry = {
    enabled: true,
    anonymousId: "123e4567-e89b-12d3-a456-426614174000",
    decidedAt: "2026-07-03T00:00:00.000Z",
    promptVersion: 1,
  };
  writeFileSync(join(configDir, "config.json"), JSON.stringify({
    baseURL: "http://localhost:8080",
    token: "legacy-token",
    tokenStore: "file",
    gatewayUrl: "https://gateway.example.com",
    telemetry,
    futureField: { keep: true },
  }, null, 2));

  const out = await runCli(["logout"], env);
  assert.equal(out.code, 0, out.stderr);
  const cfg = JSON.parse(readFileSync(join(configDir, "config.json"), "utf8"));
  assert.deepEqual(cfg.telemetry, telemetry, "logout must not wipe consent");
  assert.deepEqual(cfg.futureField, { keep: true }, "saveConfig must preserve unknown config fields");
  assert.ok(!("token" in cfg), "logout still clears legacy inline token");
  assert.ok(!("gatewayUrl" in cfg), "logout still clears managed gateway URL");
});
