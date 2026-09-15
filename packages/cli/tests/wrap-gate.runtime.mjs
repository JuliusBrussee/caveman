import { test } from "node:test";
import assert from "node:assert";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// resolveWrapGate is a pure, IO-free decision, so it can be imported and unit-tested
// directly (index.js only runs main() under the isCliEntrypoint guard).
const { OFF_STATES, resolveWrapGate, wrapExternalWritesDisabled } = await import(
  pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js")).href
);

test("offline wrap switch blocks account writes without changing gate semantics", () => {
  assert.equal(wrapExternalWritesDisabled({ CAVEMAN_OFFLINE: "1" }), true);
  assert.equal(wrapExternalWritesDisabled({ CAVEMAN_OFFLINE: "0" }), false);
  assert.deepEqual(resolveWrapGate(ent(), NOW, "compress"), { mode: "compress", estimate: false, reason: "entitled" });
});

const NOW = new Date("2026-07-22T12:00:00Z");
const HOUR = 3600 * 1000;
const DAY = 24 * HOUR;

function ent(overrides = {}) {
  return {
    entitled: true,
    plan: "free",
    telemetry_level: "metadata",
    seats_used: 1,
    seats_limit: 1,
    devices_used: 1,
    devices_limit: 3,
    evicted_device_hash: null,
    expires_at: new Date(NOW.getTime() + 72 * HOUR).toISOString(),
    ...overrides,
  };
}

test("user-requested record stays plain record with no estimate", () => {
  assert.deepEqual(resolveWrapGate(ent(), NOW, "record"), { mode: "record", estimate: false, reason: "user-record" });
  // Even with no entitlement, an explicit record request is honored verbatim.
  assert.deepEqual(resolveWrapGate(null, NOW, "record"), { mode: "record", estimate: false, reason: "user-record" });
});

test("valid entitlement keeps the requested compress/pixel mode, no estimate", () => {
  assert.deepEqual(resolveWrapGate(ent(), NOW, "compress"), { mode: "compress", estimate: false, reason: "entitled" });
  assert.deepEqual(resolveWrapGate(ent(), NOW, "pixel"), { mode: "pixel", estimate: false, reason: "entitled" });
});

test("lapsed within 7-day grace keeps the requested mode and is labelled grace", () => {
  const lapsed3d = ent({ expires_at: new Date(NOW.getTime() - 3 * DAY).toISOString() });
  assert.deepEqual(resolveWrapGate(lapsed3d, NOW, "compress"), { mode: "compress", estimate: false, reason: "grace" });
  // Boundary: just inside 7 days is still grace.
  const almost7 = ent({ expires_at: new Date(NOW.getTime() - (7 * DAY - HOUR)).toISOString() });
  assert.equal(resolveWrapGate(almost7, NOW, "compress").reason, "grace");
});

// ── compression is NOT account-gated ────────────────────────────────
// The entitlement labels the account; it never decides whether the local proxy
// compresses. These are the pins that make a re-introduced account gate fail.
test("no entitlement at all still compresses", () => {
  assert.deepEqual(resolveWrapGate(null, NOW, "compress"), { mode: "compress", estimate: false, reason: "unentitled" });
  assert.deepEqual(resolveWrapGate(undefined, NOW, "pixel"), { mode: "pixel", estimate: false, reason: "unentitled" });
});

test("an entitlement expired far beyond grace still compresses", () => {
  const lapsed8d = ent({ expires_at: new Date(NOW.getTime() - 8 * DAY).toISOString() });
  assert.deepEqual(resolveWrapGate(lapsed8d, NOW, "compress"), { mode: "compress", estimate: false, reason: "unentitled" });
  const lapsed1y = ent({ expires_at: new Date(NOW.getTime() - 365 * DAY).toISOString() });
  assert.deepEqual(resolveWrapGate(lapsed1y, NOW, "pixel"), { mode: "pixel", estimate: false, reason: "unentitled" });
});

test("entitled:false or an unreadable expiry still compresses", () => {
  assert.deepEqual(resolveWrapGate(ent({ entitled: false }), NOW, "compress"), { mode: "compress", estimate: false, reason: "unentitled" });
  assert.deepEqual(resolveWrapGate(ent({ expires_at: "not-a-date" }), NOW, "compress"), { mode: "compress", estimate: false, reason: "unentitled" });
});

