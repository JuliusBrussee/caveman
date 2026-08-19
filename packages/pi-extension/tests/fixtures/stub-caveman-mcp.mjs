#!/usr/bin/env node
// Stub caveman-mcp: `version --json` probe + line-delimited MCP JSON-RPC with a
// single known handle. STUB_MCP_DROP_CAPABILITY=1 removes mcp_recovery from the
// probe; STUB_MCP_EXIT_AFTER_INIT=1 dies right after initialize (crash test).

const KNOWN_HANDLE = "ccr_0123456789abcdef0123456789abcdef";
const KNOWN_BYTES = "exact original bytes\nline two éø bytes";

if (process.argv[2] === "version") {
  const capabilities = process.env.STUB_MCP_DROP_CAPABILITY === "1" ? ["build_stamped_version"] : ["mcp_recovery", "build_stamped_version"];
  process.stdout.write(JSON.stringify({ version: "1.0.0", schema: "caveman.mcp.version.v1", capabilities }) + "\n");
  process.exit(0);
}

let buffer = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => {
  buffer += chunk;
  let index;
  while ((index = buffer.indexOf("\n")) !== -1) {
    const line = buffer.slice(0, index).trim();
    buffer = buffer.slice(index + 1);
    if (!line) continue;
    let message;
    try { message = JSON.parse(line); } catch { continue; }
    if (message.method === "initialize") {
      reply(message.id, { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "stub", version: "1.0.0" } });
      if (process.env.STUB_MCP_EXIT_AFTER_INIT === "1") process.exit(1);
    } else if (message.method === "tools/call") {
      const args = message.params?.arguments ?? {};
      if (message.params?.name !== "caveman_retrieve") {
        reply(message.id, { content: [{ type: "text", text: JSON.stringify({ error: "cave_unknown_tool" }) }], isError: true });
      } else if (args.recovery_handle === KNOWN_HANDLE) {
        reply(message.id, { content: [{ type: "text", text: KNOWN_BYTES }], isError: false });
      } else {
        reply(message.id, { content: [{ type: "text", text: JSON.stringify({ error: "cave_unknown_handle", message: "no original found for handle" }) }], isError: true });
      }
    } else if (message.id !== undefined) {
      reply(message.id, {});
    }
  }
});

function reply(id, result) {
  process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
}
