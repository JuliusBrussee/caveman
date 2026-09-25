// Run: node --test supabase/functions/cli-telemetry/ingest.test.ts (Node >= 22.18)
import { test } from "node:test";
import assert from "node:assert/strict";
import { clientIp, readBounded, validateEvent } from "./ingest.ts";

const base = {
  schema: "cli/v1",
  anonymous_id: "11111111-1111-4111-8111-111111111111",
  cli_version: "1.3.5",
  os: "darwin",
  arch: "arm64",
  node_major: 22,
  ts: "2026-09-24T12:00:00.000Z",
  // telemetryContext() rides on every event.
  account: "none",
  install_channel: "npm",
  timezone: "Europe/Amsterdam",
  locale: "en-US",
};

// Shapes copied from the emitters in packages/cli/src/index.ts.
const commandRun = { ...base, event: "command_run", command: "wrap", subcommand: "run", agent: "claude", duration_ms: 1234, exit_class: "ok", tokens_processed: 5000, tokens_saved: 1200, tokens_basis: "inferred" };
const consentGranted = { ...base, event: "consent_granted" };
const runtimeBootstrap = { ...base, event: "runtime_bootstrap", command: "wrap", subcommand: "install", duration_ms: 900, exit_class: "error", error_class: "exec_failed" };
const agentToolCall = { ...base, event: "agent_tool_call", command: "mcp", subcommand: "caveman_trace_get", duration_ms: 40, exit_class: "ok", error_class: "" };
const firstRun = { ...base, event: "first_run", scan_ok: true, sessions_total: 12, sessions_scanned: 10, tokens_observed: 900000, would_cut_tokens: 120000, would_cut_stream_tokens: null, engine_used: true, time_boxed: false, logged_in: false, duration_ms: 20000 };
const engineMeasured = {
  ...base, event: "engine_session", command: "wrap", agent: "codex", duration_ms: 60000, exit_class: "ok",
  measurement_mode: "compress", measurement_ok: true, requests_observed: 40, input_tokens_observed: 800000,
  compression_eligible_requests: 30, compression_eligibility_known: true, compression_tokens_before: 200000,
  compression_tokens_after: 80000, compression_tokens_saved: 120000, compression_pair_known: true,
  estimated_cut_tokens: 0, cache_read_tokens: 500000, cache_write_tokens: 20000, cache_bust_requests: 1, headline_suppressed: false,
};
const sessionStart = { ...base, event: "session_start", agent: "claude", session_source: "startup", tokens_processed: 800, tokens_saved: 90, tokens_basis: "inferred" };
// engineSessionTelemetryFields' fail-closed `empty` object.
const engineUnmeasured = {
  ...base, event: "engine_session", command: "wrap", duration_ms: 1000, exit_class: "error", error_class: "other",
  measurement_mode: "observe", measurement_ok: false, requests_observed: 0, input_tokens_observed: 0,
  compression_eligible_requests: 0, compression_eligibility_known: false, compression_tokens_before: 0,
  compression_tokens_after: 0, compression_tokens_saved: 0, compression_pair_known: false, estimated_cut_tokens: 0,
  cache_read_tokens: 0, cache_write_tokens: 0, cache_bust_requests: 0, headline_suppressed: false,
};

test("accepts every event shape the CLI emits", () => {
  for (const event of [commandRun, consentGranted, runtimeBootstrap, agentToolCall, firstRun, engineMeasured, engineUnmeasured, sessionStart]) {
    assert.ok(validateEvent(event), `${event.event} should be accepted`);
  }
});

test("stores owned fields only and normalizes", () => {
  const row = validateEvent({ ...commandRun, anonymous_id: base.anonymous_id.toUpperCase(), ts: "2026-09-24T14:00:00+02:00", prompt: "secret" })!;
  assert.equal(row.anonymous_id, base.anonymous_id);
  assert.equal(row.ts, "2026-09-24T12:00:00.000Z");
  assert.equal(row.tokens_saved, 1200);
  assert.equal(row.tokens_basis, "inferred");
  assert.equal("prompt" in row, false, "unknown fields never reach the row");
  assert.equal("sessions_total" in row, false, "other events' aggregates are not stored");

  const consent = validateEvent(consentGranted)!;
  assert.equal(consent.command, null);
  assert.equal(consent.duration_ms, null, "absent is stored as null, not 0");
  assert.equal(consent.exit_class, null);

  assert.ok(validateEvent({ ...commandRun, tokens_basis: "estimated_request_json_o200k_v1" }), "proxy basis ids carry digits");

  const env = validateEvent({ ...consentGranted, plan: "team", timezone: "Etc/GMT+3" })!;
  assert.equal(env.plan, "team");
  assert.equal(env.timezone, "Etc/GMT+3");
  const odd = validateEvent({ ...consentGranted, account: "admin", timezone: "Europe/Amsterdam; drop", locale: 42, plan: "pro plan" });
  assert.ok(odd, "a malformed descriptor must not cost the event");
  assert.equal(odd!.account, null);
  assert.equal(odd!.timezone, null);
  assert.equal(odd!.locale, null);
  assert.equal(odd!.plan, null);
  assert.equal(validateEvent(sessionStart)!.tokens_saved, 90);
  assert.equal(validateEvent(sessionStart)!.session_source, "startup");
  assert.equal(validateEvent({ ...sessionStart, session_source: undefined })!.session_source, null);

  const tool = validateEvent(agentToolCall)!;
  assert.equal(tool.error_class, null);

  const first = validateEvent(firstRun)!;
  assert.equal(first.would_cut_stream_tokens, null, "null stays distinguishable from a measured 0");
  assert.equal(first.sessions_scanned, 10);
});

