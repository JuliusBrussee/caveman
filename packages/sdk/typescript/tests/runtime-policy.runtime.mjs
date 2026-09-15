/**
 * Runtime tests for the TS SDK runtime-policy client (spec §21.2, issue #88).
 *
 * Drives EVERY section of ../../parity/runtime-policy.fixtures.json: the fetch
 * wire, the signature cases, all assignment vectors (exact float equality
 * against the Go sampling.Fraction) and all decision cases. Run with:
 *   pnpm --filter @caveman-ai/sdk build && node --test tests/runtime-policy.runtime.mjs
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createPrivateKey, createPublicKey, sign } from "node:crypto";

import { Cave, policyUnitFraction } from "../dist/index.js";

const FIXTURES = JSON.parse(readFileSync(new URL("../../parity/runtime-policy.fixtures.json", import.meta.url), "utf8"));
const PARITY = JSON.parse(readFileSync(new URL("../../parity/fixtures.json", import.meta.url), "utf8"));
const CONFIG = PARITY.config;

// ─── Transport mock ──────────────────────────────────────────────────────────

const originalFetch = globalThis.fetch;
let captured = [];

function boundedMockResponse(payload, status = 200) {
  const encoded = new TextEncoder().encode(typeof payload === "string" ? payload : JSON.stringify(payload));
  let sent = false;
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => payload,
    text: async () => (typeof payload === "string" ? payload : JSON.stringify(payload)),
    body: {
      getReader: () => ({
        read: async () => {
          if (sent) return { done: true, value: undefined };
          sent = true;
          return { done: false, value: encoded };
        },
        cancel: async () => undefined,
        releaseLock: () => undefined
      })
    }
  };
}

/** Queue one or more responses; each is `{status?, body}` or an Error to throw. */
function mockFetch(...responses) {
  captured = [];
  let index = 0;
  globalThis.fetch = async (url, init) => {
    captured.push({ url: String(url), method: init?.method, headers: init?.headers ?? {}, body: init?.body });
    const next = responses[Math.min(index, responses.length - 1)];
    index += 1;
    if (next instanceof Error) throw next;
    const status = next.status ?? 200;
    const payload = next.body;
    return boundedMockResponse(payload, status);
  };
}

function restoreFetch() {
  globalThis.fetch = originalFetch;
  captured = [];
}

function newCave() {
  return new Cave({
    apiKey: CONFIG.api_key,
    baseURL: CONFIG.base_url,
    agent: CONFIG.agent,
    defaultWorkflow: CONFIG.default_workflow,
    retention: CONFIG.retention,
    user: CONFIG.user
  });
}

const SIGNED_RESPONSE = () => ({
  bundle: FIXTURES.fetch.response.bundle,
  signature: FIXTURES.fetch.response.signature,
  public_key: FIXTURES.fetch.response.public_key
});
const UNSIGNED_KILL_RESPONSE = () => ({ bundle: FIXTURES.kill_bundle, signature: null, public_key: null });

/** Build an unsigned response around an arbitrary bundle object. */
function bundleResponse(bundle) {
  return { bundle: JSON.stringify(bundle), signature: null, public_key: null };
}

const BASE_BUNDLE = JSON.parse(FIXTURES.fetch.response.bundle);

async function loadedClient(response, options = {}) {
  mockFetch({ body: response });
  const client = newCave().runtimePolicy(options);
  const result = await client.refresh();
  return { client, result };
}

// ─── fetch: wire + state ─────────────────────────────────────────────────────

test("fetch: GET /sdk/v1/runtime-policy with the std headers minus content-type", async () => {
  try {
    const { result } = await loadedClient(SIGNED_RESPONSE());
    assert.deepEqual(result, { ok: true, signed: true });
    assert.equal(captured.length, 1);
    const request = captured[0];
    assert.equal(request.method, FIXTURES.fetch.wire.method);
    assert.equal(request.url, `${CONFIG.base_url}${FIXTURES.fetch.wire.path}`);
    assert.equal(request.body, undefined, "GET carries no body");

    const expected = { ...PARITY.std_headers };
    delete expected["content-type"];
    const actual = Object.fromEntries(Object.entries(request.headers).map(([k, v]) => [k.toLowerCase(), v]));
    assert.deepEqual(actual, expected);
  } finally {
    restoreFetch();
  }
});

test("fetch: state() matches expect_state", async () => {
  try {
    const { client } = await loadedClient(SIGNED_RESPONSE());
    const state = client.state();
    const expected = FIXTURES.fetch.expect_state;
    assert.equal(state.signed, expected.signed);
    assert.equal(state.policyVersion, expected.policy_version);
    assert.equal(state.sequence, expected.sequence);
    assert.equal(state.kill, expected.kill);
    assert.equal(state.hasBundle, true);
    assert.equal(state.killedLocally, false);
    assert.equal(JSON.parse(FIXTURES.fetch.response.bundle).runtime_policies.length, expected.policy_count);
  } finally {
    restoreFetch();
  }
});

// ─── signature cases ─────────────────────────────────────────────────────────

const PINNED_KEY = FIXTURES.fetch.response.public_key.key;

/**
 * Sign an arbitrary bundle string with the fixture's published test seed, so a
 * test can produce a bundle that is CRYPTOGRAPHICALLY VALID and still gets
 * rejected downstream (wrong schema, regressed sequence). Asserted below to
 * reproduce the fixture's own key and signature byte-for-byte — if that ever
 * drifts, these tests must fail loudly rather than sign with a stray key.
 */
function signBundle(bundle) {
  const pkcs8 = Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), Buffer.from(FIXTURES.signing_test_seed_hex, "hex")]);
  const key = createPrivateKey({ key: pkcs8, format: "der", type: "pkcs8" });
  return sign(null, Buffer.from(bundle, "utf8"), key).toString("base64");
}

/** A signed envelope around an arbitrary bundle string, keyed like the fixture. */
function signedResponse(bundle) {
  return { bundle, signature: { sig: signBundle(bundle) }, public_key: FIXTURES.fetch.response.public_key };
}