test("no account state ever downgrades the requested mode", () => {
  for (const entitlement of [null, undefined, ent({ entitled: false }), ent({ expires_at: new Date(NOW.getTime() - 400 * DAY).toISOString() })]) {
    for (const requested of ["compress", "pixel"]) {
      const gate = resolveWrapGate(entitlement, NOW, requested);
      assert.equal(gate.mode, requested, "account state must never change the mode");
      assert.equal(gate.estimate, false, "there is no observe-only fallback any more");
    }
  }
});

test("record stays pass-through no matter the account state", () => {
  for (const entitlement of [ent(), null, undefined, ent({ entitled: false })]) {
    assert.deepEqual(resolveWrapGate(entitlement, NOW, "record"), { mode: "record", estimate: false, reason: "user-record" });
  }
});

// No off-state may claim compression is off for an account reason.
test("OFF_STATES carries no account-derived reason", () => {
  for (const key of ["observe", "seatWall", "denied", "unverified", "grace"]) {
    assert.equal(OFF_STATES[key], undefined, `${key} is an account state and must not gate compression`);
  }
  const accountWords = /sign in|signed in|entitlement lapsed|no seat available|entitlement denied/i;
  for (const [key, value] of Object.entries(OFF_STATES)) {
    const line = typeof value === "function" ? value("a", "b").line : value.line;
    assert.doesNotMatch(line, accountWords, `OFF_STATES.${key} must not blame an account state`);
  }
});

// ── subscription compression channel (local wrap only) ─────────────────────────
// The local proxy compresses subscription/OAuth logins (Claude Pro/Max) with no
// account. Only the user's own record request and the proxy's technical
// conditions can withhold it.
const { subscriptionCompressEnabled, formatSessionSavings } = await import(
  pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js")).href
);

test("subscription compression turns on for any compress session, account or not", () => {
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "compress")), true);
  const lapsed3d = ent({ expires_at: new Date(NOW.getTime() - 3 * DAY).toISOString() });
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(lapsed3d, NOW, "compress")), true, "grace keeps it on");
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(null, NOW, "compress")), true, "no account still compresses");
  const lapsed8d = ent({ expires_at: new Date(NOW.getTime() - 8 * DAY).toISOString() });
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(lapsed8d, NOW, "compress")), true, "an expired entitlement still compresses");
});

test("subscription compression stays off in record, in pixel, and off-gate", () => {
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "record")), false, "--off never mutates bytes");
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(null, NOW, "record")), false, "--off never mutates bytes, account or not");
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "pixel")), false, "pixel is not a subscription path");
  assert.equal(subscriptionCompressEnabled(null), false, "managed traffic is never configured from the wrap gate");
});

// ── session-end display: subscription savings are TOKENS, never dollars ────────
const PAYG_COMPRESS = { spans: 12, tokens_in: 480_000, compression_tokens_saved: 120_000, savings_usd: 4.6 };

test("a PAYG compress session keeps the existing dollar line", () => {
  const lines = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"]);
  assert.deepEqual(lines, [
    "12 requests · 480k tokens sent",
    // No percentage: compression_tokens_saved / tokens_in is a cross-basis ratio
    // (the retro path refuses it). The window is session-start → "this session".
    "compression cut ~120k of those — about $4.60 this session, estimated locally (inferred).",
  ]);
});

test("a subscription compress session reports tokens only and never a dollar figure", () => {
  const lines = formatSessionSavings(
    "compress",
    { spans: 9, tokens_in: 300_000, compression_tokens_saved: 90_000, savings_usd: 0 },
    ["subscription"],
  );
  const text = lines.join("\n");
  assert.doesNotMatch(text, /\$/, "subscription traffic has no per-token price — no dollars anywhere");
  assert.match(text, /compression cut ~90k of those/);
  assert.doesNotMatch(text, /of those \(\d+%\)/, "no cross-basis percentage on the compress line");
  assert.match(text, /local o200k estimate/, "token counts must declare the local estimator");
});

test("a mixed session scopes its dollar figure to the API-key traffic", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg", "subscription"]).join("\n");
  assert.match(text, /about \$4\.60 this session on the API-key traffic/);
  assert.match(text, /counted in tokens only/);
});

