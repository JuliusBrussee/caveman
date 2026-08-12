import { test } from "node:test";
import assert from "node:assert/strict";
import { runCli, runCliWithApi } from "./_cli.mjs";

function rpc(id, method, params) {
  return JSON.stringify({
    jsonrpc: "2.0",
    id,
    method,
    ...(params === undefined ? {} : { params }),
  });
}

function notification(method, params) {
  return JSON.stringify({
    jsonrpc: "2.0",
    method,
    ...(params === undefined ? {} : { params }),
  });
}

function mcpInput(...requests) {
  return [
    rpc(900, "initialize", {
      protocolVersion: "2025-11-25",
      capabilities: {},
      clientInfo: { name: "caveman-test", version: "1.0.0" },
    }),
    notification("notifications/initialized"),
    ...requests,
  ].join("\n") + "\n";
}

function responses(stdout) {
  return stdout.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
}

test("cloud MCP lists structured tools with risk annotations without network access", async () => {
  const input = [
    rpc(1, "initialize", { protocolVersion: "2025-06-18" }),
    notification("notifications/initialized"),
    rpc(2, "tools/list"),
  ].join("\n") + "\n";
  const out = await runCli(["cloud", "mcp-serve"], { input });
  assert.equal(out.code, 0, out.stderr);
  const values = responses(out.stdout);
  assert.equal(values[0].result.protocolVersion, "2025-06-18");
  const tools = values[1].result.tools;
  assert.deepEqual(
    tools.map((tool) => tool.name),
    [
      "caveman_context",
      "caveman_report",
      "caveman_plan",
      "caveman_trace_search",
      "caveman_trace_get",
      "caveman_experiment_get",
    ],
  );
  for (const tool of tools) {
    assert.equal(tool.inputSchema.type, "object");
    assert.equal(tool.outputSchema.type, "object");
    assert.equal(typeof tool.annotations.readOnlyHint, "boolean");
    assert.equal(typeof tool.annotations.destructiveHint, "boolean");
  }
  assert.equal(tools.every((tool) => tool.annotations.readOnlyHint), true);
  assert.equal(tools.every((tool) => !tool.annotations.destructiveHint), true);
  const traceSearch = tools.find((tool) => tool.name === "caveman_trace_search");
  assert.equal(traceSearch.inputSchema.properties.filters.additionalProperties, false);
  assert.equal(traceSearch.inputSchema.properties.filters.properties.min_total_tokens.type, "number");
});

test("cloud MCP negotiates latest supported version and blocks operation before initialized", async () => {
  const input = [
    rpc(1, "initialize", { protocolVersion: "future-version" }),
    rpc(2, "tools/list"),
  ].join("\n") + "\n";
  const out = await runCli(["cloud", "mcp-serve"], { input });
  assert.equal(out.code, 0, out.stderr);
  const values = responses(out.stdout);
  assert.equal(values[0].result.protocolVersion, "2025-11-25");
  assert.equal(values[1].error.code, -32002);
  assert.equal(values[1].error.message, "Server not initialized");
});

test("cloud MCP context uses existing CLI credentials and returns structured content", async () => {
  const input = mcpInput(rpc(1, "tools/call", { name: "caveman_context", arguments: {} }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], {
    input,
    respond(req) {
      if (req.url === "/api/v1/auth/me") return { body: { id: "user-1" } };
      if (req.url === "/api/v1/projects") return { body: { data: [{ id: "proj-alias" }] } };
      return { status: 404, body: { error: { code: "cave_not_found", message: "not found" } } };
    },
  });
  assert.equal(out.code, 0, out.stderr);
  const result = responses(out.stdout).at(-1).result;
  assert.equal(result.isError, undefined);
  assert.equal(result.structuredContent.selected_project_id, "proj-alias");
  assert.equal(result.structuredContent.identity.id, "user-1");
  assert.deepEqual(out.requests.map((request) => request.path).sort(), ["/api/v1/auth/me", "/api/v1/projects"]);
});

test("cloud MCP rejects trace filter shapes before HTTP", async () => {
  const input = mcpInput(rpc(1, "tools/call", {
    name: "caveman_trace_search",
    arguments: { filters: { workflow: "support-reply" } },
  }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], { input });
  assert.equal(out.code, 0, out.stderr);
  const result = responses(out.stdout).at(-1).result;
  assert.equal(result.isError, true);
  assert.match(result.structuredContent.message, /array/);
  assert.equal(out.requests.length, 0);
});

test("cloud MCP does not expose experiment mutation tools", async () => {
  const input = mcpInput(rpc(1, "tools/call", {
    name: "caveman_experiment_change",
    arguments: {
      action: "approve",
      experiment_id: "exp-1",
      confirmation: "approve:some-other-id",
    },
  }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], { input });
  assert.equal(out.code, 0, out.stderr);
  const response = responses(out.stdout).at(-1);
  assert.equal(response.error.code, -32602);
  assert.match(response.error.message, /Unknown tool/);
  assert.equal(out.requests.length, 0);
});