test("signing seed: reproduces the fixture's key and signature exactly", () => {
  const pkcs8 = Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), Buffer.from(FIXTURES.signing_test_seed_hex, "hex")]);
  const pub = createPublicKey(createPrivateKey({ key: pkcs8, format: "der", type: "pkcs8" }))
    .export({ format: "der", type: "spki" })
    .subarray(-32)
    .toString("base64");
  assert.equal(pub, PINNED_KEY, "the fixture seed no longer derives the fixture public key");
  assert.equal(signBundle(FIXTURES.fetch.response.bundle), FIXTURES.fetch.response.signature.sig);
});

const SIGNATURE_HANDLERS = {
  pinned_key_verifies: async (testCase) => {
    const { client, result } = await loadedClient(SIGNED_RESPONSE(), { publicKey: testCase.pinned_public_key });
    assert.equal(result.ok, true, testCase.expect);
    assert.equal(result.signed, true);
    assert.equal(client.state().signed, true);
    assert.equal(client.state().policyVersion, 7);
  },
  tampered_bundle_rejected: async (testCase) => {
    const tampered = {
      ...SIGNED_RESPONSE(),
      bundle: FIXTURES.fetch.response.bundle.replace('"policy_version":7', '"policy_version":9')
    };
    assert.notEqual(tampered.bundle, FIXTURES.fetch.response.bundle);
    const { client, result } = await loadedClient(tampered, { publicKey: testCase.pinned_public_key });
    assert.equal(result.ok, false, testCase.expect);
    assert.match(result.error, /did not verify/);
    assert.equal(client.state().hasBundle, false, "no prior bundle survives a rejected one");
    const decision = client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context: {} });
    assert.equal(decision.reason, "policy_unavailable");
    assert.equal(decision.decision, "baseline");
  },
  unsigned_refused_when_key_pinned: async (testCase) => {
    const { client, result } = await loadedClient(UNSIGNED_KILL_RESPONSE(), { publicKey: testCase.pinned_public_key });
    assert.equal(result.ok, false, testCase.expect);
    assert.match(result.error, /unsigned/);
    assert.equal(client.state().hasBundle, false);
  },
  unsigned_accepted_when_nothing_pinned: async (testCase) => {
    assert.equal(testCase.pinned_public_key, null);
    const { client, result } = await loadedClient(UNSIGNED_KILL_RESPONSE());
    assert.equal(result.ok, true, testCase.expect);
    assert.equal(result.signed, false);
    assert.equal(client.state().signed, false);
    assert.equal(client.state().kill, true);
  }
};

for (const testCase of FIXTURES.signature_cases) {
  test(`signature case: ${testCase.name}`, async () => {
    const handler = SIGNATURE_HANDLERS[testCase.name];
    assert.ok(handler, `no handler for signature case ${testCase.name}`);
    try {
      await handler(testCase);
    } finally {
      restoreFetch();
    }
  });
}

test("signature: tampered bundle keeps the last-known-good bundle", async () => {
  try {
    mockFetch(
      { body: SIGNED_RESPONSE() },
      { body: { ...SIGNED_RESPONSE(), bundle: FIXTURES.fetch.response.bundle.replace('"policy_version":7', '"policy_version":9') } }
    );
    const client = newCave().runtimePolicy({ publicKey: PINNED_KEY });
    assert.equal((await client.refresh()).ok, true);
    const second = await client.refresh();
    assert.equal(second.ok, false);
    assert.equal(client.state().policyVersion, 7, "last-known-good survives");
    assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context: { stack_trace_location_confidence: 0.95, language: "typescript" } }).decision, "execute");
  } finally {
    restoreFetch();
  }
});

test("signature: TOFU pin rejects a later unsigned bundle", async () => {
  try {
    mockFetch({ body: SIGNED_RESPONSE() }, { body: UNSIGNED_KILL_RESPONSE() });
    const client = newCave().runtimePolicy();
    assert.equal((await client.refresh()).signed, true);
    const second = await client.refresh();
    assert.equal(second.ok, false);
    assert.match(second.error, /pinned/);
    assert.equal(client.state().policyVersion, 7);
    assert.equal(client.state().kill, false);
  } finally {
    restoreFetch();
  }
});

test("signature: a validly-signed but schema-rejected bundle still pins the key", async () => {
  // Regression: the TOFU pin used to be stored only AFTER the schema_version
  // and sequence checks. A bundle the project genuinely signed but this client
  // rejected for shape therefore left the client unpinned — and the next
  // UNSIGNED bundle was accepted. A verified signature is a fact about the
  // project's key no matter what the payload turns out to say.
  try {
    const badSchema = JSON.stringify({ ...BASE_BUNDLE, schema_version: "caveman.runtime-policy.v9" });
    mockFetch({ body: signedResponse(badSchema) }, { body: UNSIGNED_KILL_RESPONSE() });
    const client = newCave().runtimePolicy();

    const rejected = await client.refresh();
    assert.equal(rejected.ok, false);
    assert.match(rejected.error, /schema_version/);
    assert.equal(client.state().hasBundle, false, "the rejected bundle never took force");

    // The pin from that verified signature stands, so the unsigned follow-up
    // is refused rather than silently installed.
    const unsigned = await client.refresh();
    assert.equal(unsigned.ok, false);
    assert.match(unsigned.error, /pinned/);
    assert.equal(client.state().hasBundle, false);
    assert.equal(client.state().kill, false, "the unsigned kill bundle never landed");
  } finally {
    restoreFetch();
  }
});

