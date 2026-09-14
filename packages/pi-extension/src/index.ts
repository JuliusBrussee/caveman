// Pi entry. Registration is synchronous; background work waits for session_start.
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { createCavemanRuntime } from "./runtime.ts";

export default function (pi: ExtensionAPI) {
  const runtime = createCavemanRuntime<NonNullable<ExtensionContext["model"]>>(pi);
  pi.registerTool({
    name: "caveman_retrieve",
    label: "Retrieve compressed context",
    description: "Recover exact original content from a Caveman recovery handle.",
    parameters: Type.Object({
      recovery_handle: Type.String({ description: "Exact ccr_ handle returned by Caveman or copied from a <<ccr:HANDLE>> marker." }),
      query: Type.Optional(Type.String({ description: "One broad description covering every detail needed from this handle." })),
    }),
    execute: runtime.retrieve,
  });
  pi.on("session_start", (_event, ctx) => runtime.start(ctx));
  pi.on("session_shutdown", () => runtime.shutdown());
  pi.on("model_select", (event, ctx) => runtime.modelSelect(event.model, ctx));
  pi.on("before_agent_start", async (event, ctx) => {
    const context = await runtime.beforeAgentStart(event.prompt, ctx);
    if (context) return { systemPrompt: [event.systemPrompt, ...context].filter(Boolean).join("\n\n") };
  });
  pi.on("turn_start", () => runtime.turnStart());
  pi.on("turn_end", () => runtime.turnEnd());
  pi.on("tool_call", (event) => runtime.toolCall(event));
  pi.on("tool_result", (event) => runtime.toolResult(event));
  pi.on("session_before_compact", () => runtime.beforeCompact());
  pi.on("session_compact", () => runtime.compact());
}
