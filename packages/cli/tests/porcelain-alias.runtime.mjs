import { test } from "node:test";
import assert from "node:assert";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { isolatedCliEnv, runCli, runCliWithApi } from "./_cli.mjs";

function normalizePrefix(value, group, verb) {
  return value.replaceAll(`caveman ${group} ${verb}`, `caveman ${verb}`);
}

function genericApiResponse(req) {
  if (req.url === "/api/v1/billing/account") {
    return {
      body: {
        gainshare_bps: 0,
        mtd_fee_cents: 0,
        currency: "usd",
        connected: false,
        status: "off",
        billing_enabled: false,
      },
    };
  }
  if (req.url === "/api/v1/projects/proj-alias/cave-plan") {
    return { body: { headline: { base: 0, low: 0, high: 0, basis: "inferred", move_count: 0 }, moves: [] } };
  }
  if (req.url?.startsWith("/api/v1/metering/receipts")) return { body: { receipts: [] } };
  return { body: {} };
}

const cloudCases = [
  { verb: "whoami", argv: [], method: "GET", path: "/api/v1/auth/me" },
  { verb: "projects", argv: ["list"], method: "GET", path: "/api/v1/projects" },
  { verb: "keys", argv: ["revoke", "key-7"], method: "POST", path: "/api/v1/projects/proj-alias/keys/key-7/revoke", body: "{}" },
  { verb: "providers", argv: ["verify", "provider-7"], method: "POST", path: "/api/v1/projects/proj-alias/providers/provider-7/verify", body: "{}" },
  { verb: "billing", argv: ["status"], method: "GET", path: "/api/v1/billing/account" },
  { verb: "score", argv: [], method: "GET", path: "/api/v1/reports/cave-score" },
  { verb: "costs", argv: [], method: "GET", path: "/api/v1/reports/costs" },
  { verb: "plan", argv: ["--json"], method: "GET", path: "/api/v1/projects/proj-alias/cave-plan" },
  { verb: "traces", argv: ["show", "trace-7"], method: "GET", path: "/api/v1/traces/trace-7?project_id=proj-alias" },
  { verb: "experiments", argv: ["list"], method: "GET", path: "/api/v1/experiments?project_id=proj-alias" },
  { verb: "audit", argv: ["report", "audit-7"], method: "GET", path: "/api/v1/audits/audit-7" },
  { verb: "agent", argv: ["run", "proposal-7"], method: "POST", path: "/api/v1/optimization-proposals/proposal-7/run", body: "{}" },
];

for (const item of cloudCases) {
  test(`cloud alias preserves ${item.verb} argv and HTTP contract`, async () => {
    const legacy = await runCliWithApi([item.verb, ...item.argv], { respond: genericApiResponse });
    const grouped = await runCliWithApi([item.verb, ...item.argv], { prefix: "cloud", respond: genericApiResponse });
    assert.equal(legacy.code, grouped.code);
    assert.equal(normalizePrefix(grouped.stdout, "cloud", item.verb), legacy.stdout);
    assert.equal(normalizePrefix(grouped.stderr, "cloud", item.verb), legacy.stderr);
    for (const run of [legacy, grouped]) {
      assert.equal(run.requests.length, 1, `${item.verb} must issue one authenticated request`);
      assert.equal(run.requests[0].method, item.method);
      assert.equal(run.requests[0].path, item.path);
      assert.equal(run.requests[0].headers.authorization, "Bearer alias-matrix-token");
      assert.equal(run.requests[0].body, item.body ?? "");
    }
  });
}

test("plan rejects removed engineer voice instead of silently rendering operator output", async () => {
  for (const args of [["plan", "--engineer"], ["cloud", "plan", "--engineer"], ["plan", "--json", "--json"]]) {
    const out = await runCli(args);
    assert.equal(out.code, 2, `${args.join(" ")} exit code`);
    assert.match(out.stderr, /usage: caveman (?:cloud )?plan \[--json\]/);
  }
});

