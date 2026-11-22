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

test("read-only execute refuses a compound write tool whose first token is a read verb", async () => {
  // `find_and_replace` contains the read verb "find", so the read rule promoted it
  // to read-only and execute() ran a file-mutating tool with no host prompt.
  let writes = 0;
  const tools = [
    {
      name: "find_and_replace",
      server: "repo",
      description: "Replace every match of a pattern in a file.",
      inputSchema: { type: "object", properties: { path: { type: "string" }, pattern: { type: "string" } } },
      execute: async (args) => {
        writes += 1;
        return { replaced: args.pattern };
      },
    },
  ];
  for (const name of ["search_replace", "read_and_replace", "list_and_append", "get_and_overwrite", "view_and_save"]) {
    tools.push({
      name,
      server: "repo",
      description: `Compound write tool ${name}.`,
      inputSchema: { type: "object", properties: { path: { type: "string" } } },
      execute: async () => ({ ok: true }),
    });
  }
  const cm = createCodeModeToolset(tools);

  await assert.rejects(
    () => cm.execute({ steps: [{ id: "e", op: "call", tool: "repo.find_and_replace", args: { path: "a.go", pattern: "foo" } }] }),
    /call codemode_mutate instead/,
  );
  assert.equal(writes, 0, "read-only execute ran the mutating handler");

  for (const name of ["search_replace", "read_and_replace", "list_and_append", "get_and_overwrite", "view_and_save"]) {
    await assert.rejects(
      () => cm.execute({ steps: [{ id: "e", op: "call", tool: `repo.${name}`, args: { path: "a.go" } }] }),
      /call codemode_mutate instead/,
      `${name} must not run under read-only execute`,
    );
  }

  const mutateTool = cm.tools.find((t) => t.name === "codemode_mutate");
  const raw = await mutateTool.execute({ tool: "repo.find_and_replace", args: { path: "a.go", pattern: "foo" } });
  assert.deepEqual(JSON.parse(raw).result, { replaced: "foo" });
  assert.equal(writes, 1);
});
