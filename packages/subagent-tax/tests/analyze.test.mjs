import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { analyzeCapture, pickPrimary, CLAUDE_MCP_PATTERN } from "../lib/analyze.mjs";

const fixture = JSON.parse(
  readFileSync(join(dirname(fileURLToPath(import.meta.url)), "..", "fixtures", "anthropic-first-request.json"), "utf8"),
);

const record = (kind, body, seq = 1) => ({ seq, kind, url: "/x", body_bytes: JSON.stringify(body).length, body });

test("anthropic capture analysis + mcp split when a pattern is supplied", () => {
  const a = analyzeCapture(record("anthropic-messages", fixture), { mcpPattern: CLAUDE_MCP_PATTERN });
  assert.equal(a.model, "claude-opus-5");
  assert.equal(a.tools_count, 3);
  assert.equal(a.mcp_tools_count, 1);
  assert.ok(a.mcp_tools_chars > 0 && a.mcp_tools_chars < a.tools_chars);
  assert.ok(a.system_chars > 100);
  assert.ok(a.messages_chars > 0);
  assert.equal(a.total_chars, JSON.stringify(fixture).length);
  const names = a.tools.map((t) => t.name);
  assert.deepEqual(names, ["Bash", "Read", "mcp__example__do_thing"]);
});

test("mcp counts are null (unknown), never a false zero, without a verified pattern", () => {
  const a = analyzeCapture(record("anthropic-messages", fixture));
  assert.equal(a.mcp_tools_count, null);
  assert.equal(a.mcp_tools_chars, null);
});

test("anthropic system-role MESSAGES count as system, not as conversation", () => {
  const bigSystemMsg = "S".repeat(5000);
  const a = analyzeCapture(
    record("anthropic-messages", {
      model: "m",
      system: "short top-level system",
      messages: [
        { role: "system", content: bigSystemMsg },
        { role: "user", content: "hi" },
      ],
    }),
  );
  assert.ok(a.system_chars > 5000, `system_chars ${a.system_chars} must include the system-role message`);
  assert.ok(a.messages_chars < 100, "the user turn alone remains in messages");
});

test("a malformed capture is skipped, never fatal", () => {
  const bad = analyzeCapture(record("gemini-generatecontent", { tools: { functionDeclarations: [{ name: "x" }] }, contents: [] }));
  assert.equal(bad.unparsed, true);
  assert.ok(bad.error);
  const bad2 = analyzeCapture(record("openai-chat", { messages: "oops" }));
  assert.ok(bad2 === null || bad2.unparsed === true);

  // and it must not take the rest of the run down with it
  const good = record("anthropic-messages", fixture, 2);
  const { primary, skipped } = pickPrimary([record("gemini-generatecontent", { tools: { bad: true } }, 1), good]);
  assert.equal(primary.seq, 2);
  assert.equal(skipped.length, 1);
});

test("openai-chat analysis: system from messages, tool names from function", () => {
  const a = analyzeCapture(
    record("openai-chat", {
      model: "gpt-4o",
      messages: [
        { role: "system", content: "sys" },
        { role: "user", content: "hi" },
      ],
      tools: [{ type: "function", function: { name: "bash", parameters: {} } }],
    }),
  );
  assert.equal(a.tools_count, 1);
  assert.equal(a.tools[0].name, "bash");
  assert.ok(a.system_chars > 0);
  assert.ok(a.messages_chars > 0);
});

test("openai-responses analysis: instructions + flat tools", () => {
  const a = analyzeCapture(
    record("openai-responses", {
      model: "gpt-5.5",
      instructions: "be a good agent",
      input: [{ role: "user", content: "hi" }],
      tools: [
        { type: "function", name: "shell", parameters: {} },
        { type: "local_shell" },
      ],
    }),
  );
  assert.equal(a.tools_count, 2);
  assert.deepEqual(a.tools.map((t) => t.name), ["shell", "local_shell"]);
  assert.ok(a.system_chars > 0);
});

test("gemini analysis: functionDeclarations flattened", () => {
  const a = analyzeCapture(
    record("gemini-generatecontent", {
      systemInstruction: { parts: [{ text: "sys" }] },
      contents: [{ role: "user", parts: [{ text: "hi" }] }],
      tools: [{ functionDeclarations: [{ name: "run_shell_command" }, { name: "read_file" }] }],
    }),
  );
  assert.equal(a.tools_count, 2);
  assert.deepEqual(a.tools.map((t) => t.name), ["run_shell_command", "read_file"]);
});

test("openai-responses: system-role input items count as system (opencode shape)", () => {
  const a = analyzeCapture(
    record("openai-responses", {
      model: "gpt-4o",
      input: [
        { role: "system", content: [{ type: "input_text", text: "big system prompt" }] },
        { role: "user", content: [{ type: "input_text", text: "hi" }] },
      ],
      tools: [{ type: "function", name: "bash" }],
    }),
  );
  assert.ok(a.system_chars > 0, "system-role input items are system");
  assert.ok(a.messages_chars > 0 && a.messages_chars < a.system_chars);
});

test("pickPrimary ties break to the earliest capture, not the grown later turn", () => {
  const first = record("openai-responses", { model: "m", input: [], tools: [{ type: "function", name: "bash" }] }, 1);
  const grown = record("openai-responses", { model: "m", input: [{ role: "user", content: "long history ".repeat(50) }], tools: [{ type: "function", name: "bash" }] }, 2);
  const { primary, pick_rule } = pickPrimary([first, grown]);
  assert.equal(primary.seq, 1);
  assert.equal(pick_rule, "most-tools");
});

test("pickPrimary is not fooled by a small router/classifier call that carries one tool", () => {
  const router = record("anthropic-messages", { model: "router", tools: [{ name: "classify", input_schema: {} }], messages: [{ role: "user", content: "hi" }] }, 1);
  const realPrefix = record("anthropic-messages", fixture, 2);
  const { primary, pick_rule } = pickPrimary([router, realPrefix]);
  assert.equal(primary.seq, 2, "the 3-tool real prefix must win over the 1-tool router call");
  assert.equal(primary.tools_count, 3);
  assert.equal(pick_rule, "most-tools");
});

test("pickPrimary falls back to largest body when nothing carries tools, and says so", () => {
  const small = record("anthropic-messages", { model: "m", messages: [{ role: "user", content: "ping" }] }, 1);
  const big = record("anthropic-messages", { model: "m", messages: [{ role: "user", content: "x".repeat(4000) }] }, 2);
  const { primary, pick_rule } = pickPrimary([small, big]);
  assert.equal(primary.seq, 2);
  assert.match(pick_rule, /largest-body/);
});

test("pickPrimary skips non-LLM captures entirely", () => {
  const main = record("anthropic-messages", fixture, 1);
  const nonLlm = { seq: 2, kind: "other", url: "/telemetry", body_bytes: 999999, body: { huge: "x" } };
  const { primary, all } = pickPrimary([main, nonLlm]);
  assert.equal(primary.seq, 1);
  assert.equal(all.length, 1);
});

test("pickPrimary with no LLM captures returns null", () => {
  const { primary, all, pick_rule } = pickPrimary([{ seq: 1, kind: "other", url: "/x", body_bytes: 5, body: {} }]);
  assert.equal(primary, null);
  assert.equal(all.length, 0);
  assert.equal(pick_rule, null);
});
