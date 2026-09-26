#!/usr/bin/env node
// Stub caveman-mcp: `version --json` probe + line-delimited MCP JSON-RPC with a
// single known handle. STUB_MCP_DROP_CAPABILITY=1 removes mcp_recovery from the
// probe; STUB_MCP_EXIT_AFTER_INIT=1 dies right after initialize (crash test);
// STUB_MCP_EXIT_ONCE_FLAG=<path> dies after initialize only on the FIRST spawn,
// creating that file as the marker. The once-flag lives here rather than in a
// shell shim because the launcher is bypassed on Windows: portableInvocation
// extracts the node target from a .cmd and runs it directly, so anything the
// shim tried to set never executed.
// STUB_MCP_HANG_INIT=1 starts but never answers initialize — the locked-ccr.db
// case that used to hold Pi's session_start for the full 30s call budget.
// STUB_MCP_SPAWN_LOG=<path> appends one pid line per serve-mode spawn, so a
// test can count how many children ensure() actually created and whether
// dispose() reaped them.

import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";

const KNOWN_HANDLE = "ccr_0123456789abcdef0123456789abcdef";
const KNOWN_BYTES = "exact original bytes\nline two éø bytes";
// Read on every retrieve: the hook fixture publishes fresh originals after the
// MCP child has initialized, just as the separate native producer does.
function storedText(handle) {
  if (!process.env.STUB_MCP_STORE) return handle === KNOWN_HANDLE ? KNOWN_BYTES : undefined;
  try { return JSON.parse(readFileSync(process.env.STUB_MCP_STORE, "utf8"))[handle]; } catch { return undefined; }
}

if (process.argv[2] === "version") {
  const capabilities = process.env.STUB_MCP_DROP_CAPABILITY === "1" ? ["build_stamped_version"] : ["mcp_recovery", "build_stamped_version", ...(process.env.STUB_MCP_DROP_VERIFICATION === "1" ? [] : ["recovery_verification"])];
  process.stdout.write(JSON.stringify({ version: "1.0.0", schema: "caveman.mcp.version.v1", capabilities }) + "\n");
  process.exit(0);
}

const spawnLog = process.env.STUB_MCP_SPAWN_LOG;
if (spawnLog) appendFileSync(spawnLog, `${process.pid}\n`);

const served = new Set();
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
      if (process.env.STUB_MCP_HANG_INIT === "1") continue; // alive, silent — never answers
      reply(message.id, { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "stub", version: "1.0.0" } });
      if (process.env.STUB_MCP_EXIT_AFTER_INIT === "1") process.exit(1);
      const onceFlag = process.env.STUB_MCP_EXIT_ONCE_FLAG;
      if (onceFlag && !existsSync(onceFlag)) {
        writeFileSync(onceFlag, "");
        process.exit(1);
      }
    } else if (message.method === "tools/call") {
      const args = message.params?.arguments ?? {};
      const handle = args.recovery_handle?.replace(/^ccr:\/\//, "");
      const text = storedText(handle);
      if (message.params?.name !== "caveman_retrieve") {
        reply(message.id, { content: [{ type: "text", text: JSON.stringify({ error: "cave_unknown_tool" }) }], isError: true });
      } else if (typeof text === "string") {
        const key = `${handle}\0${args.query ?? ""}`;
        if (args.verify_only && process.env.STUB_MCP_DROP_VERIFICATION !== "1") {
          const proof = { recovery_handle: handle, byte_length: Buffer.byteLength(text), sha256: createHash("sha256").update(text).digest("hex") };
          const fault = process.env.STUB_MCP_VERIFICATION_FAULT;
          if (fault === "reference") proof.recovery_handle = "ccr_ffffffffffffffffffffffffffffffff";
          if (fault === "length") proof.byte_length++;
          if (fault === "digest") proof.sha256 = "0".repeat(64);
          if (fault === "hang") continue;
          reply(message.id, { content: [{ type: "text", text: fault === "malformed" ? "not JSON" : JSON.stringify(proof) }], isError: fault === "error" });
        } else {
          reply(message.id, { content: [{ type: "text", text: served.has(key) ? "already recovered" : text }], isError: false });
          served.add(key);
        }
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