// Not a timing regression (a sequence rejection can never be the FIRST
// refresh — it needs a prior accepted bundle, which already pins). It pins the
// adjacent property: a rejected-but-verified bundle does not weaken the pin.
test("signature: a rejected bundle does not weaken an established pin", async () => {
  try {
    const ahead = JSON.stringify({ ...BASE_BUNDLE, sequence: 999, policy_version: 99 });
    const behind = JSON.stringify({ ...BASE_BUNDLE, sequence: 41, policy_version: 6 });
    mockFetch({ body: signedResponse(ahead) }, { body: signedResponse(behind) }, { body: UNSIGNED_KILL_RESPONSE() });
    const client = newCave().runtimePolicy({ publicKey: PINNED_KEY });
    assert.equal((await client.refresh()).ok, true);

    const rejected = await client.refresh();
    assert.equal(rejected.ok, false);
    assert.match(rejected.error, /sequence regressed/);
    assert.equal(client.state().sequence, 999, "the rejected bundle did not take force");

    const unsigned = await client.refresh();
    assert.equal(unsigned.ok, false);
    assert.match(unsigned.error, /pinned/);
    assert.equal(client.state().sequence, 999);
  } finally {
    restoreFetch();
  }
});

test("signature: a signature without a public key is rejected", async () => {
  try {
    const { client, result } = await loadedClient({ bundle: FIXTURES.fetch.response.bundle, signature: FIXTURES.fetch.response.signature, public_key: null });
    assert.equal(result.ok, false);
    assert.equal(client.state().hasBundle, false);
  } finally {
    restoreFetch();
  }
});

test("signature: malformed or partial metadata cannot downgrade to unsigned", async () => {
  try {
    for (const envelope of [
      { bundle: FIXTURES.fetch.response.bundle, signature: { sig: "***not-base64***" }, public_key: { key: "also-bad" } },
      { bundle: FIXTURES.fetch.response.bundle, signature: {}, public_key: null },
      { bundle: FIXTURES.fetch.response.bundle, signature: "bad-shape", public_key: null },
      { bundle: FIXTURES.fetch.response.bundle, signature: null, public_key: { key: FIXTURES.fetch.response.public_key.key } },
    ]) {
      const { client, result } = await loadedClient(envelope);
      assert.equal(result.ok, false, JSON.stringify(envelope));
      assert.equal(client.state().hasBundle, false);
    }
  } finally {
    restoreFetch();
  }
});

// ─── assignment vectors ──────────────────────────────────────────────────────

test("assignment: the weighted exp-w vectors are present and pin the propensity association", () => {
  // (1 - holdout) * (fraction / total) and ((1 - holdout) * fraction) / total
  // are different float expressions. The exp-w vectors carry the one-ULP
  // difference (0.32000000000000006 vs 0.32) that separates them, so a port
  // that "simplifies" the association fails here instead of quietly shipping a
  // propensity the Go side never computed.
  const weighted = FIXTURES.assignment_vectors.filter((v) => v.keys[1] === "exp-w");
  assert.equal(weighted.length, 3, "the three weighted fractional-arm vectors must be present");
  assert.ok(typeof FIXTURES.assignment_vectors_note === "string" && FIXTURES.assignment_vectors_note.includes("(1 - holdout_frac) * (fraction / total)"));
  const loose = weighted.find((v) => v.expected_arm === "loose");
  assert.ok(loose, "exp-w must carry a vector landing on the trailing arm");
  const holdout = loose.holdout_frac;
  const total = loose.arms.reduce((sum, arm) => sum + arm.fraction, 0);
  const fraction = loose.arms.find((arm) => arm.name === "loose").fraction;
  assert.equal((1 - holdout) * (fraction / total), loose.expected_propensity);
  assert.notEqual(((1 - holdout) * fraction) / total, loose.expected_propensity, "the fixture no longer discriminates the association");
});

for (const vector of FIXTURES.assignment_vectors) {
  const [projectId, experimentId, unitKey] = vector.keys;

  test(`assignment vector: ${vector.keys.join(" / ")}`, async () => {
    // 1. the hash port, bit-for-bit against the Go implementation.
    assert.equal(policyUnitFraction(...vector.keys), vector.fraction);

    // 2. the arm the decision algorithm derives from it.
    try {
      const bundle = {
        ...BASE_BUNDLE,
        project_id: projectId,
        runtime_policies: [
          {
            id: "vector_policy",
            task_family: "vector_family",
            execute: { workflow: "vector_execute" },
            fallback: { workflow: "vector_fallback" },
            experiment: { id: experimentId, holdout_frac: vector.holdout_frac, arms: vector.arms }
          }
        ]
      };
      const { client } = await loadedClient(bundleResponse(bundle));
      const decision = client.decide("vector_family", { unitKey });

      if (unitKey === "") {
        // The empty-unit-key vector pins the hash only: decide() must refuse
        // before it ever assigns an arm.
        assert.equal(decision.reason, "no_unit_key");
        assert.equal(decision.decision, "fallback");
        assert.equal(decision.arm, undefined);
        return;
      }
      assert.equal(decision.arm, vector.expected_arm);
      assert.equal(decision.propensity, vector.expected_propensity);
      assert.equal(decision.experimentId, experimentId);
      assert.equal(decision.decision, vector.expected_arm === "holdout" ? "fallback" : "execute");
      assert.equal(decision.workflow, vector.expected_arm === "holdout" ? "vector_fallback" : "vector_execute");
    } finally {
      restoreFetch();
    }
  });
}

// ─── decision cases ──────────────────────────────────────────────────────────

for (const testCase of FIXTURES.decision_cases) {
  test(`decision case: ${testCase.name}`, async () => {
    try {
      let client;
      if (testCase.bundle === null) {
        client = newCave().runtimePolicy();
      } else if (testCase.bundle === "kill_bundle") {
        ({ client } = await loadedClient({ bundle: FIXTURES.kill_bundle, signature: null, public_key: null }));
      } else {
        ({ client } = await loadedClient(SIGNED_RESPONSE()));
      }

      const decision = client.decide(testCase.task_family, { unitKey: testCase.unit_key, context: testCase.context });
      const expected = testCase.expect;
      assert.equal(decision.decision, expected.decision);
      assert.equal(decision.reason, expected.reason);
      assert.equal(decision.workflow, expected.workflow ?? decision.workflow);
      if (expected.workflow === null) assert.equal(decision.workflow, null);
      if (expected.policy_id !== undefined) assert.equal(decision.policyId, expected.policy_id);
      if (expected.experiment_id !== undefined) assert.equal(decision.experimentId, expected.experiment_id);
      if (expected.arm !== undefined) assert.equal(decision.arm, expected.arm);
      if (expected.propensity !== undefined) assert.equal(decision.propensity, expected.propensity);
      // Nothing in a decision is a money or savings claim.
      for (const key of Object.keys(decision)) {
        assert.ok(!/saving|verified|dollar/i.test(key), `decision must not carry ${key}`);
      }
    } finally {
      restoreFetch();
    }
  });
}