test("rejects malformed, free-text, and misattributed events", () => {
  const rejects: [string, unknown][] = [
    ["non-object", "command_run"],
    ["array", [commandRun]],
    ["wrong schema", { ...commandRun, schema: "cli/v2" }],
    ["bad uuid", { ...commandRun, anonymous_id: "not-a-uuid" }],
    ["unknown event", { ...commandRun, event: "page_view" }],
    ["unknown os", { ...commandRun, os: "templeos" }],
    ["unknown arch", { ...commandRun, arch: "z80" }],
    ["free-text command", { ...commandRun, command: "rm -rf /" }],
    ["overlong version", { ...commandRun, cli_version: "1".repeat(65) }],
    ["string number", { ...commandRun, duration_ms: "12" }],
    ["float node_major", { ...commandRun, node_major: 22.5 }],
    ["node_major too big", { ...commandRun, node_major: 256 }],
    ["duration too big", { ...commandRun, duration_ms: 2 ** 32 }],
    ["negative duration", { ...commandRun, duration_ms: -1 }],
    ["missing ts", { ...commandRun, ts: undefined }],
    ["non-RFC3339 ts", { ...commandRun, ts: "September 24, 2026" }],
    ["zero ts", { ...commandRun, ts: "0001-01-01T00:00:00Z" }],
    ["missing exit_class", { ...commandRun, exit_class: undefined }],
    ["bogus exit_class", { ...commandRun, exit_class: "crashed" }],
    ["exit_class on consent", { ...consentGranted, exit_class: "ok" }],
    ["exit_class on first_run", { ...firstRun, exit_class: "ok" }],
    ["unknown error_class", { ...runtimeBootstrap, error_class: "segfault" }],
    ["bootstrap wrong shape", { ...runtimeBootstrap, command: "run" }],
    ["tool call unknown tool", { ...agentToolCall, subcommand: "shell" }],
    ["tool call with agent", { ...agentToolCall, agent: "claude" }],
    ["engine with subcommand", { ...engineMeasured, subcommand: "run" }],
    ["scan fields on command_run", { ...commandRun, sessions_total: 3 }],
    ["engine fields on command_run", { ...commandRun, requests_observed: 3 }],
    ["tokens on engine_session", { ...engineMeasured, tokens_processed: 1, tokens_saved: 0, tokens_basis: "inferred" }],
    ["tokens without basis", { ...commandRun, tokens_basis: undefined }],
    ["tokens with free-text basis", { ...commandRun, tokens_basis: "trust me" }],
    ["tokens too big", { ...commandRun, tokens_processed: 2 ** 50 }],
    ["first_run scanned > total", { ...firstRun, sessions_scanned: 13 }],
    ["first_run cut > observed", { ...firstRun, would_cut_tokens: 900001 }],
    ["first_run stream > observed", { ...firstRun, would_cut_stream_tokens: 900001 }],
    ["engine unknown mode", { ...engineMeasured, measurement_mode: "guess" }],
    ["engine saved != before-after", { ...engineMeasured, compression_tokens_saved: 1 }],
    ["engine eligible > requests", { ...engineMeasured, compression_eligible_requests: 41 }],
    ["engine unmeasured with numbers", { ...engineUnmeasured, input_tokens_observed: 5 }],
    ["engine observe claiming saved", { ...engineMeasured, measurement_mode: "observe" }],
    ["engine compress with estimate", { ...engineMeasured, estimated_cut_tokens: 5 }],
    ["engine pair unknown with numbers", { ...engineMeasured, compression_pair_known: false }],
    ["session without agent", { ...sessionStart, agent: undefined }],
    ["session with command", { ...sessionStart, command: "wrap" }],
    ["session with exit_class", { ...sessionStart, exit_class: "ok" }],
    ["session with free-text source", { ...sessionStart, session_source: "tmux restore" }],
    ["session_source on command_run", { ...commandRun, session_source: "startup" }],
  ];
  for (const [name, event] of rejects) assert.equal(validateEvent(event), null, name);
});

test("client IP comes from the edge, not the client", () => {
  const h = (init: Record<string, string>) => new Headers(init);
  assert.equal(clientIp(h({ "cf-connecting-ip": "98.207.59.2", "x-forwarded-for": "203.0.113.9" })), "98.207.59.2");
  assert.equal(clientIp(h({ "cf-connecting-ip": "2001:db8::1" })), "2001:db8::1");
  assert.equal(clientIp(h({ "x-forwarded-for": "198.51.100.7, 10.0.0.1" })), null, "x-forwarded-for can be client-chosen");
  assert.equal(clientIp(h({ "true-client-ip": "203.0.113.12" })), null, "true-client-ip is client-controlled");
  assert.equal(clientIp(h({ "cf-connecting-ip": "not an ip" })), null);
  assert.equal(clientIp(h({})), null);
});

test("body reads are bounded", async () => {
  const stream = (text: string) => new Response(text).body;
  assert.equal(await readBounded(stream("[1,2]"), 10), "[1,2]");
  assert.equal(await readBounded(stream("x".repeat(11)), 10), null);
  assert.equal(await readBounded(null, 10), null);
});
