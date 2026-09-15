// Post-tool output shrinking via the native runtime's PostToolUse decision.
// The runtime decides; this side only applies a valid replacement and carries
// the recovery handle embedded in it. Anything abnormal keeps the original.

import type { ToolResultEvent } from "@earendil-works/pi-coding-agent";
import type { HookBridge } from "./lifecycle.ts";
import { MAX_TOOL_OUTPUT_BYTES, outputReplacementOf } from "./protocol.ts";

// Partial-patch result shape for tool_result handlers (ToolResultEventResult is
// not re-exported from the package root; omitted fields keep current values).
type ToolResultPatch = { content: ToolResultEvent["content"] };

export async function shrinkToolResult(
  bridge: HookBridge,
  sessionId: string,
  event: ToolResultEvent,
): Promise<ToolResultPatch | undefined> {
  const text = event.content
    .map((block) => (block.type === "text" && typeof block.text === "string" ? block.text : ""))
    .join("");
  if (!text) return undefined;
  // Over-cap output is skipped, not truncated: a partial payload could produce
  // a replacement whose recovery handle does not cover the elided bytes.
  if (Buffer.byteLength(text, "utf8") > MAX_TOOL_OUTPUT_BYTES) return undefined;
  const response = await bridge.call(event.isError ? "PostToolUseFailure" : "PostToolUse", {
    session_id: sessionId,
    tool_name: event.toolName,
    tool_input: event.input,
    tool_output: text,
  });
  const replacement = outputReplacementOf(response);
  if (!replacement) return undefined;
  // Replace only the model-facing text; non-text blocks (images) were never
  // sent to the runtime, are not covered by the recovery handle, and must
  // survive the replacement byte-for-byte. Details keep the renderer's shape.
  return { content: [{ type: "text", text: replacement }, ...event.content.filter((block) => block.type !== "text")] };
}
