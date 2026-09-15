import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createHash, generateKeyPairSync, sign as edSign } from "node:crypto";
import { createServer } from "node:http";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const fixture = join(here, "fixtures", "receipts-bundle.json");

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env: env ?? process.env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function tmp(name) {
  return join(mkdtempSync(join(tmpdir(), "cave-rcpt-")), name);
}

function canonicalize(value) {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return "[" + value.map(canonicalize).join(",") + "]";
  return "{" + Object.keys(value).sort().map((key) => JSON.stringify(key) + ":" + canonicalize(value[key])).join(",") + "}";
}

function testKey(keyId) {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const der = publicKey.export({ format: "der", type: "spki" });
  return {
    privateKey,
    info: { key_id: keyId, alg: "Ed25519", key: der.subarray(der.length - 32).toString("base64") },
  };
}

function signedReceipt({ key, scope, seq, day, prev = "", tokensBefore = 100, tokensAfter = 70, schema = "caveman.receipt.v2", savings = 1.25, formula = "verified-savings-ledger.v1", passed = 2, failed = 0, catalogVersion = "2026-07-10", methods }) {
  const core = {
    schema,
    scope,
    day,
    seq,
    prev_receipt_hash: prev,
    verified_savings_usd: savings,
    tokens_before: tokensBefore,
    tokens_after: tokensAfter,
    total_cost_usd: 0.12,
    total_cost_microusd: 120000,
    requests_optimized: 2,
    optimizers: [{ optimizer_id_hash: `sha256:${"c".repeat(64)}`, requests_optimized: 2 }],
    eval_gate: { passed, failed },
    formula_version: formula,
    catalog_version: catalogVersion,
  };
  if (schema === "caveman.receipt.v3" || schema === "caveman.receipt.v4") {
    core.verified_savings_units_1e10 = Math.round(savings * 10_000_000_000);
    core.total_cost_units_1e10 = 1200000000;
  }
  if (schema === "caveman.receipt.v4") {
    core.scope_completeness = "included_receipts_only";
    core.methods = formula === "cavebench.self.v1" ? [] : methods ?? ["provider_causal_cache"];
  }
  const receipt_hash = "sha256:" + createHash("sha256").update(canonicalize(core)).digest("hex");
  return {
    ...core,
    receipt_hash,
    signature: { alg: "Ed25519", key_id: key.info.key_id, sig: edSign(null, Buffer.from(receipt_hash), key.privateKey).toString("base64") },
  };
}

test("verify accepts v3 exact-money receipts", async () => {
  const key = testKey("key-v3");
  const scope = { org_hash: `sha256:${"d".repeat(64)}`, project_hash: `sha256:${"3".repeat(64)}` };
  const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v3" });
  const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
  const file = tmp("v3.json");
  writeFileSync(file, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
});

test("CLI method mirror matches the Go verified-method registry", (t) => {
  const goRegistry = join(here, "..", "..", "..", "cloud", "internal", "verifiedmethod", "verifiedmethod.go");
  if (!existsSync(goRegistry)) {
    t.skip("commercial verified-method registry is not part of the public repository");
    return;
  }
  const goSource = readFileSync(goRegistry, "utf8");
  const mirrorSource = readFileSync(join(here, "..", "src", "verified-methods.mirror.ts"), "utf8");
  const tupleMethods = [...goSource.matchAll(/Method:\s*"([^"]+)"/g)].map((match) => match[1]);
  const countedMethod = goSource.match(/const CountedBaselineMethod = "([^"]+)"/)?.[1];
  const mirrorBlock = mirrorSource.match(/VERIFIED_SAVINGS_METHODS = \[([\s\S]*?)\] as const/);
  const mirrorMethods = mirrorBlock ? [...mirrorBlock[1].matchAll(/"([^"]+)"/g)].map((match) => match[1]) : [];
  assert.ok(countedMethod, "Go counted-baseline method is missing");
  assert.ok(mirrorBlock, "CLI verified-method mirror is missing");
  assert.deepEqual(mirrorMethods, [...tupleMethods, countedMethod]);
});