test("cloud receipts export preserves query and body-free GET", async () => {
  const firstOut = join(mkdtempSync(join(tmpdir(), "cave-alias-receipts-")), "legacy.json");
  const secondOut = join(mkdtempSync(join(tmpdir(), "cave-alias-receipts-")), "grouped.json");
  const legacy = await runCliWithApi(["receipts", "export", "--since", "2026-07-01", "--until", "2026-07-02", "-o", firstOut], { respond: genericApiResponse });
  const grouped = await runCliWithApi(["receipts", "export", "--since", "2026-07-01", "--until", "2026-07-02", "-o", secondOut], { prefix: "cloud", respond: genericApiResponse });
  assert.equal(legacy.code, 0, legacy.stderr);
  assert.equal(grouped.code, 0, grouped.stderr);
  for (const run of [legacy, grouped]) {
    assert.equal(run.requests.length, 1);
    assert.equal(run.requests[0].method, "GET");
    assert.equal(run.requests[0].path, "/api/v1/metering/receipts?since=2026-07-01&until=2026-07-02");
    assert.equal(run.requests[0].body, "");
  }
  assert.equal(readFileSync(firstOut, "utf8"), readFileSync(secondOut, "utf8"));
});

function seedSyncDb({ env }) {
  const db = new DatabaseSync(join(env.CAVEMAN_HOME, "caveman.db"));
  db.exec(`CREATE TABLE requests (
    id INTEGER PRIMARY KEY,
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
  );
  INSERT INTO requests VALUES (
    1, '2026-07-26 12:00:00.000', 'request-alias', 'trace-alias',
    'claude', 'anthropic', 'claude-test', 200, '', 10, 100, 50,
    12, 2, 0, 0.01, 0.001, 'inferred', 'compress', 's4', 20, 12
  );`);
  db.close();
}

test("cloud sync alias preserves authenticated import path and JSONL body", async () => {
  const options = {
    prepare: seedSyncDb,
    respond(req, body) {
      return req.url?.startsWith("/api/v1/imports")
        ? { body: { status: "completed", row_count: body.trim().split("\n").length } }
        : genericApiResponse(req);
    },
  };
  const legacy = await runCliWithApi(["sync"], options);
  const grouped = await runCliWithApi(["sync"], { ...options, prefix: "cloud" });
  assert.equal(legacy.code, 0, legacy.stderr);
  assert.equal(grouped.code, 0, grouped.stderr);
  assert.equal(grouped.stdout, legacy.stdout);
  for (const run of [legacy, grouped]) {
    assert.equal(run.requests.length, 1);
    assert.equal(run.requests[0].method, "POST");
    assert.equal(run.requests[0].path, "/api/v1/imports?format=caveman-jsonl");
    assert.equal(run.requests[0].headers.authorization, "Bearer alias-matrix-token");
    const row = JSON.parse(run.requests[0].body.trim());
    assert.equal(row.span_id, "request-alias");
    assert.equal(row.attributes["cave.basis"], "inferred");
  }
  assert.equal(grouped.requests[0].body, legacy.requests[0].body);
});