// ─── kill switches ───────────────────────────────────────────────────────────

test("kill(): local latch forces the baseline", async () => {
  try {
    const { client } = await loadedClient(SIGNED_RESPONSE());
    client.kill();
    const decision = client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context: { stack_trace_location_confidence: 0.95, language: "typescript" } });
    assert.deepEqual(
      { decision: decision.decision, reason: decision.reason, workflow: decision.workflow, policyId: decision.policyId },
      { decision: "baseline", reason: "local_kill", workflow: null, policyId: null }
    );
    assert.equal(client.state().killedLocally, true);
  } finally {
    restoreFetch();
  }
});

test("killEnv: env var is read at decide() time", async () => {
  const variable = "CAVEMAN_POLICY_KILL_TEST";
  try {
    const { client } = await loadedClient(SIGNED_RESPONSE(), { killEnv: variable });
    const context = { stack_trace_location_confidence: 0.95, language: "typescript" };
    assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context }).decision, "execute");

    process.env[variable] = "1";
    assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context }).reason, "local_kill");

    for (const falsey of ["", "0", "false", "off", "no"]) {
      process.env[variable] = falsey;
      assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context }).decision, "execute", `${JSON.stringify(falsey)} is not a kill`);
    }
    delete process.env[variable];
    assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context }).decision, "execute");

    // The default variable name is CAVEMAN_POLICY_KILL.
    const { client: defaultClient } = await loadedClient(SIGNED_RESPONSE());
    process.env["CAVEMAN_POLICY_KILL"] = "yes";
    assert.equal(defaultClient.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context }).reason, "local_kill");
  } finally {
    delete process.env[variable];
    delete process.env["CAVEMAN_POLICY_KILL"];
    restoreFetch();
  }
});

// ─── refresh robustness ──────────────────────────────────────────────────────

test("refresh: sequence regression is rejected", async () => {
  try {
    const stale = { ...BASE_BUNDLE, policy_version: 6, sequence: 41 };
    mockFetch({ body: SIGNED_RESPONSE() }, { body: bundleResponse(stale) });
    const client = newCave().runtimePolicy();
    await client.refresh();
    // The first bundle was signed, so the TOFU pin also rejects the unsigned
    // stale one; re-pin-free clients still must reject on sequence alone.
    const fresh = newCave().runtimePolicy();
    mockFetch({ body: bundleResponse(BASE_BUNDLE) }, { body: bundleResponse(stale) });
    assert.equal((await fresh.refresh()).ok, true);
    const second = await fresh.refresh();
    assert.equal(second.ok, false);
    assert.match(second.error, /sequence regressed/);
    assert.equal(fresh.state().sequence, 42);
    assert.equal(fresh.state().policyVersion, 7);
  } finally {
    restoreFetch();
  }
});

test("refresh: equal or higher sequence is accepted", async () => {
  try {
    const next = { ...BASE_BUNDLE, policy_version: 8, sequence: 43, runtime_policies: [] };
    mockFetch({ body: bundleResponse(BASE_BUNDLE) }, { body: bundleResponse(next) });
    const client = newCave().runtimePolicy();
    await client.refresh();
    assert.equal((await client.refresh()).ok, true);
    assert.equal(client.state().sequence, 43);
    assert.equal(client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context: {} }).reason, "no_policy");
  } finally {
    restoreFetch();
  }
});

test("refresh: missing or invalid counters cannot replace last-known-good", async () => {
  try {
    for (const patch of [
      { sequence: undefined },
      { sequence: 42.5 },
      { sequence: Number.NaN },
      { sequence: Number.MAX_SAFE_INTEGER + 1 },
      { policy_version: undefined },
      { policy_version: -1 },
    ]) {
      const invalid = { ...BASE_BUNDLE, kill: true, ...patch };
      if (patch.sequence === undefined) delete invalid.sequence;
      if (patch.policy_version === undefined) delete invalid.policy_version;
      mockFetch({ body: bundleResponse(BASE_BUNDLE) }, { body: bundleResponse(invalid) });
      const client = newCave().runtimePolicy();
      assert.equal((await client.refresh()).ok, true);
      assert.equal((await client.refresh()).ok, false, JSON.stringify(patch));
      assert.equal(client.state().sequence, 42);
      assert.equal(client.state().kill, false);
      restoreFetch();
    }
  } finally {
    restoreFetch();
  }
});

test("refresh: transport, HTTP, JSON and schema failures keep last-known-good", async () => {
  try {
    for (const bad of [
      new Error("connection reset"),
      { status: 503, body: {} },
      { body: "not json at all" },
      { body: { bundle: "{not json}" } },
      { body: { bundle: JSON.stringify({ ...BASE_BUNDLE, schema_version: "caveman.runtime-policy.v9" }) } },
      { body: { bundle: 42 } },
      { body: {} }
    ]) {
      mockFetch({ body: bundleResponse(BASE_BUNDLE) }, bad);
      const client = newCave().runtimePolicy();
      assert.equal((await client.refresh()).ok, true);
      const failure = await client.refresh();
      assert.equal(failure.ok, false, `expected rejection for ${JSON.stringify(bad)}`);
      assert.equal(typeof failure.error, "string");
      assert.equal(client.state().policyVersion, 7, "bundle in force is unchanged");
      assert.equal(
        client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context: { stack_trace_location_confidence: 0.95, language: "typescript" } }).decision,
        "execute"
      );
    }
  } finally {
    restoreFetch();
  }
});

