// Native OMP entry: no Pi compatibility shim or event interception proxy.
import { z, type ExtensionAPI, type ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { createCavemanRuntime } from "./runtime.ts";

export default function (omp: ExtensionAPI) {
  const runtime = createCavemanRuntime<NonNullable<ExtensionContext["model"]>>(omp);
  omp.registerTool({
    name: "caveman_retrieve",
    label: "Retrieve compressed context",
    description: "Recover exact original content from a Caveman recovery handle.",
    parameters: z.object({
      recovery_handle: z.string().describe("Exact ccr_ handle returned by Caveman or copied from a <<ccr:HANDLE>> marker."),
      query: z.string().describe("One broad description covering every detail needed from this handle.").optional(),
    }),
    execute: runtime.retrieve,
  });
  omp.on("session_start", (_event, ctx) => runtime.start(ctx));
  omp.on("session_switch", (_event, ctx) => runtime.start(ctx));
  omp.on("session_tree", (_event, ctx) => runtime.start(ctx));
  omp.on("session_branch", (_event, ctx) => runtime.start(ctx));
  omp.on("session_shutdown", () => runtime.shutdown());
  omp.on("before_agent_start", async (event, ctx) => {
    // OMP exposes no extension model_select event. Recheck the selected model
    // at the native user-run boundary, never by replaying startup per request.
    await runtime.modelSelect(ctx.model, ctx);
    const context = await runtime.beforeAgentStart(event.prompt, ctx);
    // Preserve existing prompt blocks byte-for-byte for OMP's prefix cache.
    if (context) return { systemPrompt: [...event.systemPrompt, ...context] };
  });
  omp.on("turn_start", () => runtime.turnStart());
  omp.on("turn_end", () => runtime.turnEnd());
  omp.on("tool_call", (event) => runtime.toolCall(event));
  omp.on("tool_result", (event) => runtime.toolResult(event));
  omp.on("session_before_compact", () => runtime.beforeCompact());
  omp.on("session_compact", () => runtime.compact());
}