test("verify accepts v4 signed scope and methods and prints both", async () => {
  const key = testKey("key-v4");
  const scope = { org_hash: `sha256:${"d".repeat(64)}`, project_hash: `sha256:${"4".repeat(64)}` };
  const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v4" });
  const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
  const file = tmp("v4.json");
  writeFileSync(file, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
  const summary = JSON.parse(out.stdout);
  assert.deepEqual(summary.scope_completeness, ["included_receipts_only"]);
  assert.deepEqual(summary.methods, ["provider_causal_cache"]);
});

test("verify accepts every registered billable v4 method and a mixed method day", async () => {
  const methods = [
    "provider_causal_cache",
    "provider_causal_cache_bedrock",
    "provider_counted_baseline_delta",
  ];
  for (const [index, method] of methods.entries()) {
    const key = testKey(`key-v4-${index}`);
    const scope = { org_hash: `sha256:${"f".repeat(64)}`, project_hash: `sha256:${(index + 5).toString(16).repeat(64)}` };
    const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v4", methods: [method] });
    const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
    const file = tmp(`v4-${method}.json`);
    writeFileSync(file, JSON.stringify(bundle));
    const out = await runCli(["receipts", "verify", file]);
    assert.equal(out.code, 0, `${method}: ${out.stderr}`);
    assert.deepEqual(JSON.parse(out.stdout).methods, [method]);
  }

  const key = testKey("key-v4-mixed");
  const scope = { org_hash: `sha256:${"f".repeat(64)}`, project_hash: `sha256:${"9".repeat(64)}` };
  const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v4", methods });
  const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
  const file = tmp("v4-mixed.json");
  writeFileSync(file, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout).methods, methods);
});

test("verify rejects unknown, duplicate, and unsorted billable methods", async () => {
  const invalidMethodSets = [
    ["provider_unknown"],
    ["provider_causal_cache", "provider_unknown"],
    ["provider_causal_cache", "provider_causal_cache"],
    ["provider_counted_baseline_delta", "provider_causal_cache"],
  ];
  for (const [index, methods] of invalidMethodSets.entries()) {
    const key = testKey(`key-v4-invalid-${index}`);
    const scope = { org_hash: `sha256:${"a".repeat(64)}`, project_hash: `sha256:${(index + 10).toString(16).repeat(64)}` };
    const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v4", methods });
    const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
    const file = tmp(`v4-invalid-${index}.json`);
    writeFileSync(file, JSON.stringify(bundle));
    const out = await runCli(["receipts", "verify", file]);
    assert.notEqual(out.code, 0, `${methods.join(",")}: invalid method list must fail`);
    assert.match(out.stderr, /unsupported billable methods/);
  }
});

test("verify rejects altered v4 scope or methods", async () => {
  const key = testKey("key-v4-tamper");
  const scope = { org_hash: `sha256:${"e".repeat(64)}`, project_hash: `sha256:${"4".repeat(64)}` };
  const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v4" });
  for (const mutate of [
    (copy) => { copy.scope_completeness = "complete"; },
    (copy) => { copy.methods = ["provider_counted_baseline"]; },
  ]) {
    const copy = structuredClone(receipt);
    mutate(copy);
    const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [copy] };
    const file = tmp("v4-tampered.json");
    writeFileSync(file, JSON.stringify(bundle));
    const out = await runCli(["receipts", "verify", file]);
    assert.notEqual(out.code, 0);
  }
});

function rotatedBundle() {
  const oldKey = testKey("key-2025");
  const currentKey = testKey("key-2026");
  const scopeA = { org_hash: `sha256:${"a".repeat(64)}`, project_hash: `sha256:${"1".repeat(64)}` };
  const scopeB = { org_hash: `sha256:${"a".repeat(64)}`, project_hash: `sha256:${"2".repeat(64)}` };
  const firstA = signedReceipt({ key: oldKey, scope: scopeA, seq: 1, day: "2026-07-08" });
  const secondA = signedReceipt({ key: currentKey, scope: scopeA, seq: 2, day: "2026-07-09", prev: firstA.receipt_hash });
  const firstB = signedReceipt({ key: currentKey, scope: scopeB, seq: 1, day: "2026-07-09" });
  return {
    oldKey,
    currentKey,
    bundle: {
      schema: "caveman.receipt-bundle.v2",
      public_key: currentKey.info,
      public_keys: [oldKey.info, currentKey.info],
      receipts: [secondA, firstB, firstA],
    },
  };
}

