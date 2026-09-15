import { writeSync } from "node:fs";
import { stdin } from "node:process";
import {
  agentDefinitionSHA256,
  toolDefinitionSHA256,
} from "./build.js";
import { validateAgentGraph } from "./definition-graph.js";
import type { AgentDefinition } from "./index.js";
import { installNetworkDeny } from "./sandbox-network.js";

// The result travels on a DEDICATED fd 3, length-prefixed — never stdout
// Any console.log in the tool's own import graph writes to stdout
// (fd 1) and the parent ignores it, so it can no longer collide with the result
// JSON and corrupt it into cave_sandbox_invalid_output.
let resultWritten = false;
function writeResult(payload: { ok: boolean; value?: unknown; code?: string }): void {
  if (resultWritten) return;
  let encoded: string;
  try {
    encoded = JSON.stringify(payload);
  } catch {
    if (payload.ok) {
      writeResult({ ok: false, code: "cave_sandbox_result_not_serializable" });
      return;
    }
    throw new Error("cave_sandbox_failure_not_serializable");
  }
  // Mark terminal only after serialization succeeds. Otherwise a BigInt or
  // circular tool value suppresses the failure frame and becomes an opaque
  // cave_sandbox_invalid_output at the parent.
  resultWritten = true;
  const body = Buffer.from(encoded, "utf8");
  const header = Buffer.allocUnsafe(4);
  header.writeUInt32BE(body.byteLength, 0);
  const frame = Buffer.concat([header, body]);
  let offset = 0;
  while (offset < frame.byteLength) {
    offset += writeSync(3, frame, offset, frame.byteLength - offset);
  }
}

function failureCode(error: unknown): string {
  return error instanceof Error
    ? [
      error.message.split("\n", 1)[0],
      "resource" in error && typeof error.resource === "string" ? error.resource : "",
    ].filter(Boolean).join(":")
    : "cave_sandbox_failed";
}

// A floating rejection or throw AFTER the result was already written must not
// turn a successful tool into a failure; before it, it becomes an honest
// failure result rather than a silent nonzero exit the parent can only read as
// a redacted stderr blob.
process.on("uncaughtException", (error) => {
  if (resultWritten) return;
  writeResult({ ok: false, code: `cave_sandbox_uncaught:${failureCode(error)}` });
  process.exit(1);
});
process.on("unhandledRejection", (reason) => {
  if (resultWritten) return;
  writeResult({ ok: false, code: `cave_sandbox_unhandled_rejection:${failureCode(reason)}` });
  process.exit(1);
});

async function readRequest(): Promise<{
  entry: string;
  agentPath: string[];
  rootDefinitionSha256: string;
  toolDefinitionSha256: string;
  tool: string;
  params: unknown;
  allowSideEffects: boolean;
  allowNetwork: boolean;
}> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of stdin) {
    const buffer = Buffer.from(chunk);
    size += buffer.byteLength;
    if (size > 1_048_576) throw new Error("cave_sandbox_input_limit");
    chunks.push(buffer);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

try {
  const request = await readRequest();
  if (typeof request.entry !== "string" || !Array.isArray(request.agentPath) ||
      request.agentPath.length > 8 ||
      request.agentPath.some((item) => typeof item !== "string" ||
        !/^[a-zA-Z][a-zA-Z0-9_-]{0,127}$/.test(item)) ||
      typeof request.rootDefinitionSha256 !== "string" ||
      !/^[a-f0-9]{64}$/.test(request.rootDefinitionSha256) ||
      typeof request.toolDefinitionSha256 !== "string" ||
      !/^[a-f0-9]{64}$/.test(request.toolDefinitionSha256) ||
      typeof request.tool !== "string" ||
      !/^[a-zA-Z][a-zA-Z0-9_-]{0,127}$/.test(request.tool) ||
      typeof request.allowSideEffects !== "boolean" ||
      typeof request.allowNetwork !== "boolean") {
    throw new Error("cave_sandbox_request_invalid");
  }
  if (request.allowNetwork !== true) installNetworkDeny();
  const imported = await import(request.entry) as { default?: AgentDefinition; agent?: AgentDefinition };
  let definition = imported.default ?? imported.agent;
  if (!definition || definition.kind !== "agent") throw new Error("cave_sandbox_agent_export_missing");
  validateAgentGraph(definition);
  if (agentDefinitionSHA256(definition) !== request.rootDefinitionSha256) {
    throw new Error("cave_sandbox_definition_mismatch");
  }
  const visited = new Set<AgentDefinition>([definition]);
  for (const name of request.agentPath) {
    const delegated = definition.tools.filter((item) =>
      item.name === name && item.runtime?.kind === "subagent"
    );
    if (delegated.length !== 1) throw new Error("cave_sandbox_unknown_subagent");
    const child = delegated[0]!.runtime!.definition as AgentDefinition;
    if (!child || child.kind !== "agent") {
      throw new Error("cave_sandbox_subagent_definition_invalid");
    }
    if (visited.has(child)) throw new Error("cave_sandbox_subagent_cycle");
    visited.add(child);
    definition = child;
  }
  const selectedTools = definition.tools.filter((item) => item.name === request.tool);
  if (selectedTools.length !== 1 || selectedTools[0]!.runtime?.kind === "subagent") {
    throw new Error("cave_sandbox_unknown_tool");
  }
  const selected = selectedTools[0]!;
  if (toolDefinitionSHA256(selected) !== request.toolDefinitionSha256) {
    throw new Error("cave_sandbox_tool_definition_mismatch");
  }
  if (selected.effect !== "read" && request.allowSideEffects !== true) {
    throw new Error("cave_sandbox_side_effect_denied");
  }
  const value = await selected.execute(request.params as never, AbortSignal.timeout(selected.timeoutMs));
  writeResult({ ok: true, value });
} catch (error) {
  writeResult({ ok: false, code: failureCode(error) });
}
