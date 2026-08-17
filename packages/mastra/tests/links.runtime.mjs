/**
 * Deep-link tests. No network.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { MASTRA_STUDIO_DEFAULT_BASE_URL, cavemanTraceLink, mastraStudioLink } from "../dist/index.js";

test("caveman trace link points at the dashboard trace page", () => {
  assert.equal(
    cavemanTraceLink("https://app.caveman.so/", "project_123", "0af7651916cd43dd8448eb211c80319c"),
    "https://app.caveman.so/traces/0af7651916cd43dd8448eb211c80319c?project_id=project_123",
  );
});

test("caveman trace link fails closed on missing parts", () => {
  assert.throws(() => cavemanTraceLink("", "p", "t"), /baseUrl is required/);
  assert.throws(() => cavemanTraceLink("app.caveman.so", "p", "t"), /absolute http\(s\) URL/);
  assert.throws(() => cavemanTraceLink("https://app.caveman.so", "", "t"), /projectId is required/);
  assert.throws(() => cavemanTraceLink("https://app.caveman.so", "p", ""), /traceId is required/);
});

test("studio link substitutes the caller template", () => {
  assert.equal(
    mastraStudioLink("abc123", { template: "{baseUrl}/observability?traceId={traceId}" }),
    `${MASTRA_STUDIO_DEFAULT_BASE_URL}/observability?traceId=abc123`,
  );
  assert.equal(
    mastraStudioLink("abc 123", { template: "{baseUrl}/t/{traceId}", baseUrl: "https://studio.example.com/" }),
    "https://studio.example.com/t/abc%20123",
  );
});

test("studio link requires a template containing {traceId} — no URL shape is guessed", () => {
  assert.throws(() => mastraStudioLink("abc", { template: "" }), /template is required/);
  assert.throws(() => mastraStudioLink("abc", { template: "{baseUrl}/observability" }), /\{traceId\} placeholder/);
  assert.throws(() => mastraStudioLink("", { template: "{traceId}" }), /traceId is required/);
});
