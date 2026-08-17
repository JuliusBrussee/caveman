import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdirSync, mkdtempSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function listen(server) {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
}

// startImportStub runs a minimal control-api imports endpoint that records
// every POST body + URL it receives and answers like the real handler.
function startImportStub() {
  const imports = [];
  const localScans = [];
  const practiceFindings = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      if (req.method === "POST" && req.url.startsWith("/api/v1/imports")) {
        const format = new URL(req.url, "http://stub").searchParams.get("format");
        if (format === "local-scan") {
          const parsed = JSON.parse(body);
          localScans.push({ url: req.url, auth: req.headers.authorization, body, parsed });
          res.writeHead(200, { "content-type": "application/json" });
          res.end(JSON.stringify({
            id: "local_scan_1",
            project_id: "project-1",
            status: "completed",
            row_count: 0,
            format: "local_scan",
            source: "local_scan",
            basis: "inferred",
          }));
          return;
        }
        const rows = body.split("\n").filter((l) => l.trim().length > 0);
        imports.push({ url: req.url, auth: req.headers.authorization, body, rowCount: rows.length });
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ id: "imp_1", status: "completed", row_count: rows.length, format: "caveman-jsonl" }));
        return;
      }
      if (req.method === "POST" && req.url.startsWith("/api/v1/practice-findings")) {
        const parsed = JSON.parse(body);
        practiceFindings.push({ url: req.url, auth: req.headers.authorization, body, parsed });
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ status: "completed", row_count: parsed.findings.length, basis: "inferred" }));
        return;
      }
      res.writeHead(404, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { code: "cave_not_found", message: "not found" } }));
    });
  });
  return { server, imports, localScans, practiceFindings };
}

function writePendingLocalScan(home) {
  const cloudDir = join(home, ".caveman-cloud");
  mkdirSync(cloudDir, { recursive: true });
  writeFileSync(join(cloudDir, "local-scan.json"), JSON.stringify({
    schema: "caveman.local_scan.pending.v1",
    payload: {
      schema: "caveman.local_scan.v1",
      source: "local_scan",
      basis: "inferred",
      scanned_at: "2026-08-10T08:00:00.000Z",
      window_days: 30,
      sessions_total: 3,
      sessions_scanned: 3,
      turns_observed: 12,
      tokens_observed: 10_000,
      tokens_observed_source: "session_usage",
      would_cut_tokens: 2_000,
      would_cut_stream_tokens: 3_000,
      families: [{ id: "tool_outputs", tokens: 2_000 }],
      config_prefix_tokens_per_turn: 100,
      engine_used: true,
      time_boxed: false,
    },
  }));
}

// makeSpendDb creates a local spend store shaped like the standalone proxy's
// requests table (only the columns sync reads) and returns an inserter.
function makeSpendDb(caveDir) {
  const db = new DatabaseSync(join(caveDir, "caveman.db"));
  db.exec(`CREATE TABLE requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,
    request_id TEXT NOT NULL,
    trace_id TEXT,
    agent_slug TEXT,
    provider TEXT,
    model TEXT,
    status_code INTEGER,
    error_code TEXT,
    latency_ms INTEGER,
    request_bytes INTEGER,
    response_bytes INTEGER,
    input_tokens INTEGER,
    output_tokens INTEGER,
    cached_input_tokens INTEGER,
    total_cost_usd REAL,
    savings_usd REAL,
    basis TEXT NOT NULL,
    runtime_mode TEXT,
    optimization_ids TEXT,
    compression_tokens_before INTEGER,
    compression_tokens_after INTEGER
  )`);
  const stmt = db.prepare(`INSERT INTO requests (
    ts, request_id, trace_id, agent_slug, provider, model, status_code, error_code,
    latency_ms, request_bytes, response_bytes, input_tokens, output_tokens,
    cached_input_tokens, total_cost_usd, savings_usd, basis, runtime_mode,
    optimization_ids, compression_tokens_before, compression_tokens_after
  ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`);
  const insert = (requestId, tokensBefore, tokensAfter) =>
    stmt.run(
      "2026-07-01 10:00:00.000", requestId, "trace-" + requestId, "claude-code", "anthropic",
      "claude-sonnet-4-5", 200, "", 850, 2048, 512, 500, 120, 300, 0.004, 0.0011,
      "inferred", "compress", "s4_compress", tokensBefore, tokensAfter,
    );
  return { db, insert };
}

