import { test } from "node:test";
import assert from "node:assert";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// resolveWrapGate is a pure, IO-free decision, so it can be imported and unit-tested
// directly (index.js only runs main() under the isCliEntrypoint guard).
const { resolveWrapGate } = await import(
  pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js")).href
);

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

test("lapsed within 7-day grace keeps compression on", () => {
  const lapsed3d = ent({ expires_at: new Date(NOW.getTime() - 3 * DAY).toISOString() });
  assert.deepEqual(resolveWrapGate(lapsed3d, NOW, "compress"), { mode: "compress", estimate: false, reason: "grace" });
  // Boundary: just inside 7 days is still grace.
  const almost7 = ent({ expires_at: new Date(NOW.getTime() - (7 * DAY - HOUR)).toISOString() });
  assert.equal(resolveWrapGate(almost7, NOW, "compress").reason, "grace");
});

test("lapsed beyond 7 days degrades to observe-only estimate", () => {
  const lapsed8d = ent({ expires_at: new Date(NOW.getTime() - 8 * DAY).toISOString() });
  assert.deepEqual(resolveWrapGate(lapsed8d, NOW, "compress"), { mode: "record", estimate: true, reason: "observe" });
});

test("no entitlement / never logged in → observe-only estimate", () => {
  assert.deepEqual(resolveWrapGate(null, NOW, "compress"), { mode: "record", estimate: true, reason: "observe" });
  assert.deepEqual(resolveWrapGate(undefined, NOW, "pixel"), { mode: "record", estimate: true, reason: "observe" });
});

test("entitled:false or unparseable expiry fails safe to observe", () => {
  assert.equal(resolveWrapGate(ent({ entitled: false }), NOW, "compress").reason, "observe");
  assert.equal(resolveWrapGate(ent({ expires_at: "not-a-date" }), NOW, "compress").reason, "observe");
});

test("observe path never enables compression — mode is always record", () => {
  for (const requested of ["compress", "pixel"]) {
    const gate = resolveWrapGate(null, NOW, requested);
    assert.equal(gate.mode, "record", "no entitlement must force byte-safe record mode");
    assert.equal(gate.estimate, true, "observe must turn on the would-have-saved estimate");
  }
});

// ── subscription compression channel (local wrap only) ─────────────────────────
// The local proxy may compress subscription/OAuth logins (Claude Pro/Max) only
// with a valid account. Everything else keeps today's byte-identical pass-through.
const { subscriptionCompressEnabled, formatSessionSavings } = await import(
  pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js")).href
);

test("subscription compression turns on only for an entitled/grace compress session", () => {
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "compress")), true);
  const lapsed3d = ent({ expires_at: new Date(NOW.getTime() - 3 * DAY).toISOString() });
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(lapsed3d, NOW, "compress")), true, "grace keeps it on");
});

test("subscription compression stays off without an entitlement, in record, in pixel, and off-gate", () => {
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(null, NOW, "compress")), false, "no account = pass-through");
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "record")), false, "--off never mutates bytes");
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(ent(), NOW, "pixel")), false, "pixel is not a subscription path");
  assert.equal(subscriptionCompressEnabled(null), false, "managed traffic is never configured from the wrap gate");
  const lapsed8d = ent({ expires_at: new Date(NOW.getTime() - 8 * DAY).toISOString() });
  assert.equal(subscriptionCompressEnabled(resolveWrapGate(lapsed8d, NOW, "compress")), false, "expired past grace = observe-only");
});

// ── session-end display: subscription savings are TOKENS, never dollars ────────
const PAYG_COMPRESS = { spans: 12, tokens_in: 480_000, compression_tokens_saved: 120_000, savings_usd: 4.6 };

test("a PAYG compress session keeps the existing dollar line", () => {
  const lines = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"]);
  assert.deepEqual(lines, [
    "12 requests · 480k tokens sent",
    "compression cut ~120k of those (25%) — about $4.60 today, estimated locally (inferred).",
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
  assert.match(text, /compression cut ~90k of those \(30%\)/);
  assert.match(text, /local o200k estimate/, "token counts must declare the local estimator");
});

test("a mixed session scopes its dollar figure to the API-key traffic", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg", "subscription"]).join("\n");
  assert.match(text, /about \$4\.60 today on the API-key traffic/);
  assert.match(text, /counted in tokens only/);
});

// OAuth rows are list-price-eligible on Vertex ALONE, and this window carries no
// provider — so an oauth session gets the same scope clause + tokens-only note as a
// subscription one. Folding it into an unqualified dollar figure would claim a
// saving on traffic that may have no readable price. (honesty rule: no-fake-savings)
test("an oauth session is scoped and noted exactly like a subscription one", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg", "oauth"]).join("\n");
  assert.match(text, /about \$4\.60 today on the API-key traffic/);
  assert.match(text, /subscription and OAuth logins are counted in tokens only/);
});

// The auth-mode window is a capped page of recent rows. A full page back means the
// window is truncated: it cannot prove no tokens-only rows were in scope, so the
// qualifier fires unconditionally rather than trusting a partial view.
test("a truncated auth-mode window qualifies even when it only saw API-key rows", () => {
  const text = formatSessionSavings("compress", PAYG_COMPRESS, ["payg"], true).join("\n");
  assert.match(text, /about \$4\.60 today on the API-key traffic/);
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

test("the observe block uses inferred local-estimate wording and still nudges to login", () => {
  const lines = formatSessionSavings(
    "observe",
    { spans: 20, tokens_in: 510_000, would_save_tokens: 310_000, would_save_pct: 0.61, would_save_usd: 4.6 },
    ["payg"],
  );
  assert.deepEqual(lines, [
    "20 requests · 510k tokens sent",
    "compression would have cut ~310k of those (61%) — about $4.60 today,\nestimated locally (inferred).",
    "turn it on:  caveman login   (free · 1 seat · no card)",
  ]);
});

test("unmeasurable sessions end with honest state-specific lines, never silence", () => {
  assert.deepEqual(formatSessionSavings("observe", { spans: 0, tokens_in: 0 }), [
    "no compressible context in this session — `caveman login` (free · 1 seat · no card) turns compression on when there is",
  ]);
  assert.deepEqual(formatSessionSavings("compress", { spans: 0, tokens_in: 0 }), [
    "nothing compressible in this session — the layer stayed byte-safe; `caveman status` shows today's totals",
  ]);
  assert.deepEqual(formatSessionSavings("compress", { spans: 5, tokens_in: 100, compression_tokens_saved: 0 }), [
    "nothing compressible in this session — the layer stayed byte-safe; `caveman status` shows today's totals",
  ]);
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
