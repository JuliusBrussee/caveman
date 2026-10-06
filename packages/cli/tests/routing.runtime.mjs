import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { modulesFixture, runCli } from "./_modules.mjs";

// A Cloud whose GET /api/v1/auth/me answers `me` (an object, or a status code).
async function cloud(me) {
  const server = createServer((req, res) => {
    if (req.url !== "/api/v1/auth/me") return res.writeHead(404).end();
    if (typeof me === "number") return res.writeHead(me).end();
    res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify(me));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { url: `http://127.0.0.1:${server.address().port}`, close: () => server.close() };
}

async function routingRow(me) {
  const fx = modulesFixture();
  const stub = await cloud(me);
  try {
    assert.equal((await runCli(["on", "--all", "--yes"], fx.env)).code, 0);
    const env = { ...fx.env, CAVE_TOKEN: "test-token", CAVE_API_URL: stub.url };
    const first = await runCli(["status"], env);
    assert.equal(first.code, 0, first.stderr);
    const second = await runCli(["status"], env);
    const row = (out) => out.stdout.split("\n").find((line) => line.startsWith("  on  routing"));
    return { row: row(first), first: first.stdout, second: second.stdout };
  } finally {
    stub.close();
    fx.cleanup();
  }
}

const routing = (fields) => ({ user: { email: "a@b.c" }, plan: "free", products: [{ id: "routing", unit: "decision", ...fields }] });

test("status shows routing's free decisions from /me", async () => {
  const { row } = await routingRow(routing({ free_allowance: 100000, used: 1204, period_end: "2026-11-01T00:00:00Z", state: "ok" }));
  assert.match(row, /^  on  routing +wired +wired +1,204 of 100,000 free decisions this month$/);
});

test("status shows the routing row without numbers when /me has none yet", async () => {
  const { row } = await routingRow({ user: { email: "a@b.c" } });
  assert.match(row, /^  on  routing +wired +wired$/);
});

test("routing over its free allowance pauses, says so once, and points at billing", async () => {
  const { row, first, second } = await routingRow(routing({ free_allowance: 100000, used: 100000, period_end: "2026-11-01T00:00:00Z", state: "limited", reason: "allowance" }));
  assert.match(row, /routing paused · allowance · caveman billing$/);
  const notice = "Free routing used for October. Back to local until Nov 1 · add a card: caveman billing";
  assert.ok(first.includes(notice), first);
  assert.ok(!second.includes(notice), "the allowance line prints once per period");
  // Local modules are untouched by the limit.
  assert.match(first, /^  on  input +wired +wired/m);
});

test("a Cloud that does not answer never fails status", async () => {
  const { row } = await routingRow(500);
  assert.match(row, /—.*waiting for Cloud routing$/);
});

test("caveman billing opens the signed-in Cloud's billing page", async () => {
  const fx = modulesFixture();
  mkdirSync(fx.env.CAVEMAN_HOME, { recursive: true });
  try {
    for (const [base, page] of [["https://api.caveman.so", "https://app.caveman.so/billing"], ["http://localhost:8080", "http://localhost:3000/billing"]]) {
      const path = join(fx.env.CAVEMAN_HOME, "cloud.json");
      let doc = {};
      try { doc = JSON.parse(readFileSync(path, "utf8")); } catch { /* first run */ }
      writeFileSync(path, JSON.stringify({ ...doc, baseURL: base }));
      const out = await runCli(["billing"], { ...fx.env, CAVE_TOKEN: "test-token" });
      assert.equal(out.code, 0, out.stderr);
      assert.equal(out.stdout, `${page}\n`);
    }
  } finally {
    fx.cleanup();
  }
});