// sync with no credentials must degrade like every connected verb: one
// actionable line, non-zero exit, no stack trace.
test("sync without login exits non-zero with a login hint", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir };
  delete env.CAVE_TOKEN;

  const out = await runCli(["sync"], env);
  assert.notEqual(out.code, 0, "logged-out sync must exit non-zero");
  assert.match(out.stderr, /caveman login/, "must hint at `caveman login`");
});

// Happy path + idempotency: first sync uploads all local spans as caveman-jsonl
// (labeled inferred), a second sync is a no-op (watermark), and new local rows
// sync incrementally — never re-uploading what the server already accepted.
test("sync uploads local spans, advances the watermark, and stays idempotent", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const { db, insert } = makeSpendDb(caveDir);
  insert("req-1", 1000, 400);
  insert("req-2", 2000, 1200);

  const first = await runCli(["sync"], env);
  assert.equal(first.code, 0, `sync failed: ${first.stderr}`);
  assert.match(first.stdout, /synced 2 spans/, "must report the span count");
  assert.match(first.stdout, /1400 estimated tokens saved \(inferred; counter basis unavailable\)/, "estimated savings must disclose the unavailable counter basis and remain inferred");
  assert.equal(imports.length, 1, "exactly one import POST");
  assert.match(imports[0].url, /format=caveman-jsonl/, "must post the caveman-jsonl format");
  assert.equal(imports[0].auth, "Bearer ci-token", "must send the logged-in credentials");
  assert.equal(imports[0].rowCount, 2, "must upload one jsonl line per span");
  const span = JSON.parse(imports[0].body.split("\n")[0]);
  assert.equal(span.span_id, "req-1");
  assert.equal(span.timestamp, "2026-07-01 10:00:00.000");
  assert.equal(span.input_tokens, 500);
  assert.equal(span.attributes["cave.basis"], "inferred", "every synced span must carry the inferred basis");
  assert.ok(!("organization_id" in span) || !span.organization_id, "the file must never assert a tenant scope");

  // Second run: nothing new — no POST, honest no-op.
  const second = await runCli(["sync"], env);
  assert.equal(second.code, 0, `empty sync failed: ${second.stderr}`);
  assert.match(second.stdout, /nothing new to sync/);
  assert.match(second.stdout, /inferred/, "even the no-op line keeps the inferred label");
  assert.equal(imports.length, 1, "an empty sync must not POST");

  // A new local span syncs incrementally from the watermark.
  insert("req-3", 500, 100);
  db.close();
  const third = await runCli(["sync"], env);
  assert.equal(third.code, 0, `incremental sync failed: ${third.stderr}`);
  assert.match(third.stdout, /synced 1 spans? · 400 estimated tokens saved \(inferred; counter basis unavailable\)/);
  assert.equal(imports.length, 2);
  assert.equal(imports[1].rowCount, 1, "must only upload the span past the watermark");
  assert.match(imports[1].body, /"span_id":"req-3"/);

  // The watermark is persisted in the CLI's state dir.
  const state = JSON.parse(readFileSync(join(home, ".caveman-cloud", "sync.json"), "utf8"));
  assert.equal(Object.values(state.watermarks)[0], 3, "watermark must sit at the last confirmed rowid");

  server.close();
});

// No local spend store at all: a clean no-op, exit 0, no POST, no crash.
test("sync with no local spend store is a safe no-op", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const out = await runCli(["sync"], env);
  assert.equal(out.code, 0, `sync failed: ${out.stderr}`);
  assert.match(out.stdout, /nothing to sync/);
  assert.match(out.stdout, /inferred/, "the hint keeps the inferred framing");
  assert.equal(imports.length, 0, "must not POST when there is no store");

  server.close();
});