// A clean, Go-signed bundle must verify offline and report the receipt count.
test("verify accepts a valid Go-signed bundle (cross-language)", async () => {
  const out = await runCli(["receipts", "verify", fixture]);
  assert.equal(out.code, 0, out.stderr);
  const summary = JSON.parse(out.stdout);
  assert.equal(summary.verified, true);
  assert.equal(summary.receipts, 3);
  assert.equal(summary.scopes, 1);
  assert.equal(summary.trust_anchor, "embedded_keys_self_consistency_only");
});

// Pinning the published key (matching the bundle's embedded key) must also pass.
test("verify accepts a matching --pubkey", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  const pubFile = tmp("caveman.pub");
  writeFileSync(pubFile, bundle.public_key.key);
  const out = await runCli(["receipts", "verify", fixture, "--pubkey", pubFile]);
  assert.equal(out.code, 0, out.stderr);
});

// Editing any signed field must break verification (tamper-evidence).
test("verify rejects a tampered field", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  bundle.receipts[1].verified_savings_usd = 999999.99; // inflate, keep old hash/sig
  const bad = tmp("tampered.json");
  writeFileSync(bad, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", bad]);
  assert.notEqual(out.code, 0, "a tampered receipt must fail verification");
  assert.match(out.stderr, /receipt_hash mismatch|verification failed/);
});

// Dropping an interior day between retained neighbors breaks the chain.
test("verify rejects a dropped interior day", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  bundle.receipts = [bundle.receipts[0], bundle.receipts[2]]; // seq 1 then seq 3
  const bad = tmp("dropped.json");
  writeFileSync(bad, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", bad]);
  assert.notEqual(out.code, 0, "a dropped interior day must break the chain");
});

test("verify labels tail completeness as unattested", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  bundle.receipts = bundle.receipts.slice(0, -1);
  const prefix = tmp("prefix.json");
  writeFileSync(prefix, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", prefix]);
  assert.equal(out.code, 0, out.stderr);
  const summary = JSON.parse(out.stdout);
  assert.equal(summary.verification_coverage, "included_receipts_only");
  assert.equal(summary.completeness_attested, false);
});

test("verify rejects forged unsigned completeness metadata", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  bundle.completeness_attested = true;
  bundle.verification_coverage = "complete_scope";
  const forged = tmp("forged-completeness.json");
  writeFileSync(forged, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", forged]);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /completeness cannot be attested|unsupported unsigned verification coverage/);
});

test("verify accepts non-billable CaveBench formula receipts", async () => {
  const key = testKey("key-cavebench");
  const scope = { org_hash: `sha256:${"e".repeat(64)}`, project_hash: `sha256:${"4".repeat(64)}` };
  const receipt = signedReceipt({
    key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v3",
    savings: 0, formula: "cavebench.self.v1", passed: 1, failed: 1,
  });
  const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
  const file = tmp("cavebench.json");
  writeFileSync(file, JSON.stringify(bundle));
  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
});

test("verify rejects billable receipts without canonical catalog provenance", async () => {
  const key = testKey("key-catalog");
  const scope = { org_hash: `sha256:${"f".repeat(64)}`, project_hash: `sha256:${"5".repeat(64)}` };
  for (const catalogVersion of ["", " ", "v", "unpriced:openai/unknown", "mixed:2026-07-10", "mixed:2026-07-10,2026-06-02", "mixed:2026-07-10,2026-07-10"]) {
    const receipt = signedReceipt({ key, scope, seq: 1, day: "2026-07-10", schema: "caveman.receipt.v3", catalogVersion });
    const bundle = { schema: "caveman.receipt-bundle.v2", public_key: key.info, public_keys: [key.info], receipts: [receipt] };
    const file = tmp("bad-catalog.json");
    writeFileSync(file, JSON.stringify(bundle));
    const out = await runCli(["receipts", "verify", file]);
    assert.notEqual(out.code, 0, `accepted catalog_version ${JSON.stringify(catalogVersion)}`);
    assert.match(out.stderr, /catalog_version/);
  }
});

// A bundle whose key was swapped (and re-signed) must fail when the real
// published key is pinned via --pubkey.
test("verify rejects a mismatched --pubkey", async () => {
  const pubFile = tmp("wrong.pub");
  writeFileSync(pubFile, Buffer.alloc(32, 7).toString("base64"));
  const out = await runCli(["receipts", "verify", fixture, "--pubkey", pubFile]);
  assert.notEqual(out.code, 0, "a non-matching published key must be rejected");
});

