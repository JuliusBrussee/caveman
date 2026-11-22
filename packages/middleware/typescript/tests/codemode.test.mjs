import { test } from "node:test";
import assert from "node:assert/strict";
import { createCodeModeToolset } from "../dist/codemode.js";

test("createCodeModeToolset collapses 40 tools into 3 meta-tools with BM25 search, pipeline, and read/write split", async () => {
  const tools = [
    {
      name: "list_tickets",
      server: "linear",
      description: "List open engineering tickets with metadata.",
      annotations: { readOnlyHint: true },
      inputSchema: { type: "object", properties: { state: { type: "string", enum: ["open", "closed"] } } },
      execute: async () => ({
        tickets: [
          { id: 1, title: "Fix cache", priority: "urgent", padding: "x".repeat(500) },
          { id: 2, title: "Docs", priority: "low", padding: "y".repeat(500) },
        ],
      }),
    },
    {
      name: "delete_ticket",
      server: "linear",
      description: "Permanently delete a ticket.",
      annotations: { readOnlyHint: false, destructiveHint: true },
      inputSchema: { type: "object", properties: { id: { type: "integer" } }, required: ["id"] },
      execute: async (args) => ({ deleted: args.id }),
    },
    ...Array.from({ length: 38 }, (_, i) => ({
      name: `surface_tool_${String(i).padStart(2, "0")}`,
      server: "ops",
      description: `Workspace operational tool #${i} with pagination and diagnostics.`,
      inputSchema: { type: "object", properties: { cursor: { type: "string" } } },
      execute: async () => ({ ok: true }),
    })),
  ];

  const cm = createCodeModeToolset(tools);
  assert.equal(cm.catalogSize, 40);
  assert.equal(cm.tools.length, 3);
  assert.equal(cm.toAnthropicTools().length, 3);
  assert.equal(cm.toOpenAIChatTools().length, 3);
  assert.equal(cm.toOpenAIResponsesTools().length, 3);
  assert.equal(cm.toGeminiTools()[0].functionDeclarations.length, 3);

  // Plural stemming: singular "ticket" finds both list_tickets (read) and delete_ticket (write).
  const found = cm.search("ticket");
  assert.equal(found.total_tools, 40);
  assert.ok(found.matches.some((m) => m.tool === "linear.list_tickets" && m.access === "read"));
  assert.ok(found.matches.some((m) => m.tool === "linear.delete_ticket" && m.access === "write"));

  // Read-only pipeline executes and projects fields cleanly.
  const out = await cm.execute({
    steps: [
      { id: "all", op: "call", tool: "linear.list_tickets", args: { state: "open" } },
      { id: "urgent", op: "filter", from: "$all.tickets", where: { field: "priority", equals: "urgent" } },
      { id: "compact", op: "pick", from: "$urgent", fields: ["id", "title"] },
    ],
  });
  assert.equal(out.tool_calls, 1);
  assert.deepEqual(out.result, [{ id: 1, title: "Fix cache" }]);

  // Read-only execute blocks mutating tools.
  await assert.rejects(
    () => cm.execute({ steps: [{ id: "d", op: "call", tool: "linear.delete_ticket", args: { id: 1 } }] }),
    /call codemode_mutate instead/,
  );

  // codemode_mutate executes single-tool writes cleanly.
  const mutateTool = cm.tools.find((t) => t.name === "codemode_mutate");
  const mutRaw = await mutateTool.execute({ tool: "linear.delete_ticket", args: { id: 1 } });
  const mutParsed = JSON.parse(mutRaw);
  assert.deepEqual(mutParsed.result, { deleted: 1 });
});