test("sync uploads pending local retro aggregate once without private scan text", async () => {
  const { server, imports, localScans } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const cloudDir = join(home, ".caveman-cloud");
  mkdirSync(cloudDir, { recursive: true });
  writeFileSync(join(cloudDir, "local-scan.json"), JSON.stringify({
    schema: "caveman.local_scan.pending.v1",
    payload: {
      schema: "caveman.local_scan.v1",
      source: "local_scan",
      basis: "inferred",
      scanned_at: "2026-08-10T08:00:00.000Z",
      window_days: 30,
      sessions_total: 4,
      sessions_scanned: 3,
      turns_observed: 8,
      tokens_observed: 12000,
      tokens_observed_source: "session_usage",
      would_cut_tokens: 3000,
      would_cut_stream_tokens: 5000,
      families: [
        { id: "repeated_blocks", tokens: 1000, private_label: "/Users/alice/private" },
        { id: "tool_outputs", tokens: 2000 },
      ],
      config_prefix_tokens_per_turn: 300,
      engine_used: true,
      time_boxed: false,
      raw_prompt: "never upload this",
      caveats: ["private transcript path"],
    },
  }));
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: caveDir,
    CAVE_TOKEN: "ci-token",
    CAVE_API_URL: `http://127.0.0.1:${port}`,
  };

  const first = await runCli(["sync"], env);
  assert.equal(first.code, 0, `sync failed: ${first.stderr}`);
  assert.equal(imports.length, 0, "aggregate must not enter span import lane");
  assert.equal(localScans.length, 1, "pending scan must upload once");
  assert.equal(localScans[0].auth, "Bearer ci-token");
  assert.equal(localScans[0].parsed.basis, "inferred");
  assert.equal(localScans[0].parsed.source, "local_scan");
  assert.deepEqual(localScans[0].parsed.families, [
    { id: "tool_outputs", tokens: 2000 },
    { id: "repeated_blocks", tokens: 1000 },
  ]);
  assert.doesNotMatch(localScans[0].body, /alice|private|prompt|caveat|label/i);
  assert.match(first.stdout, /synced 30-day local scan · inferred token summary · separate from gateway spend and verified savings/);

  const state = JSON.parse(readFileSync(join(cloudDir, "local-scan.json"), "utf8"));
  assert.equal(state.delivered.import_id, "local_scan_1");
  const second = await runCli(["sync"], env);
  assert.equal(second.code, 0, second.stderr);
  assert.equal(localScans.length, 1, "delivered state must prevent a second POST");

  server.close();
});

test("sync still uploads pending local scan when local spend DB is corrupt", async () => {
  const { server, localScans } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  writeFileSync(join(caveDir, "caveman.db"), "not a sqlite database");
  writePendingLocalScan(home);

  const out = await runCli(["sync"], {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: caveDir,
    CAVE_TOKEN: "ci-token",
    CAVE_API_URL: `http://127.0.0.1:${port}`,
  });

  assert.notEqual(out.code, 0, "partial explicit sync must report failed span lane");
  assert.equal(localScans.length, 1, "corrupt spend DB must not starve local scan upload");
  assert.equal(localScans[0].auth, "Bearer ci-token");
  assert.match(out.stdout, /synced 30-day local scan · inferred token summary · separate from gateway spend and verified savings/);
  assert.match(out.stderr, /sync incomplete.*local spans/i);
  server.close();
});

test("sync uploads stable practice ids and token rates without local evidence", async () => {
  const { server, imports, practiceFindings } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: caveDir,
    CAVE_TOKEN: "ci-token",
    CAVE_API_URL: `http://127.0.0.1:${port}`,
  };

  const reports = join(caveDir, "reports");
  mkdirSync(reports, { recursive: true });
  writeFileSync(join(reports, "caveman-learn.json"), JSON.stringify({
    schema: "caveman.learn.v1",
    basis: "inferred",
    generated_at: "2026-07-26T11:59:00Z",
    sessions_scanned: 4,
    sinks: [
      {
        sink_id: "context_dumbzone",
        practice_id: "context-compression",
        tokens_per_turn: 120,
        tokens_per_day_rate: 960,
        title: "private local title",
        suggestion: "private local suggestion",
        evidence: { path: "/Users/alice/secret", raw: "never upload me" },
      },
      {
        sink_id: "config_surface",
        practice_id: "",
        tokens_per_turn: 0,
        tokens_per_day_rate: 0,
        evidence: { path: "/Users/alice/also-secret" },
      },
    ],
  }));

  const out = await runCli(["sync"], env);
  assert.equal(out.code, 0, `sync failed: ${out.stderr}`);
  assert.equal(imports.length, 0, "practice sync must not fabricate a spend import");
  assert.equal(practiceFindings.length, 1, "one current snapshot POST");
  assert.equal(practiceFindings[0].auth, "Bearer ci-token");
  assert.deepEqual(practiceFindings[0].parsed, {
    findings: [{
      sink_id: "context_dumbzone",
      practice_id: "context-compression",
      basis: "inferred",
      tokens_per_turn: 120,
      tokens_per_day_rate: 960,
      sessions_scanned: 4,
      observed_at: "2026-07-26T11:59:00.000Z",
    }],
  });
  assert.doesNotMatch(practiceFindings[0].body, /Users|secret|title|suggestion|evidence|raw/);
  assert.match(out.stdout, /synced 1 local practice findings? · basis: inferred \(tokens only; no payload evidence\)/);

  server.close();
});

