import { test } from "node:test";
import assert from "node:assert";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { isolatedCliEnv, runCli, runCliWithApi } from "./_cli.mjs";

function normalizePrefix(value, group, verb) {
  return value.replaceAll(`caveman ${group} ${verb}`, `caveman ${verb}`);
}

function genericApiResponse() {
  return { body: {} };
}

test("cloud alias preserves whoami argv and HTTP contract", async () => {
  const legacy = await runCliWithApi(["whoami"], { respond: genericApiResponse });
  const grouped = await runCliWithApi(["whoami"], { prefix: "cloud", respond: genericApiResponse });
  assert.equal(legacy.code, grouped.code);
  assert.equal(grouped.stdout, legacy.stdout);
  for (const run of [legacy, grouped]) {
    assert.equal(run.requests.length, 1);
    assert.equal(run.requests[0].method, "GET");
    assert.equal(run.requests[0].path, "/api/v1/auth/me");
    assert.equal(run.requests[0].headers.authorization, "Bearer alias-matrix-token");
  }
});

const movedCases = [
  { argv: ["projects", "list"], line: "caveman projects moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm projects list" },
  { argv: ["traces", "show", "trace-7"], line: "caveman traces show moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm traces get" },
  { argv: ["agent", "factory", "show", "a-1"], line: "caveman agent factory show moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm workflows get_agent" },
  { argv: ["keys"], line: "caveman keys moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm human list_keys" },
  { argv: ["keys", "revoke", "key-7"], line: "caveman keys revoke was removed. Use the Caveman Cloud dashboard instead." },
  { argv: ["billing", "charges"], line: "caveman billing charges was removed. Use the Caveman Cloud dashboard instead." },
  { argv: ["mcp-serve"], line: "caveman mcp-serve moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm mcp" },
  { argv: ["audit"], line: "caveman audit moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm human create_audit" },
  { argv: ["receipts", "export", "-o", "x.json"], line: "caveman receipts export moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm human metering_receipts" },
  { argv: ["audit", "report", "audit-7"], line: "caveman audit report moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm human audit" },
];

for (const item of movedCases) {
  test(`moved ${item.argv.join(" ")} prints one cvm line and exits 2 without HTTP`, async () => {
    for (const prefix of [undefined, "cloud"]) {
      const run = await runCliWithApi(item.argv, { respond: genericApiResponse, ...(prefix ? { prefix } : {}) });
      assert.equal(run.code, 2);
      assert.equal(run.requests.length, 0);
      assert.equal(run.stdout, "");
      assert.equal(run.stderr, `${item.line}\n`);
    }
  });
}

test("unknown cloud verbs point at cvm with the same argv", async () => {
  for (const argv of [["sql", "--schema"], ["tools", "list"], ["scenarios", "list"], ["context", "show"], ["doctor"]]) {
    const run = await runCliWithApi(argv, { prefix: "cloud", respond: genericApiResponse });
    assert.equal(run.code, 2);
    assert.equal(run.requests.length, 0);
    assert.equal(run.stderr, `caveman cloud ${argv[0]} moved to cvm. Install: npm i -g @caveman-ai/cloud, then run: cvm ${argv.join(" ")}\n`);
  }
});

test("a local tools verb under cloud keeps the wrong-namespace hint", async () => {
  const run = await runCliWithApi(["compress"], { prefix: "cloud", respond: genericApiResponse });
  assert.equal(run.code, 2);
  assert.match(run.stderr, /did you mean `caveman tools compress`/);
});

test("audit typos keep the usage error instead of the moved line", async () => {
  const run = await runCliWithApi(["audit", "imprt", "x.jsonl"], { respond: genericApiResponse });
  assert.equal(run.code, 2);
  assert.equal(run.requests.length, 0);
  assert.match(run.stderr, /^usage: caveman audit import --format <fmt> <file> \| eval-import <evidence\.jsonl>\n$/);
});

test("legacy-only moved verbs print the cvm line", async () => {
  for (const [argv, cvm] of [[["opportunities", "list"], "fixes list_opportunities"], [["deploy", "status"], "context system_status"]]) {
    const run = await runCliWithApi(argv, { respond: genericApiResponse });
    assert.equal(run.code, 2);
    assert.equal(run.requests.length, 0);
    assert.match(run.stderr, new RegExp(`moved to cvm\\. Install: npm i -g @caveman-ai/cloud, then run: cvm ${cvm}\\n$`));
  }
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
