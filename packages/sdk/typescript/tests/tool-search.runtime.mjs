/**
 * Runtime tests for the TS SDK tool-search surface.
 * Run with: node --test packages/sdk-ts/tests/tool-search.runtime.mjs
 * (Node 22, uses built-in node:test runner with global fetch mocking.)
 */
import { test, before, after } from "node:test";
import assert from "node:assert/strict";

// Import compiled dist (must run `pnpm --filter @caveman-ai/sdk build` first)
import { Cave } from "../dist/index.js";

// ─── Fetch mock helpers ──────────────────────────────────────────────────────

let capturedRequests = [];
let nextResponse = {};

const originalFetch = globalThis.fetch;

function mockFetch(responseData) {
  nextResponse = responseData;
  capturedRequests = [];
  globalThis.fetch = async (url, init) => {
    capturedRequests.push({ url: String(url), method: init?.method, headers: init?.headers ?? {}, body: init?.body ? JSON.parse(init.body) : undefined });
    return {
      ok: true,
      json: async () => nextResponse,
      text: async () => JSON.stringify(nextResponse)
    };
  };
}

function restoreFetch() {
  globalThis.fetch = originalFetch;
}

// ─── Tests ───────────────────────────────────────────────────────────────────

const CATALOG = [
  { name: "search_customers", description: "Search customers by name or email", inputSchema: { type: "object", properties: { query: { type: "string" } } }, readOnly: true, idempotent: true, alwaysLoad: false, handler: () => {} },
  { name: "fetch_order",      description: "Fetch order details by order ID",  inputSchema: { type: "object", properties: { id: { type: "string" } } },    readOnly: true, idempotent: true, alwaysLoad: false, handler: () => {} },
  ...Array.from({ length: 8 }, (_, i) => ({
    name: `tool_${i}`, description: `Utility tool ${i}`,
    inputSchema: { type: "object", properties: {} }, readOnly: false, idempotent: false, alwaysLoad: false, handler: () => {}
  }))
];

const MOCK_RESPONSE = {
  session_id: "tool-session-1",
  tools: [{ name: "search_customers", description: "Search customers by name or email" }],
  sent_schema_tokens: 120,
  full_schema_tokens: 840,
  deferred_count: 9,
  method: "lexical-hit-rate",
  token_basis: "estimated_bytes_div_4"
};

test("tools().search() posts correct body and returns ToolSearchResult", async () => {
  mockFetch(MOCK_RESPONSE);
  try {
    const cave = new Cave({ apiKey: "cave_live_test_key", baseURL: "http://localhost:8787", agent: "test-agent" });
    const handle = cave.tools({ catalog: CATALOG, strategy: "deferred" });
    const result = await handle.search("find customer", { context: "support workflow", maxTools: 5, toolSessionId: "tool-session-1" });

    assert.equal(capturedRequests.length, 1, "exactly one fetch call");
    const req = capturedRequests[0];

    // URL
    assert.equal(req.url, "http://localhost:8787/sdk/v1/tool-search");
    assert.equal(req.method, "POST");

    // Headers
    assert.equal(req.headers["authorization"], "Bearer cave_live_test_key");
    assert.equal(req.headers["x-cave-agent"], "test-agent");
    assert.equal(req.headers["content-type"], "application/json");

    // Body shape matches gateway contract
    assert.equal(req.body.query, "find customer");
    assert.equal(req.body.context, "support workflow");
    assert.equal(req.body.max_tools, 5);
    assert.equal(req.body.session_id, "tool-session-1");
    assert.equal(req.body.tools.length, 10);

    const firstTool = req.body.tools[0];
    assert.equal(firstTool.name, "search_customers");
    assert.ok("input_schema" in firstTool, "input_schema key present");
    assert.ok("read_only" in firstTool, "read_only key present");
    assert.equal(firstTool.idempotent, true, "idempotent safety metadata preserved");
    assert.ok("always_load" in firstTool, "always_load key present");

    // Parsed result
    assert.equal(result.tools.length, 1);
    assert.equal(result.tools[0].name, "search_customers");
    assert.equal(result.tools[0], CATALOG[0], "selected descriptor must rehydrate local handler and metadata");
    assert.doesNotThrow(() => result.tools[0].handler());
    assert.equal(result.sentSchemaTokens, 120);
    assert.equal(result.fullSchemaTokens, 840);
    assert.equal(result.deferredCount, 9);
    assert.equal(result.method, "lexical-hit-rate");
    assert.equal(result.sessionId, "tool-session-1");
    assert.equal(result.tokenBasis, "estimated_bytes_div_4");
    assert.equal(result.basis, "inferred");

    // Derived helpers
    assert.equal(result.savedTokens, 720);
    assert.equal(result.reductionPct, 85.7);
  } finally {
    restoreFetch();
  }
});

test("tool search rejects unknown or duplicate wire names and ambiguous local catalogs", async () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  try {
    mockFetch({ ...MOCK_RESPONSE, tools: [{ name: "server_injected" }] });
    await assert.rejects(cave.toolSearch(CATALOG, "query"), /unknown or duplicate tool/);
    mockFetch({ ...MOCK_RESPONSE, tools: [{ name: "search_customers" }, { name: "search_customers" }] });
    await assert.rejects(cave.toolSearch(CATALOG, "query"), /unknown or duplicate tool/);
    mockFetch(MOCK_RESPONSE);
    await assert.rejects(cave.toolSearch([CATALOG[0], { ...CATALOG[0] }], "query"), /non-empty and unique/);
  } finally {
    restoreFetch();
  }
});