// A failed import must NOT advance the watermark: the next sync retries the
// same spans instead of silently dropping them.
test("sync does not advance the watermark when the server rejects the import", async () => {
  const server = createServer((req, res) => {
    req.on("data", () => {});
    req.on("end", () => {
      res.writeHead(400, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { code: "cave_import_parse_failed", message: "bad import" } }));
    });
  });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const { db, insert } = makeSpendDb(caveDir);
  insert("req-1", 1000, 400);
  db.close();

  const failed = await runCli(["sync"], env);
  assert.notEqual(failed.code, 0, "a rejected import must exit non-zero");
  assert.match(failed.stderr, /bad import/, "must surface the server's error");

  const { server: okServer, imports } = startImportStub();
  const okPort = await listen(okServer);
  const retry = await runCli(["sync"], { ...env, CAVE_API_URL: `http://127.0.0.1:${okPort}` });
  assert.equal(retry.code, 0, `retry failed: ${retry.stderr}`);
  assert.equal(imports.length, 1, "the retry must re-send the unsynced span");
  assert.equal(imports[0].rowCount, 1);

  server.close();
  okServer.close();
});

// After a successful login, the CLI runs the same sync once automatically and
// tells the user what happened (the funnel bridge).
test("login auto-syncs local inferred savings once", { skip: "Cloud login disabled during beta" }, async () => {
  const TOKEN = `${Buffer.from(JSON.stringify({ uid: "u1", oid: "org-test" })).toString("base64url")}.sig`;
  const imports = [];
  const localScans = [];
  const practicePosts = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const send = (code, obj) => {
        res.writeHead(code, { "content-type": "application/json" });
        res.end(JSON.stringify(obj));
      };
      if (req.url === "/api/v1/auth/device/code") {
        send(200, { device_code: "dev-1", user_code: "AAAA-1111", verification_uri: "http://stub/activate", expires_in: 60, interval: 0 });
      } else if (req.url === "/api/v1/auth/device/token") {
        send(200, { access_token: TOKEN, token_type: "Bearer", expires_in: 900 });
      } else if (req.method === "POST" && req.url.startsWith("/api/v1/imports")) {
        if (new URL(req.url, "http://stub").searchParams.get("format") === "local-scan") {
          localScans.push({ auth: req.headers.authorization, parsed: JSON.parse(body) });
          send(200, {
            id: "local_scan_login",
            project_id: "",
            status: "completed",
            row_count: 0,
            format: "local_scan",
            source: "local_scan",
            basis: "inferred",
          });
          return;
        }
        const rows = body.split("\n").filter((l) => l.trim().length > 0);
        imports.push({ auth: req.headers.authorization, rowCount: rows.length });
        send(200, { id: "imp_1", status: "completed", row_count: rows.length, format: "caveman-jsonl" });
      } else if (req.method === "POST" && req.url.startsWith("/api/v1/practice-findings")) {
        const parsed = JSON.parse(body);
        practicePosts.push({ auth: req.headers.authorization, parsed });
        send(200, { status: "completed", row_count: parsed.findings.length, basis: "inferred" });
      } else {
        send(404, {});
      }
    });
  });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;

  const { db, insert } = makeSpendDb(caveDir);
  insert("req-1", 1000, 400);
  db.close();
  mkdirSync(join(caveDir, "reports"), { recursive: true });
  writeFileSync(join(caveDir, "reports", "caveman-learn.json"), JSON.stringify({
    generated_at: "2026-07-26T11:59:00Z",
    sessions_scanned: 3,
    sinks: [{
      sink_id: "dead_load:skills",
      practice_id: "tool-schema-deferral",
      tokens_per_turn: 200,
      tokens_per_day_rate: 1200,
    }],
  }));
  writePendingLocalScan(home);

  const login = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(login.code, 0, `login failed: ${login.stderr}`);
  assert.equal(imports.length, 1, "login must run the sync once");
  assert.equal(imports[0].auth, `Bearer ${TOKEN}`, "the auto-sync must use the freshly stored token");
  assert.match(login.stderr, /synced 1 local spans? · 600 estimated tokens saved \(inferred; counter basis unavailable\)/, "login must disclose the estimated counter basis and keep savings inferred");
  assert.equal(practicePosts.length, 1, "login must sync current local practice findings");
  assert.equal(practicePosts[0].auth, `Bearer ${TOKEN}`);
  assert.equal(practicePosts[0].parsed.findings[0].practice_id, "tool-schema-deferral");
  assert.match(login.stderr, /synced 1 local practice findings? · inferred, tokens only/);
  assert.equal(localScans.length, 1, "login must upload pending first-run scan");
  assert.equal(localScans[0].auth, `Bearer ${TOKEN}`);
  assert.match(login.stderr, /synced 30-day local scan · inferred token summary · separate from gateway spend and verified savings/);

  server.close();
});