test("audit import and receipts verify resolve file after rebased subcommand", async () => {
  const input = join(mkdtempSync(join(tmpdir(), "cave-alias-audit-")), "events.jsonl");
  writeFileSync(input, "{\"event\":\"sentinel\"}\n");
  const auditLegacy = await runCliWithApi(["audit", "import", "--format", "caveman-jsonl", input], { respond: genericApiResponse });
  const auditGrouped = await runCliWithApi(["audit", "import", "--format", "caveman-jsonl", input], { prefix: "cloud", respond: genericApiResponse });
  for (const run of [auditLegacy, auditGrouped]) {
    assert.equal(run.code, 0, run.stderr);
    assert.equal(run.requests.length, 1);
    assert.equal(run.requests[0].method, "POST");
    assert.equal(run.requests[0].path, "/api/v1/imports?format=caveman-jsonl");
    assert.equal(run.requests[0].body, "{\"event\":\"sentinel\"}\n");
  }

  const receipt = join(mkdtempSync(join(tmpdir(), "cave-alias-bundle-")), "bundle.json");
  const emptyKey = { key_id: "empty", alg: "Ed25519", key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" };
  writeFileSync(receipt, JSON.stringify({
    schema: "caveman.receipt-bundle.v2",
    public_key: emptyKey,
    public_keys: [emptyKey],
    receipts: [],
  }));
  const isolated = isolatedCliEnv();
  try {
    const receiptLegacy = await runCli(["receipts", "verify", receipt], { env: isolated.env });
    const receiptGrouped = await runCli(["receipts", "verify", receipt], { env: isolated.env, prefix: "cloud" });
    assert.equal(receiptLegacy.code, 0, receiptLegacy.stderr);
    assert.equal(receiptGrouped.code, 0, receiptGrouped.stderr);
    assert.equal(receiptGrouped.stdout, receiptLegacy.stdout);
  } finally {
    isolated.cleanup();
  }
});

test("audit eval-import preserves JSONL records and server-owned authority", async () => {
  const input = join(mkdtempSync(join(tmpdir(), "cave-alias-evals-")), "evidence.jsonl");
  writeFileSync(input, `${JSON.stringify({
    observed_at: "2026-08-09T10:00:00Z",
    result_at: "2026-08-09T10:00:01Z",
    source_system: "posthog",
    evaluator_name: "activation",
    evaluator_type: "boolean",
    evaluator_version: "1",
    evaluator_config_digest: "a".repeat(64),
    status: "processed",
    passed: true,
    completeness: "complete",
    idempotency_key: "run-1/case-1",
  })}\n`);
  const options = {
    respond(req) {
      return req.url === "/api/v1/eval-evidence/batches"
        ? { status: 201, body: { accepted: 1, rejected: 0, duplicate: 0, conflict: 0, dry_run: true, errors: [] } }
        : genericApiResponse(req);
    },
  };
  const legacy = await runCliWithApi(["audit", "eval-import", input, "--dry-run"], options);
  const grouped = await runCliWithApi(["audit", "eval-import", input, "--dry-run"], { ...options, prefix: "cloud" });
  for (const run of [legacy, grouped]) {
    assert.equal(run.code, 0, run.stderr);
    assert.equal(run.requests.length, 1);
    assert.equal(run.requests[0].path, "/api/v1/eval-evidence/batches");
    const body = JSON.parse(run.requests[0].body);
    assert.equal(body.project_id, "proj-alias");
    assert.equal(body.dry_run, true);
    assert.equal(body.items.length, 1);
    assert.equal(body.items[0].source_system, "posthog");
    assert.equal(body.items[0].authority, undefined);
    assert.equal(body.items[0].basis, undefined);
  }
  assert.equal(grouped.stdout, legacy.stdout);
});

const positionalLocalCases = [
  { legacy: ["mcp", "install", "claude"], grouped: ["mcp", "install", "claude"], verb: "mcp" },
  { legacy: ["learn", "apply", "sink-7"], grouped: ["learn", "apply", "sink-7"], verb: "learn", prefix: "" },
  { legacy: ["retrieve", "handle-7"], grouped: ["retrieve", "handle-7"], verb: "retrieve" },
  { legacy: ["snippets", "openai-ts"], grouped: ["sdk", "snippets", "openai-ts"], verb: "sdk" },
];

for (const item of positionalLocalCases) {
  test(`positional alias stays rebased for ${item.verb}`, async () => {
    const legacyEnv = isolatedCliEnv();
    const groupedEnv = isolatedCliEnv();
    try {
      const legacy = await runCli(item.legacy, { env: legacyEnv.env });
      const grouped = item.prefix === ""
        ? await runCli(item.grouped, { env: groupedEnv.env })
        : await runCli(item.grouped, { env: groupedEnv.env, prefix: "tools" });
      assert.equal(grouped.code, legacy.code);
      assert.equal(normalizePrefix(grouped.stdout, "tools", item.verb), legacy.stdout);
      assert.equal(normalizePrefix(grouped.stderr, "tools", item.verb), legacy.stderr);
    } finally {
      legacyEnv.cleanup();
      groupedEnv.cleanup();
    }
  });
}