// OAuth rows are list-price-eligible on Vertex ALONE, and this window carries no
// provider — so an oauth session gets the same scope clause + tokens-only note as a
// subscription one. Folding it into an unqualified dollar figure would claim a
// saving on traffic that may have no readable price. (honesty rule: no-fake-savings)
test("an oauth session is scoped and noted exactly like a subscription one", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg", "oauth"]).join("\n");
  assert.match(text, /about \$4\.60 this session on the API-key traffic/);
  assert.match(text, /subscription and OAuth logins are counted in tokens only/);
});

// The auth-mode window is a capped page of recent rows. A full page back means the
// window is truncated: it cannot prove no tokens-only rows were in scope, so the
// qualifier fires unconditionally rather than trusting a partial view.
test("a truncated auth-mode window qualifies even when it only saw API-key rows", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"], true).join("\n");
  assert.match(text, /about \$4\.60 this session on the API-key traffic/);
  assert.match(text, /subscription and OAuth logins are counted in tokens only/);
});

test("an untruncated all-API-key session stays unqualified", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"], false).join("\n");
  assert.doesNotMatch(text, /on the API-key traffic/);
  assert.doesNotMatch(text, /counted in tokens only/);
});

test("compressed-parts before/after render when the proxy reports them", () => {
  const text = formatSessionSavings(
    "compress",
    { ...PAYG_COMPRESS, compression_tokens_before: 210_000, compression_tokens_after: 90_000 },
    ["payg"],
  ).join("\n");
  assert.match(text, /compressed parts 210k → 90k/);
});

// The before/after pair is a REAL field of the proxy's compact summary
// (store.ObserveSummary), not a synthetic one: the numbers must be internally
// consistent with the saved delta the same object reports, and an older proxy that
// omits them must degrade to the delta line rather than invent a pair.
test("compressed-parts render from the proxy's own before/after/saved triple", () => {
  const summary = {
    spans: 12,
    tokens_in: 480_000,
    compression_tokens_before: 210_000,
    compression_tokens_after: 90_000,
    compression_tokens_saved: 120_000,
    savings_usd: 4.6,
  };
  assert.equal(
    summary.compression_tokens_before - summary.compression_tokens_after,
    summary.compression_tokens_saved,
    "the contract is saved = before - after",
  );
  assert.match(formatSessionSavings("compress", summary, ["payg"]).join("\n"), /compressed parts 210k → 90k/);
});

test("a proxy summary without the before/after pair reports the delta alone", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"]).join("\n");
  assert.doesNotMatch(text, /compressed parts/);
});

test("the observe block uses inferred local-estimate wording and nudges to compress mode", () => {
  const lines = formatSessionSavings(
    "observe",
    { spans: 20, tokens_in: 510_000, would_save_tokens: 310_000, would_save_pct: 0.61, would_save_usd: 4.6 },
    ["payg"],
  );
  assert.deepEqual(lines, [
    "20 requests · 510k tokens sent",
    "compression would have cut ~310k of those (61%) — about $4.60 this session,\nestimated locally (inferred).",
    "apply it:  caveman tools config set think.mode compress",
  ]);
});

test("unmeasurable observe sessions end with an honest line, never silence", () => {
  assert.deepEqual(formatSessionSavings("observe", { spans: 0, tokens_in: 0 }), [
    "no compressible context in this session — observe mode found nothing to measure",
  ]);
});

test("a pre-eligibility proxy with no cut asks for an update, never claims byte-safety", () => {
  for (const summary of [
    { spans: 0, tokens_in: 0 },
    { spans: 5, tokens_in: 100, compression_tokens_saved: 0 },
  ]) {
    const lines = formatSessionSavings("compress", summary);
    assert.equal(lines.length, 1);
    assert.match(lines[0], /routing status is unknown/);
    assert.match(lines[0], /runtime is out of date/);
    assert.match(lines[0], /caveman setup --install/);
    assert.doesNotMatch(lines[0], /byte-safe/);
  }
});

// ── issue #129: the three states behind the old "byte-safe" line are distinct ──

// (c) A null summary means the proxy could not be read — do NOT claim byte-safety
// or savings we never measured; name the cause instead.
test("a null summary prints a distinct not-measured line, never byte-safe success", () => {
  for (const kind of ["observe", "compress"]) {
    const lines = formatSessionSavings(kind, null, [], false, "claude");
    assert.equal(lines.length, 1);
    assert.match(lines[0], /could not read proxy stats/);
    assert.match(lines[0], /binary missing, db locked, or timed out/);
    assert.doesNotMatch(lines[0], /byte-safe/, "not-measured is not a byte-safe success");
    assert.doesNotMatch(lines[0], /\bcut\b/, "claims no savings when nothing was measured");
  }
});