test("login still uploads pending local scan when local spend DB is corrupt", { skip: "Cloud login disabled during beta" }, async () => {
  const TOKEN = `${Buffer.from(JSON.stringify({ uid: "u1", oid: "org-test" })).toString("base64url")}.sig`;
  const localScans = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      const send = (code, value) => {
        res.writeHead(code, { "content-type": "application/json" });
        res.end(JSON.stringify(value));
      };
      if (req.url === "/api/v1/auth/device/code") {
        send(200, { device_code: "dev-1", user_code: "AAAA-1111", verification_uri: "http://stub/activate", expires_in: 60, interval: 0 });
      } else if (req.url === "/api/v1/auth/device/token") {
        send(200, { access_token: TOKEN, token_type: "Bearer", expires_in: 900 });
      } else if (req.method === "POST" && new URL(req.url, "http://stub").searchParams.get("format") === "local-scan") {
        localScans.push({ auth: req.headers.authorization, parsed: JSON.parse(body) });
        send(200, { id: "local_scan_login", project_id: "", status: "completed", row_count: 0, format: "local_scan", source: "local_scan", basis: "inferred" });
      } else {
        send(404, { error: { code: "cave_not_found", message: "not found" } });
      }
    });
  });
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  writeFileSync(join(caveDir, "caveman.db"), "not a sqlite database");
  writePendingLocalScan(home);
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_NO_KEYCHAIN: "1" };
  delete env.CAVE_TOKEN;

  const out = await runCli(["login", "--base-url", `http://127.0.0.1:${port}`], env);
  assert.equal(out.code, 0, `login must survive partial sync: ${out.stderr}`);
  assert.equal(localScans.length, 1, "corrupt spend DB must not starve post-login local scan");
  assert.equal(localScans[0].auth, `Bearer ${TOKEN}`);
  assert.match(out.stderr, /local spans sync skipped/i);
  assert.match(out.stderr, /synced 30-day local scan · inferred token summary · separate from gateway spend and verified savings/);
  server.close();
});

// A local DB reset (e.g. `make reset-local` wipes ~/.caveman) restarts rowids at
// 1. The watermark must be keyed to the DB's identity, so the next sync re-reads
// from the start instead of silently reporting "nothing new" and dropping spans.
test("sync re-reads from the start after the local DB is reset", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const first = makeSpendDb(caveDir);
  first.insert("req-1", 1000, 400);
  first.insert("req-2", 2000, 1200);
  first.db.close();

  const run1 = await runCli(["sync"], env);
  assert.equal(run1.code, 0, `first sync failed: ${run1.stderr}`);
  assert.match(run1.stdout, /synced 2 spans/, "first sync uploads both spans");
  assert.equal(imports.length, 1, "one import so far");

  // Simulate `make reset-local`: delete the DB (and any sidecar files), then a
  // fresh proxy recreates it — rowids restart at 1.
  const dbFile = join(caveDir, "caveman.db");
  unlinkSync(dbFile);
  for (const ext of ["-wal", "-shm", "-journal"]) {
    try { unlinkSync(dbFile + ext); } catch { /* not present */ }
  }
  const second = makeSpendDb(caveDir);
  second.insert("reset-1", 500, 100); // id 1 in the fresh DB — <= the old watermark of 2
  second.insert("reset-2", 800, 200); // id 2 in the fresh DB
  second.db.close();

  const run2 = await runCli(["sync"], env);
  assert.equal(run2.code, 0, `post-reset sync failed: ${run2.stderr}`);
  assert.doesNotMatch(run2.stdout, /nothing new/, "a reset DB must not be mistaken for 'already up to date'");
  assert.match(run2.stdout, /synced 2 spans/, "both spans from the recreated DB must upload");
  assert.equal(imports.length, 2, "the reset must trigger a second import POST");
  assert.equal(imports[1].rowCount, 2, "both fresh rows upload, not skipped by the stale watermark");
  assert.match(imports[1].body, /"span_id":"reset-1"/);

  server.close();
});