test("cloud MCP surfaces control-api errors as tool errors", async () => {
  const input = mcpInput(rpc(1, "tools/call", {
    name: "caveman_trace_get",
    arguments: { trace_id: "0123456789abcdef" },
  }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], {
    input,
    respond: () => ({
      status: 404,
      body: { error: { code: "cave_trace_not_found", message: "Trace not found." } },
    }),
  });
  assert.equal(out.code, 0, out.stderr);
  const result = responses(out.stdout).at(-1).result;
  assert.equal(result.isError, true);
  assert.equal(result.structuredContent.error, "cave_trace_not_found");
  assert.equal(result.structuredContent.status, 404);
});

test("cloud MCP scopes trace reads and strips all non-allowlisted payload fields", async () => {
  const traceId = "0123456789abcdef";
  const input = mcpInput(rpc(1, "tools/call", {
    name: "caveman_trace_get",
    arguments: { trace_id: traceId },
  }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], {
    input,
    respond(req) {
      if (req.url.includes("/spans")) {
        return {
          body: {
            spans: [{
              span_id: "span-1",
              span_name: "llm.call",
              duration_ms: 12,
              attributes: { "gen_ai.prompt": "secret prompt" },
              events_json: JSON.stringify([{ body: "secret completion" }]),
              future_payload: "secret tool result",
              efficiency: { session_id: "could-link-user" },
            }],
            timeline: [{
              span_id: "span-1",
              name: "llm.call",
              start_ms: 0,
              duration_ms: 12,
              future_payload: "secret",
            }],
            future_payload: "secret",
          },
        };
      }
      return {
        body: {
          trace_id: traceId,
          project_id: "proj-alias",
          model: "model-1",
          tags: { prompt: "secret prompt" },
          capture_request_handle: "secret-handle",
          future_payload: "secret",
        },
      };
    },
  });
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(
    out.requests.map((request) => request.path).sort(),
    [
      `/api/v1/traces/${traceId}/spans?project_id=proj-alias`,
      `/api/v1/traces/${traceId}?project_id=proj-alias`,
    ].sort(),
  );
  const result = responses(out.stdout).at(-1).result.structuredContent;
  assert.deepEqual(result.trace, {
    trace_id: traceId,
    project_id: "proj-alias",
    model: "model-1",
  });
  assert.deepEqual(result.spans, {
    spans: [{ span_id: "span-1", span_name: "llm.call", duration_ms: 12 }],
    timeline: [{ span_id: "span-1", name: "llm.call", start_ms: 0, duration_ms: 12 }],
  });
  assert.doesNotMatch(JSON.stringify(result), /secret|attributes|events_json|future_payload|efficiency/);
});

test("cloud MCP missing login is explicit structured failure", async () => {
  const input = mcpInput(rpc(1, "tools/call", { name: "caveman_plan", arguments: {} }));
  const out = await runCli(["cloud", "mcp-serve"], { input });
  assert.equal(out.code, 0, out.stderr);
  const result = responses(out.stdout).at(-1).result;
  assert.equal(result.isError, true);
  assert.equal(result.structuredContent.error, "cave_auth_required");
  assert.match(result.structuredContent.message, /caveman login/);
});

test("cloud MCP telemetry carries only closed tool outcome fields", async () => {
  const input = mcpInput(rpc(1, "tools/call", { name: "caveman_plan", arguments: {} }));
  const out = await runCliWithApi(["cloud", "mcp-serve"], {
    input,
    telemetry: true,
    respond(req) {
      if (req.url === "/api/v1/projects/proj-alias/cave-plan") {
        return { body: { headline: { basis: "inferred" }, moves: [] } };
      }
      if (req.url === "/telemetry/cli") return { status: 202, body: {} };
      return { status: 404, body: {} };
    },
  });
  assert.equal(out.code, 0, out.stderr);
  const telemetry = out.requests
    .filter((request) => request.path === "/telemetry/cli")
    .flatMap((request) => JSON.parse(request.body));
  const toolEvent = telemetry.find((event) => event.event === "agent_tool_call");
  assert.equal(toolEvent.command, "mcp");
  assert.equal(toolEvent.subcommand, "caveman_plan");
  assert.equal(toolEvent.exit_class, "ok");
  assert.deepEqual(
    Object.keys(toolEvent).sort(),
    [
      "anonymous_id",
      "arch",
      "cli_version",
      "command",
      "duration_ms",
      "error_class",
      "event",
      "exit_class",
      "node_major",
      "os",
      "schema",
      "subcommand",
      "ts",
    ],
  );
  assert.doesNotMatch(JSON.stringify(telemetry), /proj-alias|headline|moves/);
});