// (b→routing) Zero eligible requests → the injection never reached compression;
// blame routing and point at the doctor for the specific agent, not byte-safety.
test("a compress session with zero eligible requests names a routing failure, not byte-safety", () => {
  const lines = formatSessionSavings(
    "compress",
    { spans: 4, tokens_in: 120_000, requests_eligible_for_compression: 0, compression_tokens_saved: 0 },
    ["payg"],
    false,
    "codex",
  );
  assert.equal(lines.length, 1);
  assert.match(lines[0], /never reached the compression layer/);
  assert.match(lines[0], /routing may not have applied/);
  assert.match(lines[0], /caveman doctor codex/);
  assert.doesNotMatch(lines[0], /byte-safe/);
});

// With no agent id, the doctor command falls back to a self-documenting placeholder.
test("the routing-failure line uses a <agent> placeholder when the agent is unknown", () => {
  const lines = formatSessionSavings(
    "compress",
    { spans: 0, tokens_in: 0, requests_eligible_for_compression: 0 },
    [],
  );
  assert.match(lines[0], /caveman doctor <agent>/);
});

// A wrappable agent that `caveman doctor` does not accept (openclaw) must map to
// `generic` so the remediation hint is a runnable command, not a usage error.
test("the routing-failure line maps an unsupported doctor target to generic", () => {
  const lines = formatSessionSavings(
    "compress",
    { spans: 4, tokens_in: 120_000, requests_eligible_for_compression: 0, compression_tokens_saved: 0 },
    ["payg"],
    false,
    "openclaw",
  );
  assert.match(lines[0], /caveman doctor generic/);
  assert.doesNotMatch(lines[0], /caveman doctor openclaw/);
});

// (b→broken) Eligible requests but a zero cut → compression ran and saved nothing;
// warn and point at the doctor rather than printing a byte-safe win.
test("a compress session that ran but saved nothing warns and points at the doctor", () => {
  const lines = formatSessionSavings(
    "compress",
    { spans: 6, tokens_in: 200_000, requests_eligible_for_compression: 6, compression_tokens_saved: 0 },
    ["payg"],
    false,
    "claude",
  );
  assert.equal(lines.length, 1);
  assert.match(lines[0], /compression ran on 6 requests this session but saved nothing/);
  assert.match(lines[0], /caveman doctor claude/);
  assert.doesNotMatch(lines[0], /byte-safe/);
});

// (a) Eligible requests AND a real cut → the honest positive line still renders.
test("a compress session with eligible requests and a real cut still renders the positive line", () => {
  const text = formatSessionSavings(
    "compress",
    { ...PAYG_COMPRESS, requests_eligible_for_compression: 12 },
    ["payg"],
  ).join("\n");
  assert.match(text, /compression cut ~120k of those/);
  assert.doesNotMatch(text, /doctor/);
});

// headline_compression_refused mirrors the proxy: a cache-write-heavy window has no
// honest compression headline, so we refuse it rather than over-claim a small cut.
test("a cache-write-heavy session refuses the compression headline", () => {
  const text = formatSessionSavings(
    "compress",
    {
      spans: 8,
      tokens_in: 300_000,
      requests_eligible_for_compression: 8,
      compression_tokens_saved: 60,
      compression_tokens_before: 260,
      compression_tokens_after: 200,
      cached_input_tokens: 1_000,
      cache_creation_input_tokens: 144_000,
      headline_compression_refused: true,
      savings_usd: 0.01,
    },
    ["payg"],
  ).join("\n");
  assert.match(text, /cache-write heavy/);
  assert.doesNotMatch(text, /compression cut/, "no compression headline when writes dominate");
  assert.doesNotMatch(text, /\$/, "no dollar headline for a refused session");
});

test("every session-end rendering rejects managed-proof basis words", () => {
  for (const kind of ["observe", "compress"]) {
    const text = [
      ...formatSessionSavings(kind, PAYG_COMPRESS, ["payg"]),
      ...formatSessionSavings(kind, { spans: 0, tokens_in: 0 }),
    ].join("\n");
    assert.doesNotMatch(text, /\b(?:measured|verified)\b/i);
  }
});