// Locally compressed SUBSCRIPTION traffic (Claude Pro/Max) must sync as tokens:
// the span has to carry auth_mode + the compression before/after + the o200k
// counter basis so the cloud can aggregate token savings by auth mode — and it
// must never carry a dollar figure, because a seat has no per-token price.
// (honesty rule: no-fake-savings)
test("sync carries subscription token savings with auth_mode and zero dollars", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  // Full-shape store: the columns the standalone proxy actually writes, including
  // the three the older fixture omits (token_usage_basis, auth_mode, counter basis).
  const db = new DatabaseSync(join(caveDir, "caveman.db"));
  db.exec(`CREATE TABLE requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL, request_id TEXT NOT NULL, trace_id TEXT, agent_slug TEXT,
    provider TEXT, model TEXT, status_code INTEGER, error_code TEXT, latency_ms INTEGER,
    request_bytes INTEGER, response_bytes INTEGER, input_tokens INTEGER, output_tokens INTEGER,
    cached_input_tokens INTEGER, total_cost_usd REAL, savings_usd REAL, basis TEXT NOT NULL,
    token_usage_basis TEXT, auth_mode TEXT, runtime_mode TEXT, optimization_ids TEXT,
    compression_tokens_before INTEGER, compression_tokens_after INTEGER,
    compression_token_count_basis TEXT
  )`);
  db.prepare(`INSERT INTO requests (
    ts, request_id, trace_id, agent_slug, provider, model, status_code, error_code, latency_ms,
    request_bytes, response_bytes, input_tokens, output_tokens, cached_input_tokens,
    total_cost_usd, savings_usd, basis, token_usage_basis, auth_mode, runtime_mode,
    optimization_ids, compression_tokens_before, compression_tokens_after, compression_token_count_basis
  ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`).run(
    "2026-07-25 10:00:00.000", "req-sub", "trace-sub", "claude-code", "anthropic", "claude-sonnet-4-5",
    200, "", 900, 4096, 700, 900, 150, 0,
    // The local store zeroes cost + savings for subscription rows (no list price).
    0, 0, "inferred", "provider_complete", "subscription", "compress",
    "caveman-compression", 3000, 1100, "estimated_engine_o200k",
  );
  db.close();

  const out = await runCli(["sync"], env);
  assert.equal(out.code, 0, `sync failed: ${out.stderr}`);
  assert.equal(imports.length, 1, "one import POST");
  const span = JSON.parse(imports[0].body.split("\n")[0]);
  assert.equal(span.attributes["cave.auth_mode"], "subscription", "the cloud aggregates tokens by auth mode — the label must ride along");
  assert.equal(span.attributes["cave.compression_tokens_before"], "3000");
  assert.equal(span.attributes["cave.compression_tokens_after"], "1100");
  assert.equal(span.attributes["cave.compression_token_count_basis"], "estimated_engine_o200k", "token counts must declare the local o200k estimator");
  assert.equal(span.attributes["cave.cache_creation_input_tokens"], "0", "cache-write counter must survive the sync import path");
  assert.equal(span.attributes["cave.basis"], "inferred");
  assert.equal(span.attributes["cave.savings_usd"], "0", "subscription savings are tokens — never dollars");
  assert.equal(span.total_cost_usd, 0, "subscription traffic has no per-token price");
  assert.match(out.stdout, /1900 estimated tokens saved \(inferred; counter basis estimated_engine_o200k\)/);
  // The disclosure that imports never touch managed money must survive, and now
  // also names the tokens-only rule for subscription sessions.
  assert.match(out.stdout, /never affect managed budgets, verified savings, or billing/);
  assert.match(out.stdout, /Subscription\/OAuth sessions carry token counts only — no dollar figure/);

  server.close();
});