test("refresh: response body is capped at 1 MiB and keeps last-known-good", async () => {
  try {
    mockFetch({ body: bundleResponse(BASE_BUNDLE) }, { body: "x".repeat(1024 * 1024 + 1) });
    const client = newCave().runtimePolicy();
    assert.equal((await client.refresh()).ok, true);
    const failure = await client.refresh();
    assert.deepEqual(failure, { ok: false, signed: false, error: "oversized_response" });
    assert.equal(client.state().policyVersion, 7);
    assert.equal(client.state().sequence, 42);
  } finally {
    restoreFetch();
  }
});

test("refresh: oversized content-length rejects before reading", async () => {
  let cancelled = false;
  let reads = 0;
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    headers: { get: (name) => name === "content-length" ? String(1024 * 1024 + 1) : null },
    body: {
      cancel: async () => { cancelled = true; },
      getReader: () => ({
        read: async () => { reads += 1; return { done: true, value: undefined }; },
        cancel: async () => undefined,
        releaseLock: () => undefined
      })
    }
  });
  try {
    const result = await newCave().runtimePolicy().refresh();
    assert.deepEqual(result, { ok: false, signed: false, error: "oversized_response" });
    assert.equal(reads, 0);
    assert.equal(cancelled, true);
  } finally {
    restoreFetch();
  }
});

test("refresh: response without a bounded reader fails closed", async () => {
  let textCalled = false;
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    headers: { get: () => null },
    body: null,
    text: async () => { textCalled = true; return JSON.stringify(SIGNED_RESPONSE()); }
  });
  try {
    const result = await newCave().runtimePolicy().refresh();
    assert.deepEqual(result, { ok: false, signed: false, error: "bounded_response_unavailable" });
    assert.equal(textCalled, false);
  } finally {
    restoreFetch();
  }
});

test("refresh: streamed response stops and cancels immediately past 1 MiB", async () => {
  let reads = 0;
  let cancelled = false;
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    headers: { get: () => null },
    body: {
      getReader: () => ({
        read: async () => {
          reads += 1;
          if (reads === 1) return { done: false, value: new Uint8Array(1024 * 1024) };
          return { done: false, value: new Uint8Array(1) };
        },
        cancel: async () => { cancelled = true; },
        releaseLock: () => undefined
      })
    },
    text: async () => { throw new Error("streaming path must not call text()"); }
  });
  try {
    const result = await newCave().runtimePolicy().refresh();
    assert.deepEqual(result, { ok: false, signed: false, error: "oversized_response" });
    assert.equal(reads, 2);
    assert.equal(cancelled, true);
  } finally {
    restoreFetch();
  }
});

test("refresh: never throws with no bundle ever loaded", async () => {
  try {
    mockFetch(new Error("dns failure"));
    const client = newCave().runtimePolicy();
    const result = await client.refresh();
    assert.equal(result.ok, false);
    assert.equal(result.signed, false);
    assert.equal(client.state().hasBundle, false);
    assert.equal(client.decide("anything", {}).reason, "policy_unavailable");
  } finally {
    restoreFetch();
  }
});

test("constructor: an unusable pinned key is a configuration error", () => {
  assert.throws(() => newCave().runtimePolicy({ publicKey: "not-base64!!" }), /Ed25519/);
  assert.throws(() => newCave().runtimePolicy({ publicKey: "YWJj" }), /Ed25519/);
});

// ─── fail-closed decision paths ──────────────────────────────────────────────

async function clientWithPolicies(policies, bundleOverrides = {}) {
  return loadedClient(bundleResponse({ ...BASE_BUNDLE, ...bundleOverrides, runtime_policies: policies }));
}

test("decide: malformed policies are skipped, never guessed", async () => {
  try {
    const invalid = [
      { task_family: "f", execute: { workflow: "w" } },
      { id: "no-exec", task_family: "f" },
      { id: "bad-exec", task_family: "f", execute: { workflow: "" } },
      { id: "bad-guards", task_family: "f", execute: { workflow: "w" }, applies_when: "nope" },
      { id: "bad-guard-entry", task_family: "f", execute: { workflow: "w" }, applies_when: ["nope"] }
    ];
    const { client } = await clientWithPolicies(invalid);
    const decision = client.decide("f", { unitKey: "u" });
    assert.equal(decision.decision, "baseline");
    assert.equal(decision.reason, "invalid_policy");
    assert.equal(decision.workflow, null);
  } finally {
    restoreFetch();
  }
});

test("decide: a disabled-only family is baseline/disabled", async () => {
  try {
    const { client } = await clientWithPolicies([{ id: "p", task_family: "f", disabled: true, execute: { workflow: "w" }, fallback: { workflow: "fb" } }]);
    const decision = client.decide("f", { unitKey: "u" });
    assert.equal(decision.decision, "baseline");
    assert.equal(decision.reason, "disabled");
  } finally {
    restoreFetch();
  }
});

test("decide: two guard-passing policies for one family are ambiguous", async () => {
  try {
    const { client } = await clientWithPolicies([
      { id: "a", task_family: "f", execute: { workflow: "wa" } },
      { id: "b", task_family: "f", execute: { workflow: "wb" } }
    ]);
    const decision = client.decide("f", { unitKey: "u" });
    assert.equal(decision.decision, "baseline");
    assert.equal(decision.reason, "ambiguous_policy");
    assert.equal(decision.policyId, null);
    assert.equal(decision.workflow, null);
  } finally {
    restoreFetch();
  }
});

