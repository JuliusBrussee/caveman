/**
 * Span-processor tests: the mapping table for each documented Mastra span kind,
 * unmapped passthrough + diagnostics, and id preservation. No network.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { cavemanSpanProcessor, withTask } from "../dist/index.js";

function makeSpan(name, attributes = {}) {
  const attrs = { ...attributes };
  return {
    name,
    attributes: attrs,
    setAttribute(key, value) {
      attrs[key] = value;
    },
    spanContext() {
      return { traceId: "0af7651916cd43dd8448eb211c80319c", spanId: "b7ad6b7169203331" };
    },
  };
}

function run(processor, span) {
  processor.onStart(span);
  processor.onEnd(span);
  return span.attributes;
}

test("declared span types map onto gen_ai operations", () => {
  const cases = [
    ["AGENT_RUN", "invoke_agent"],
    ["WORKFLOW_RUN", "invoke_workflow"],
    ["WORKFLOW_STEP", "workflow_step"],
    ["MODEL_GENERATION", "chat"],
    ["LLM_GENERATION", "chat"],
    ["TOOL_CALL", "execute_tool"],
    ["MCP_TOOL_CALL", "execute_tool"],
  ];
  for (const [spanType, operation] of cases) {
    const processor = cavemanSpanProcessor();
    const attrs = run(processor, makeSpan("some name", { "mastra.span.type": spanType }));
    assert.equal(attrs["gen_ai.operation.name"], operation, spanType);
    assert.equal(processor.diagnostics().mappedSpans, 1, spanType);
  }
});

test("sub-spans and control flow are known but carry no operation label", () => {
  for (const spanType of [
    "MODEL_STEP",
    "MODEL_CHUNK",
    "LLM_CHUNK",
    "GENERIC",
    "WORKFLOW_LOOP",
    "WORKFLOW_PARALLEL",
    "WORKFLOW_CONDITIONAL",
    "WORKFLOW_CONDITIONAL_EVAL",
    "WORKFLOW_SLEEP",
  ]) {
    const processor = cavemanSpanProcessor({ agentId: "support-agent" });
    const attrs = run(processor, makeSpan("chunk", { "mastra.span.type": spanType }));
    assert.equal("gen_ai.operation.name" in attrs, false, spanType);
    assert.equal(attrs["caveman.agent.id"], "support-agent", spanType);
    const diagnostics = processor.diagnostics();
    assert.equal(diagnostics.contextOnlySpans, 1, spanType);
    assert.equal(diagnostics.unmappedSpans, 0, spanType);
  }
});

test("tool spans carry the tool name and type", () => {
  const processor = cavemanSpanProcessor();
  const tool = run(processor, makeSpan("execute_tool search_tickets", { "mastra.span.type": "TOOL_CALL" }));
  assert.equal(tool["gen_ai.tool.name"], "search_tickets");
  assert.equal(tool["gen_ai.tool.type"], "function");

  const mcp = run(
    cavemanSpanProcessor({ toolNameAttributes: ["mastra.tool.id"] }),
    makeSpan("mcp call", { "mastra.span.type": "MCP_TOOL_CALL", "mastra.tool.id": "github.create_issue" }),
  );
  assert.equal(mcp["gen_ai.tool.name"], "github.create_issue");
  assert.equal(mcp["gen_ai.tool.type"], "mcp");
});

test("documented span names classify spans that declare no type", () => {
  const chat = run(cavemanSpanProcessor(), makeSpan("chat gpt-4o-mini"));
  assert.equal(chat["gen_ai.operation.name"], "chat");
  assert.equal(chat["gen_ai.request.model"], "gpt-4o-mini");

  const tool = run(cavemanSpanProcessor(), makeSpan("execute_tool lookup"));
  assert.equal(tool["gen_ai.operation.name"], "execute_tool");
  assert.equal(tool["gen_ai.tool.name"], "lookup");

  const agent = run(cavemanSpanProcessor(), makeSpan("invoke_agent support-agent"));
  assert.equal(agent["gen_ai.operation.name"], "invoke_agent");

  const workflow = run(cavemanSpanProcessor(), makeSpan("invoke_workflow triage"));
  assert.equal(workflow["gen_ai.operation.name"], "invoke_workflow");
});

test("existing gen_ai attributes are never overwritten", () => {
  const processor = cavemanSpanProcessor({ projectId: "project_123" });
  const attrs = run(
    processor,
    makeSpan("chat gpt-4o-mini", {
      "gen_ai.operation.name": "chat",
      "gen_ai.request.model": "claude-sonnet-4",
      "gen_ai.usage.input_tokens": 1200,
    }),
  );
  assert.equal(attrs["gen_ai.request.model"], "claude-sonnet-4");
  assert.equal(attrs["gen_ai.usage.input_tokens"], 1200);
  assert.equal(attrs["caveman.project.id"], "project_123");
  const diagnostics = processor.diagnostics();
  assert.equal(diagnostics.alreadyInstrumentedSpans, 1);
  assert.equal(diagnostics.mappedSpans, 0);
});

test("unknown span shapes pass through completely unmodified and are counted", () => {
  const processor = cavemanSpanProcessor({ projectId: "project_123", agentId: "support-agent" });
  const span = makeSpan("db.query", { "db.system": "postgresql" });
  const attrs = run(processor, span);
  assert.deepEqual(attrs, { "db.system": "postgresql" });
  assert.equal(span.name, "db.query");

  processor.onStart(span);
  processor.onEnd(makeSpan("http.request"));
  const diagnostics = processor.diagnostics();
  assert.equal(diagnostics.unmappedSpans, 2);
  assert.equal(diagnostics.totalSpans, 2);
  assert.deepEqual(diagnostics.unmappedSpanNames, ["db.query", "http.request"]);
});

test("a declared but unknown Mastra type stays unmapped instead of falling back to the name", () => {
  const processor = cavemanSpanProcessor();
  const attrs = run(processor, makeSpan("chat gpt-4o-mini", { "mastra.span.type": "SOMETHING_NEW" }));
  assert.deepEqual(attrs, { "mastra.span.type": "SOMETHING_NEW" });
  assert.equal(processor.diagnostics().unmappedSpans, 1);

  const mapped = run(
    cavemanSpanProcessor({ spanTypeMap: { SOMETHING_NEW: "tool_call" } }),
    makeSpan("execute_tool x", { "mastra.span.type": "SOMETHING_NEW" }),
  );
  assert.equal(mapped["gen_ai.operation.name"], "execute_tool");
});

test("unmapped name samples are capped and de-duplicated", () => {
  const processor = cavemanSpanProcessor({ unmappedSampleLimit: 2 });
  for (const name of ["a", "a", "b", "c"]) processor.onEnd(makeSpan(name));
  const diagnostics = processor.diagnostics();
  assert.equal(diagnostics.unmappedSpans, 4);
  assert.deepEqual(diagnostics.unmappedSpanNames, ["a", "b"]);
});

test("withTask stamps task and session onto classified spans only", () => {
  const processor = cavemanSpanProcessor({ environment: "production" });
  const [mapped, unmapped] = withTask("ticket_991", "session_552", () => {
    const a = makeSpan("invoke_agent support-agent");
    const b = makeSpan("db.query");
    run(processor, a);
    run(processor, b);
    return [a.attributes, b.attributes];
  });
  assert.equal(mapped["caveman.task.id"], "ticket_991");
  assert.equal(mapped["caveman.session.id"], "session_552");
  assert.equal(mapped["caveman.environment"], "production");
  assert.deepEqual(unmapped, {});
});

test("withTask supports the session-less form and requires a callback", () => {
  const processor = cavemanSpanProcessor();
  const attrs = withTask("ticket_1", () => run(processor, makeSpan("invoke_agent a")));
  assert.equal(attrs["caveman.task.id"], "ticket_1");
  assert.equal("caveman.session.id" in attrs, false);
  assert.throws(() => withTask("", () => 1), /taskId is required/);
  assert.throws(() => withTask("t"), /callback is required/);
});

test("an explicit resolver overrides the async context", () => {
  const processor = cavemanSpanProcessor({
    resolveTask: (span) => (span.name.includes("support") ? { taskId: "from-resolver" } : undefined),
  });
  const attrs = withTask("ignored", () => run(processor, makeSpan("invoke_agent support-agent")));
  assert.equal(attrs["caveman.task.id"], "from-resolver");
});

test("usage aliases are copied only when configured and only into empty slots", () => {
  const processor = cavemanSpanProcessor({
    usageAttributes: {
      inputTokens: "mastra.usage.promptTokens",
      outputTokens: "mastra.usage.completionTokens",
      model: "mastra.model",
      provider: "mastra.provider",
    },
  });
  const attrs = run(
    processor,
    makeSpan("some chat", {
      "mastra.span.type": "MODEL_GENERATION",
      "mastra.usage.promptTokens": 120,
      "mastra.usage.completionTokens": 8,
      "mastra.model": "gpt-4o-mini",
      "mastra.provider": "openai",
    }),
  );
  assert.equal(attrs["gen_ai.usage.input_tokens"], 120);
  assert.equal(attrs["gen_ai.usage.output_tokens"], 8);
  assert.equal(attrs["gen_ai.request.model"], "gpt-4o-mini");
  assert.equal(attrs["gen_ai.provider.name"], "openai");

  const bare = run(cavemanSpanProcessor(), makeSpan("x", { "mastra.span.type": "MODEL_GENERATION", "mastra.model": "m" }));
  assert.equal("gen_ai.request.model" in bare, false);
});

test("span and trace ids and the span name are never touched", () => {
  const processor = cavemanSpanProcessor({ agentId: "support-agent" });
  const span = makeSpan("chat gpt-4o-mini");
  const before = span.spanContext();
  run(processor, span);
  assert.deepEqual(span.spanContext(), before);
  assert.equal(span.name, "chat gpt-4o-mini");
});

test("each span is counted exactly once across onStart/onEnd", () => {
  const processor = cavemanSpanProcessor();
  const span = makeSpan("chat gpt-4o-mini");
  processor.onStart(span);
  processor.onEnd(span);
  processor.onEnd(span);
  const diagnostics = processor.diagnostics();
  assert.equal(diagnostics.totalSpans, 1);
  assert.equal(diagnostics.mappedSpans, 1);
});

test("spans without setAttribute are mutated in place, frozen ones are skipped silently", () => {
  const processor = cavemanSpanProcessor();
  const plain = { name: "execute_tool lookup", attributes: {} };
  processor.onEnd(plain);
  assert.equal(plain.attributes["gen_ai.operation.name"], "execute_tool");

  const frozen = { name: "execute_tool lookup", attributes: Object.freeze({}) };
  processor.onEnd(frozen);
  assert.deepEqual(frozen.attributes, {});
  assert.equal(processor.diagnostics().totalSpans, 2);
});

test("diagnostics snapshots do not alias internal state", () => {
  const processor = cavemanSpanProcessor();
  processor.onEnd(makeSpan("db.query"));
  const first = processor.diagnostics();
  first.unmappedSpanNames.push("tampered");
  assert.deepEqual(processor.diagnostics().unmappedSpanNames, ["db.query"]);
});

test("shutdown and forceFlush resolve", async () => {
  const processor = cavemanSpanProcessor();
  await processor.shutdown();
  await processor.forceFlush();
});