test("verify supports key rotation and independent per-project chains", async () => {
  const { bundle } = rotatedBundle();
  const file = tmp("rotated.json");
  writeFileSync(file, JSON.stringify(bundle));

  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
  const summary = JSON.parse(out.stdout);
  assert.equal(summary.verified, true);
  assert.equal(summary.receipts, 3);
  assert.equal(summary.scopes, 2);
  assert.deepEqual(summary.key_ids, ["key-2025", "key-2026"]);
});

test("a raw pinned current key does not silently trust embedded historical keys", async () => {
  const { bundle, currentKey } = rotatedBundle();
  const file = tmp("rotated.json");
  const pubFile = tmp("current.pub");
  writeFileSync(file, JSON.stringify(bundle));
  writeFileSync(pubFile, currentKey.info.key);

  const out = await runCli(["receipts", "verify", file, "--pubkey", pubFile]);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /key-2025 is not present in trusted --pubkey material/);
});

test("a pinned JSON keyring authenticates every rotated signing key", async () => {
  const { bundle } = rotatedBundle();
  const file = tmp("rotated.json");
  const pubFile = tmp("trusted-keyring.json");
  writeFileSync(file, JSON.stringify(bundle));
  writeFileSync(pubFile, JSON.stringify({ public_keys: bundle.public_keys }));

  const out = await runCli(["receipts", "verify", file, "--pubkey", pubFile]);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout).trust_anchor, "pinned_keyring");
});

test("verify rejects duplicate key ids and provider-signed impossible counters", async () => {
  const { bundle, currentKey } = rotatedBundle();
  bundle.public_keys.push(currentKey.info);
  const duplicate = tmp("duplicate-keys.json");
  writeFileSync(duplicate, JSON.stringify(bundle));
  const duplicateOut = await runCli(["receipts", "verify", duplicate]);
  assert.notEqual(duplicateOut.code, 0);
  assert.match(duplicateOut.stderr, /duplicate key_id/);

  const scope = { org_hash: `sha256:${"d".repeat(64)}`, project_hash: `sha256:${"3".repeat(64)}` };
  const impossible = signedReceipt({ key: currentKey, scope, seq: 1, day: "2026-07-10", tokensBefore: 10, tokensAfter: 20 });
  const invalidBundle = {
    schema: "caveman.receipt-bundle.v2",
    public_key: currentKey.info,
    public_keys: [currentKey.info],
    receipts: [impossible],
  };
  const invalid = tmp("impossible-counters.json");
  writeFileSync(invalid, JSON.stringify(invalidBundle));
  const impossibleOut = await runCli(["receipts", "verify", invalid]);
  assert.notEqual(impossibleOut.code, 0);
  assert.match(impossibleOut.stderr, /invalid token counters/);
});

test("an empty bundle is structurally valid but never claimed as verified evidence", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  bundle.receipts = [];
  const file = tmp("empty.json");
  writeFileSync(file, JSON.stringify(bundle));

  const out = await runCli(["receipts", "verify", file]);
  assert.equal(out.code, 0, out.stderr);
  const summary = JSON.parse(out.stdout);
  assert.equal(summary.valid_bundle, true);
  assert.equal(summary.verified, false);
  assert.equal(summary.empty, true);
});

// export reads the local control plane (no Caveman cloud), writes a bundle file,
// and that file then verifies offline — the air-gapped meter flow end to end.
test("export writes a bundle from the local control plane, which then verifies", async () => {
  const bundle = JSON.parse(readFileSync(fixture, "utf8"));
  const server = createServer((req, res) => {
    if (req.url.startsWith("/api/v1/metering/receipts")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(bundle));
    } else {
      res.writeHead(404);
      res.end("{}");
    }
  });
  const port = await new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

  const home = mkdtempSync(join(tmpdir(), "cave-home-"));
  const outFile = tmp("exported.json");
  const env = { ...process.env, HOME: home, CAVE_TOKEN: "ci-token", CAVE_API_URL: `http://127.0.0.1:${port}` };

  const exported = await runCli(["receipts", "export", "--since", "2026-06-01", "-o", outFile], env);
  assert.equal(exported.code, 0, exported.stderr);
  assert.equal(JSON.parse(exported.stdout).receipts, 3);

  const verified = await runCli(["receipts", "verify", outFile]);
  assert.equal(verified.code, 0, verified.stderr);

  server.close();
});