test("decide: invalid experiments fall back instead of guessing an arm", async () => {
  try {
    const experiments = [
      { id: "", arms: [{ name: "candidate", fraction: 1 }] },
      { id: "e", arms: [] },
      { id: "e", arms: [{ name: "holdout", fraction: 1 }] },
      { id: "e", arms: [{ name: "", fraction: 1 }] },
      { id: "e", arms: [{ name: "candidate", fraction: 0 }] },
      { id: "e", arms: [{ name: "candidate", fraction: -1 }] },
      { id: "e", arms: [{ name: "candidate", fraction: "1" }] },
      { id: "e", holdout_frac: 1, arms: [{ name: "candidate", fraction: 1 }] },
      { id: "e", holdout_frac: -0.1, arms: [{ name: "candidate", fraction: 1 }] },
      { id: "e", holdout_frac: "0.1", arms: [{ name: "candidate", fraction: 1 }] },
      "not-an-object"
    ];
    for (const experiment of experiments) {
      const { client } = await clientWithPolicies([
        { id: "p", task_family: "f", execute: { workflow: "w" }, fallback: { workflow: "fb" }, experiment }
      ]);
      const decision = client.decide("f", { unitKey: "u" });
      assert.equal(decision.reason, "invalid_experiment", `expected invalid_experiment for ${JSON.stringify(experiment)}`);
      assert.equal(decision.decision, "fallback");
      assert.equal(decision.workflow, "fb");
      assert.equal(decision.arm, undefined);
      restoreFetch();
    }
  } finally {
    restoreFetch();
  }
});

test("decide: an absent or null holdout_frac means no holdout slice", async () => {
  try {
    for (const experiment of [
      { id: "e", arms: [{ name: "candidate", fraction: 1 }] },
      { id: "e", holdout_frac: null, arms: [{ name: "candidate", fraction: 1 }] },
      { id: "e", holdout_frac: 0, arms: [{ name: "candidate", fraction: 1 }] }
    ]) {
      const { client } = await clientWithPolicies([
        { id: "p", task_family: "f", execute: { workflow: "w" }, fallback: { workflow: "fb" }, experiment }
      ]);
      const decision = client.decide("f", { unitKey: "u" });
      assert.equal(decision.reason, "applied");
      assert.equal(decision.arm, "candidate");
      assert.equal(decision.propensity, 1);
      restoreFetch();
    }
  } finally {
    restoreFetch();
  }
});

// ─── guard cases (shared truth table) ────────────────────────────────────────
//
// The guard operator semantics live in the parity fixture, not in this file: a
// case that passes here and fails (or is missing) in sdk-python is a release
// gate failure, and the only way to keep two languages honest about a
// fail-closed truth table is to make them read the same one.

/** Run one fixture guard case through decide() and return whether the guard passed. */
async function guardPassed(guard, context) {
  const { client } = await clientWithPolicies([
    { id: "p", task_family: "f", execute: { workflow: "w" }, fallback: { workflow: "fb" }, applies_when: [guard] }
  ]);
  const decision = client.decide("f", { unitKey: "u", context });
  assert.ok(
    decision.reason === "applied" || decision.reason === "guards_failed",
    `guard evaluation resolved to an unexpected reason: ${decision.reason}`
  );
  return decision.reason === "applied";
}

test("guard cases: the fixture truth table is complete", () => {
  assert.ok(Array.isArray(FIXTURES.guard_cases), "fixture must carry guard_cases");
  assert.ok(FIXTURES.guard_cases.length >= 32, `expected the full guard truth table, saw ${FIXTURES.guard_cases.length}`);
  assert.equal(typeof FIXTURES.guard_cases_note, "string");
  // Every operator the client claims to understand is exercised, plus an
  // unknown one — an operator with no case is an untested fail-open risk.
  const ops = new Set(FIXTURES.guard_cases.map((c) => c.guard.op));
  for (const op of ["eq", "ne", "gt", "gte", "lt", "lte", "in"]) assert.ok(ops.has(op), `no guard case covers op ${op}`);
  assert.ok([...ops].some((op) => !["eq", "ne", "gt", "gte", "lt", "lte", "in"].includes(op)), "no unknown-op case");
});

for (const testCase of FIXTURES.guard_cases) {
  test(`guard case: ${testCase.name}`, async () => {
    try {
      const passed = await guardPassed(testCase.guard, testCase.context);
      assert.equal(
        passed,
        testCase.expect,
        `guard ${JSON.stringify(testCase.guard)} against ${JSON.stringify(testCase.context)}`
      );
    } finally {
      restoreFetch();
    }
  });
}

test("guard cases: shapes the wire fixture cannot express still fail closed", async () => {
  // JSON has no `undefined` and no empty-field-name case worth pinning
  // cross-language; they are TS-local hazards, so they stay hand-written here.
  try {
    for (const [guard, context] of [
      [{ field: "n", op: "eq", value: 1 }, { n: undefined }],
      [{ field: "", op: "eq", value: 1 }, { n: 1 }],
      [{ field: "n", value: 1 }, { n: 1 }],
      [{ field: "n", op: 42, value: 1 }, { n: 1 }]
    ]) {
      assert.equal(await guardPassed(guard, context), false, `guard ${JSON.stringify(guard)} must fail closed`);
      restoreFetch();
    }
  } finally {
    restoreFetch();
  }
});

test("decide: never throws on garbage inputs", async () => {
  try {
    const { client } = await loadedClient(SIGNED_RESPONSE());
    const hostile = {};
    Object.defineProperty(hostile, "stack_trace_location_confidence", {
      enumerable: true,
      get() {
        throw new Error("hostile getter");
      }
    });
    const inputs = [
      { unitKey: "task-a", context: { stack_trace_location_confidence: { nested: [1, 2] }, language: null } },
      { unitKey: "task-a", context: hostile },
      { unitKey: 42, context: [] },
      { unitKey: null, context: null },
      { unitKey: "task-a", context: "not an object" },
      { unitKey: "task-a", context: { stack_trace_location_confidence: Number.NaN, language: ["typescript"] } }
    ];
    for (const options of inputs) {
      const decision = client.decide("fix_failing_test_with_stacktrace", options);
      assert.ok(["execute", "fallback", "baseline"].includes(decision.decision));
      assert.equal(typeof decision.reason, "string");
    }
    for (const family of [null, undefined, 42, "", "unknown_family"]) {
      const decision = client.decide(family, { unitKey: "task-a" });
      assert.equal(decision.decision, "baseline");
      assert.equal(decision.reason, "no_policy");
    }
    assert.doesNotThrow(() => client.decide("fix_failing_test_with_stacktrace"));
  } finally {
    restoreFetch();
  }
});