test("tools() applies maxLoadedTools by default and lets each search override it", async () => {
  mockFetch(MOCK_RESPONSE);
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const handle = cave.tools({ catalog: CATALOG, strategy: "deferred", maxLoadedTools: 2 });
    await handle.search("first");
    assert.equal(capturedRequests[0].body.max_tools, 2);
    await handle.search("second", { maxTools: 1 });
    assert.equal(capturedRequests[1].body.max_tools, 1);
    assert.throws(() => cave.tools({ catalog: CATALOG, maxLoadedTools: 0 }), /positive integer/);
  } finally {
    restoreFetch();
  }
});

test("malformed or impossible counters fail closed to zero savings", async () => {
  mockFetch({
    tools: [],
    sent_schema_tokens: 200,
    full_schema_tokens: 100,
    deferred_count: -4,
    method: "lexical-hit-rate",
    token_basis: "estimated_bytes_div_4"
  });
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const result = await cave.toolSearch([], "anything");

    assert.equal(result.sentSchemaTokens, 0);
    assert.equal(result.fullSchemaTokens, 0);
    assert.equal(result.savedTokens, 0);
    assert.equal(result.reductionPct, 0);
    assert.equal(result.deferredCount, 0);
    assert.equal(result.basis, "inferred");
  } finally {
    restoreFetch();
  }
});

test("cave.toolSearch() direct method works identically", async () => {
  mockFetch(MOCK_RESPONSE);
  try {
    const cave = new Cave({ apiKey: "cave_live_test_key", baseURL: "http://localhost:8787", agent: "test-agent" });
    const result = await cave.toolSearch(CATALOG, "find customer");

    assert.equal(capturedRequests.length, 1);
    assert.equal(capturedRequests[0].url, "http://localhost:8787/sdk/v1/tool-search");
    assert.equal(result.sentSchemaTokens, 120);
    assert.equal(result.fullSchemaTokens, 840);
  } finally {
    restoreFetch();
  }
});

test("search() without optional args omits context and max_tools from body", async () => {
  mockFetch({ tools: [], sent_schema_tokens: 0, full_schema_tokens: 100, deferred_count: 3, method: "lexical-hit-rate" });
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const result = await cave.toolSearch([], "anything");

    const body = capturedRequests[0].body;
    assert.ok(!("context" in body), "context must be absent");
    assert.ok(!("max_tools" in body), "max_tools must be absent");
    assert.ok(!("session_id" in body), "session_id must be absent");
    assert.equal(result.reductionPct, 100);
  } finally {
    restoreFetch();
  }
});

test("search() sends custom workflow in x-cave-workflow header", async () => {
  mockFetch({ tools: [], sent_schema_tokens: 50, full_schema_tokens: 200, deferred_count: 2, method: "lexical-hit-rate" });
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a", defaultWorkflow: "default-wf" });
    await cave.toolSearch([], "test", { workflow: "custom-workflow" });

    assert.equal(capturedRequests[0].headers["x-cave-workflow"], "custom-workflow");
  } finally {
    restoreFetch();
  }
});

test("provider requests can carry tool-session header", async () => {
  mockFetch({ ok: true });
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    await cave.openai().chat.completions.create({ model: "gpt-5.5", messages: [] }, { cave: { toolSessionId: "tool-session-1" } });

    assert.equal(capturedRequests[0].url, "http://localhost:8787/openai/v1/chat/completions");
    assert.equal(capturedRequests[0].headers["x-cave-tool-session"], "tool-session-1");
  } finally {
    restoreFetch();
  }
});

test("tools() initial still returns alwaysLoad tools for deferred strategy", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const catalog = [
    { name: "always", description: "always loaded", inputSchema: {}, readOnly: true, idempotent: true, alwaysLoad: true, handler: () => {} },
    { name: "lazy",   description: "lazy loaded",   inputSchema: {}, readOnly: true, idempotent: true, alwaysLoad: false, handler: () => {} }
  ];
  const handle = cave.tools({ catalog, strategy: "deferred" });
  assert.equal(handle.strategy, "deferred");
  assert.ok(handle.initial.some((t) => t.name === "always"), "alwaysLoad tool in initial");
  assert.equal(handle.initial.filter((t) => t.name === "always").length, 1, "alwaysLoad tool is not duplicated");
});

test("tools() validates deferred counts and never hides mandatory tools", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const catalog = [
    { name: "always", description: "mandatory", inputSchema: {}, readOnly: true, idempotent: true, alwaysLoad: true, handler: () => {} },
    { name: "lazy1", description: "lazy", inputSchema: {}, readOnly: true, idempotent: true, alwaysLoad: false, handler: () => {} },
    { name: "lazy2", description: "lazy", inputSchema: {}, readOnly: true, idempotent: true, alwaysLoad: false, handler: () => {} }
  ];
  const bounded = cave.tools({ catalog, strategy: "deferred", initialToolCount: 8, maxLoadedTools: 2 });
  assert.deepEqual(bounded.initial.map((tool) => tool.name), ["always", "lazy1"]);
  assert.throws(() => cave.tools({ catalog, strategy: /** @type {any} */ ("unknown") }), /strategy/);
  assert.throws(() => cave.tools({ catalog, strategy: "deferred", initialToolCount: -1 }), /non-negative integer/);
  assert.throws(() => cave.tools({ catalog, strategy: "deferred", maxLoadedTools: 0 }), /positive integer/);
  assert.throws(() => cave.tools({ catalog: [catalog[0], { ...catalog[0], name: "always2" }], strategy: "deferred", maxLoadedTools: 1 }), /alwaysLoad tool count/);
});