test("sync refuses a compression headline when cache writes exceed cache reads", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const { db, insert } = makeSpendDb(caveDir);
  db.exec("ALTER TABLE requests ADD COLUMN cache_creation_input_tokens INTEGER");
  insert("req-cache-write-heavy", 1000, 400);
  db.prepare(`UPDATE requests
    SET input_tokens = 200000, cached_input_tokens = 50000, cache_creation_input_tokens = 144000
    WHERE request_id = ?`).run("req-cache-write-heavy");
  db.close();

  const out = await runCli(["sync"], env);
  assert.equal(out.code, 0, `sync failed: ${out.stderr}`);
  assert.match(out.stdout, /compression headline refused: cache writes 144000 > cache reads 50000 \(inferred; no savings headline\)/);
  assert.doesNotMatch(out.stdout, /600 estimated tokens saved/, "cache-write-heavy traffic must not print a savings headline");
  assert.equal(imports.length, 1, "refusal does not discard telemetry");
  const span = JSON.parse(imports[0].body.split("\n")[0]);
  assert.equal(span.attributes["cave.cache_creation_input_tokens"], "144000", "cache writes must reach imported span metadata");
  assert.equal(span.cached_input_tokens, 50000, "cache reads remain in the first-class span counter");

  server.close();
});

// Review M12: the wrap-directive label is bounded by the marker's installed_at.
// Rows synced from BEFORE the install are backlog — not evidence about the
// directive — and must carry NO cave.directives attribute; rows at/after the
// install carry it. A marker without a parseable installed_at labels nothing.
test("sync labels only rows created at or after a directive's install", async () => {
  const { server, imports } = startImportStub();
  const port = await listen(server);
  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const caveDir = mkdtempSync(join(tmpdir(), "cave-dot-"));
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveDir, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const { db, insert } = makeSpendDb(caveDir); // fixture rows are ts 2026-07-01
  insert("req-backlog", 1000, 400);
  db.prepare(`INSERT INTO requests (
    ts, request_id, trace_id, agent_slug, provider, model, status_code, error_code,
    latency_ms, request_bytes, response_bytes, input_tokens, output_tokens,
    cached_input_tokens, total_cost_usd, savings_usd, basis, runtime_mode,
    optimization_ids, compression_tokens_before, compression_tokens_after
  ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`).run(
    "2026-07-20 10:00:00.000", "req-after", "trace-after", "claude-code", "anthropic",
    "claude-sonnet-4-5", 200, "", 850, 2048, 512, 500, 120, 300, 0.004, 0.0011,
    "inferred", "compress", "s4_compress", 2000, 1200,
  );
  db.close();

  // Marker installed BETWEEN the two rows' timestamps.
  const markerDir = join(caveDir, "hooks", "directives");
  mkdirSync(markerDir, { recursive: true });
  writeFileSync(join(markerDir, "exploration-offload-directive.json"),
    JSON.stringify({ installed: true, installed_at: "2026-07-10T00:00:00.000Z", agent: "codex", file: "~/.codex/AGENTS.md" }) + "\n");
  // A second marker WITHOUT installed_at (pre-timestamp shape): labels nothing.
  writeFileSync(join(markerDir, "deferred-tool-loading.json"), JSON.stringify({ installed: true }) + "\n");

  const out = await runCli(["sync"], env);
  assert.equal(out.code, 0, `sync failed: ${out.stderr}`);
  const lines = imports[0].body.split("\n").filter((l) => l.trim());
  const spans = lines.map((l) => JSON.parse(l));
  const backlog = spans.find((s) => s.span_id === "req-backlog");
  const after = spans.find((s) => s.span_id === "req-after");
  assert.equal(backlog.attributes["cave.directives"], undefined, "pre-install backlog rows must carry no directive label");
  assert.equal(after.attributes["cave.directives"], "exploration-offload-directive", "post-install rows carry the directive id; a marker without installed_at labels nothing");

  server.close();
});