// ─── decision span ───────────────────────────────────────────────────────────

test("decide: emits one buffered caveman.policy.decision span", async () => {
  try {
    const cave = newCave();
    mockFetch({ body: SIGNED_RESPONSE() });
    const client = cave.runtimePolicy();
    await client.refresh();

    const exporter = cave.exporter();
    const decision = client.decide("fix_failing_test_with_stacktrace", {
      unitKey: "task-a",
      context: { stack_trace_location_confidence: 0.95, language: "typescript" },
      trace: exporter
    });
    assert.equal(exporter.pending, 1, "exactly one span, buffered — no new network path");
    const span = exporter.buildPayload().resourceSpans[0].scopeSpans[0].spans[0];
    assert.equal(span.name, "caveman.policy.decision");
    const attributes = Object.fromEntries(span.attributes.map((kv) => [kv.key, Object.values(kv.value)[0]]));
    assert.equal(attributes["cave.policy.id"], "targeted_test_repair_v3");
    assert.equal(attributes["cave.policy.version"], "7");
    assert.equal(attributes["cave.policy.decision"], "execute");
    assert.equal(attributes["cave.policy.reason"], "applied");
    assert.equal(attributes["cave.policy.signed"], true);
    assert.equal(attributes["cave.experiment.id"], "exp-1");
    assert.equal(attributes["cave.experiment.arm"], "candidate");
    assert.equal(attributes["cave.experiment.propensity"], decision.propensity);
    for (const key of Object.keys(attributes)) {
      assert.ok(!/saving|verified|cost_usd/i.test(key), `decision span must not carry ${key}`);
    }

    // No trace handle ⇒ no emission; a broken sink never breaks routing.
    client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a" });
    assert.equal(exporter.pending, 1);
    const broken = {
      recordSpan() {
        throw new Error("sink is down");
      }
    };
    assert.doesNotThrow(() => client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", trace: broken }));
    for (const notASink of [{}, 42, "exporter", () => {}]) {
      assert.doesNotThrow(() => client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", trace: notASink }));
    }
  } finally {
    restoreFetch();
  }
});

test("decide: baseline decisions still emit a span with the honest reason", async () => {
  try {
    const cave = newCave();
    const client = cave.runtimePolicy();
    const exporter = cave.exporter();
    client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", trace: exporter });
    const span = exporter.buildPayload().resourceSpans[0].scopeSpans[0].spans[0];
    const attributes = Object.fromEntries(span.attributes.map((kv) => [kv.key, Object.values(kv.value)[0]]));
    assert.equal(attributes["cave.policy.reason"], "policy_unavailable");
    assert.equal(attributes["cave.policy.id"], undefined, "no policy matched, so no id is invented");
    assert.equal(attributes["cave.policy.signed"], false);
    assert.equal(attributes["cave.policy.version"], undefined);
  } finally {
    restoreFetch();
  }
});

test("decide: a hostile span sink cannot make decide() throw", async () => {
  // Regression: emitDecisionSpan resolved the sink OUTSIDE its own try, and
  // decide() called it outside the try around evaluate(). A trace object whose
  // property access threw therefore propagated straight out of decide() — the
  // one thing decide() promises never to do.
  try {
    const { client } = await loadedClient(SIGNED_RESPONSE());
    const context = { stack_trace_location_confidence: 0.95, language: "typescript" };

    const throwingRecordSpanGetter = {};
    Object.defineProperty(throwingRecordSpanGetter, "recordSpan", {
      enumerable: true,
      get() {
        throw new Error("hostile recordSpan getter");
      }
    });
    const throwingExporterGetter = {};
    Object.defineProperty(throwingExporterGetter, "exporter", {
      enumerable: true,
      get() {
        throw new Error("hostile exporter getter");
      }
    });
    const throwingExporterFactory = {
      exporter() {
        throw new Error("exporter factory is down");
      }
    };
    const exporterReturningGarbage = { exporter: () => ({}) };
    const exporterReturningNull = { exporter: () => null };
    const throwingRecordSpan = {
      recordSpan() {
        throw new Error("sink is down");
      }
    };
    const throwingBuiltExporter = {
      exporter: () => ({
        recordSpan() {
          throw new Error("built exporter is down");
        }
      })
    };
    // `options` itself can be hostile: decide() reads options.trace.
    const hostileOptions = { unitKey: "task-a", context };
    Object.defineProperty(hostileOptions, "trace", {
      enumerable: true,
      get() {
        throw new Error("hostile trace getter");
      }
    });

    for (const trace of [
      throwingRecordSpanGetter,
      throwingExporterGetter,
      throwingExporterFactory,
      exporterReturningGarbage,
      exporterReturningNull,
      throwingRecordSpan,
      throwingBuiltExporter
    ]) {
      let decision;
      assert.doesNotThrow(() => {
        decision = client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", context, trace });
      }, `sink ${JSON.stringify(Object.keys(trace))} must not break routing`);
      // The routing answer is unaffected by the sink's failure.
      assert.equal(decision.decision, "execute");
      assert.equal(decision.reason, "applied");
    }

    let decision;
    assert.doesNotThrow(() => {
      decision = client.decide("fix_failing_test_with_stacktrace", hostileOptions);
    }, "a throwing options.trace getter must not break routing");
    assert.equal(decision.decision, "execute");
  } finally {
    restoreFetch();
  }
});

test("decide: a cross-realm trace still gets its span (duck-typed, not instanceof)", async () => {
  // Regression: the sink resolver used `trace instanceof CaveTrace`, so a
  // CaveTrace minted by a duplicated copy of this module (bundler realm, dual
  // CJS/ESM install, transitive dependency) silently emitted nothing. Python
  // duck-types; so does this now.
  try {
    const cave = newCave();
    mockFetch({ body: SIGNED_RESPONSE() });
    const client = cave.runtimePolicy();
    await client.refresh();

    // A stand-in for "a CaveTrace from another realm": same shape, foreign
    // prototype, so `instanceof CaveTrace` is false.
    const real = cave.exporter();
    let built = 0;
    const foreignTrace = Object.create(null);
    foreignTrace.traceId = "0".repeat(32);
    foreignTrace.spanId = "0".repeat(16);
    foreignTrace.exporter = () => {
      built += 1;
      return real;
    };

    client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", trace: foreignTrace });
    assert.equal(real.pending, 1, "a cross-realm trace must not silently drop its decision span");
    const span = real.buildPayload().resourceSpans[0].scopeSpans[0].spans[0];
    assert.equal(span.name, "caveman.policy.decision");

    // …and the exporter is still memoized per trace object, not rebuilt per call.
    client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-3", trace: foreignTrace });
    assert.equal(built, 1, "the trace's exporter is built once and reused");
    assert.equal(real.pending, 2);
  } finally {
    restoreFetch();
  }
});

test("decide: a CaveTrace exposes its decision-span exporter for caller flush", async () => {
  try {
    const cave = newCave();
    mockFetch(
      { body: SIGNED_RESPONSE() },
      { body: { ok: true, spans_accepted: 3, spans_total: 3 } }
    );
    const client = cave.runtimePolicy();
    await client.refresh();

    await cave.trace({ workflow: "policy-flow" }, async (trace) => {
      client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-a", trace });
      client.decide("fix_failing_test_with_stacktrace", { unitKey: "task-3", trace });
      const again = client.decide("fix_failing_test_with_stacktrace", {
        unitKey: "task-a",
        context: { stack_trace_location_confidence: 0.95, language: "typescript" },
        trace
      });
      assert.equal(again.decision, "execute");
      const exporter = trace.exporter();
      assert.equal(trace.exporter(), exporter, "default trace exporter is stable and caller-reachable");
      assert.equal(exporter.pending, 3);
      const payload = exporter.buildPayload();
      assert.equal(payload.resourceSpans[0].scopeSpans[0].spans.length, 3);
      const result = await exporter.flush();
      assert.deepEqual(result, { ok: true, spans_accepted: 3, spans_total: 3 });
      assert.equal(exporter.pending, 0);
    });
    assert.equal(captured[1].url, `${CONFIG.base_url}/v1/traces`);
    const exported = JSON.parse(captured[1].body);
    assert.equal(exported.resourceSpans[0].scopeSpans[0].spans.length, 3);
  } finally {
    restoreFetch();
  }
});

// ─── auto refresh ────────────────────────────────────────────────────────────

test("autoRefreshSeconds: timer refreshes in the background and close() clears it", async () => {
  try {
    mockFetch({ body: bundleResponse(BASE_BUNDLE) });
    const client = newCave().runtimePolicy({ autoRefreshSeconds: 0.01 });
    await new Promise((resolve) => setTimeout(resolve, 60));
    client.close();
    assert.ok(captured.length >= 1, "the timer refreshed at least once");
    assert.equal(client.state().policyVersion, 7);
    const seen = captured.length;
    await new Promise((resolve) => setTimeout(resolve, 40));
    assert.equal(captured.length, seen, "close() ends the timer");
  } finally {
    restoreFetch();
  }
});

test("autoRefreshSeconds: overlapping refreshes are skipped, not stacked", async () => {
  // Regression: the timer fired `void this.refresh()` with no in-flight guard,
  // so a slow endpoint queued one outstanding request per tick. Ticks that land
  // while a refresh is in flight are dropped.
  let client;
  let release;
  try {
    captured = [];
    const gate = new Promise((resolve) => {
      release = resolve;
    });
    let started = 0;
    globalThis.fetch = async (url, init) => {
      captured.push({ url: String(url), method: init?.method, headers: init?.headers ?? {}, body: init?.body });
      started += 1;
      await gate;
      const payload = bundleResponse(BASE_BUNDLE);
      return boundedMockResponse(payload);
    };

    client = newCave().runtimePolicy({ autoRefreshSeconds: 0.005 });
    // ~12 ticks' worth of wall clock against one hung request.
    await new Promise((resolve) => setTimeout(resolve, 60));
    assert.equal(started, 1, `expected exactly one in-flight refresh, saw ${started}`);

    release();
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.ok(started > 1, "the timer resumes once the in-flight refresh settles");
    client.close();
    assert.equal(client.state().policyVersion, 7);
  } finally {
    // The timer and the gate must be released even on a failed assertion, or a
    // pending refresh leaks into the next test's fetch mock.
    release?.();
    client?.close();
    restoreFetch();
  }
});

test("refresh: the fetch carries a 30s abort signal", async () => {
  // Mirrors the Python `urlopen(..., timeout=30)`: a hung policy endpoint must
  // not pin an agent process open forever.
  try {
    let seen;
    globalThis.fetch = async (url, init) => {
      seen = init;
      const payload = bundleResponse(BASE_BUNDLE);
      return boundedMockResponse(payload);
    };
    await newCave().runtimePolicy().refresh();
    assert.ok(seen.signal instanceof AbortSignal, "the policy fetch must carry an abort signal");
    assert.equal(seen.signal.aborted, false);
  } finally {
    restoreFetch();
  }
});

test("autoRefreshSeconds: off by default", async () => {
  try {
    mockFetch({ body: bundleResponse(BASE_BUNDLE) });
    newCave().runtimePolicy();
    await new Promise((resolve) => setTimeout(resolve, 30));
    assert.equal(captured.length, 0);
  } finally {
    restoreFetch();
  }
});
