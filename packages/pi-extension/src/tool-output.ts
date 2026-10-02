// Post-tool output shrinking via the native runtime's PostToolUse decision.
// A replacement is published only after the session's recovery client proves
// its advertised handle still resolves to these exact original bytes.

import type { ToolResultEvent } from "@earendil-works/pi-coding-agent";
import type { HookBridge } from "./lifecycle.ts";
import { MAX_OUTPUT_REPLACEMENT_BYTES, MAX_TOOL_OUTPUT_BYTES, normalizeRecoveryHandle, outputReplacementOf } from "./protocol.ts";
import type { RecoveryClient } from "./recovery.ts";

// Partial-patch result shape for tool_result handlers (ToolResultEventResult is
// not re-exported from the package root; omitted fields keep current values).
type ToolResultPatch = { content: ToolResultEvent["content"] };

export async function shrinkToolResult(
  bridge: HookBridge,
  sessionId: string,
  event: ToolResultEvent,
  recovery: Pick<RecoveryClient, "verify">,
): Promise<ToolResultPatch | undefined> {
  const text = event.content
    .map((block) => (block.type === "text" && typeof block.text === "string" ? block.text : ""))
    .join("");
  if (!text) return undefined;
  // Over-cap output is skipped, not truncated: a partial payload could produce
  // a replacement whose recovery handle does not cover the elided bytes.
  if (Buffer.byteLength(text, "utf8") > MAX_TOOL_OUTPUT_BYTES) return undefined;
  let replacement: string;
  try {
    const response = await bridge.call(event.isError ? "PostToolUseFailure" : "PostToolUse", {
      session_id: sessionId,
      tool_name: event.toolName,
      tool_input: event.input,
      tool_output: text,
    });
    const proposed = outputReplacementOf(response);
    if (!proposed || Buffer.byteLength(proposed, "utf8") > MAX_OUTPUT_REPLACEMENT_BYTES) return undefined;
    let handle = normalizeRecoveryHandle(response?.recovery_ref);
    if (response?.recovery_ref !== undefined && !handle) return undefined;
    let advertised = false;
    // Consume the whole reference token, including bad suffixes/delimiters.
    // Repeated mentions are fine; a different or malformed handle is not.
    for (const match of proposed.matchAll(/(?:<<)?ccr(?::|_)[^\s"'`]*/g)) {
      if (match.index > 0 && /[A-Za-z0-9_<:/]/.test(proposed[match.index - 1]!)) return undefined;
      const candidate = normalizeRecoveryHandle(match[0]);
      if (!candidate || (handle && candidate !== handle)) return undefined;
      handle = candidate;
      advertised = true;
    }
    if (!advertised || !handle || !(await recovery.verify(handle, text))) return undefined;
    replacement = proposed;
  } catch {
    return undefined;
  }
  // Replace only the model-facing text; non-text blocks (images) were never
  // sent to the runtime, are not covered by the recovery handle, and must
  // survive the replacement byte-for-byte. Details keep the renderer's shape.
  return { content: [{ type: "text", text: replacement }, ...event.content.filter((block) => block.type !== "text")] };
}
