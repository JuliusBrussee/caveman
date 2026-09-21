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
  // OMP's real BeforeAgentStartEvent/SessionEvent shapes (verified against
  // @oh-my-pi/pi-coding-agent 18.2.6's shipped .d.ts): session_start fires
  // only on initial load; session_switch/session_tree/session_branch are
  // separate, non-overlapping events for resume/fork, tree navigation, and
  // branch — none of them re-fires session_start. All four need their own
  // binding here.
  omp.on("session_start", (_event, ctx) => runtime.start(ctx));
  omp.on("session_switch", (_event, ctx) => runtime.start(ctx));
  omp.on("session_tree", (_event, ctx) => runtime.start(ctx));
  omp.on("session_branch", (_event, ctx) => runtime.start(ctx));
  omp.on("session_shutdown", () => runtime.shutdown());
  omp.on("before_agent_start", async (event, ctx) => {
    const context = await runtime.beforeAgentStart(event.prompt, ctx);
    if (!context) return;
    // Preserve existing prompt blocks byte-for-byte for OMP's prefix cache.
    // systemPrompt is typed as string[] (verified in the shipped types), but
    // the devDependency range spans builds this wasn't confirmed against —
    // never spread an unexpected shape into the model's prompt.
    const existing = Array.isArray(event.systemPrompt) ? event.systemPrompt : [event.systemPrompt].filter(Boolean);
    return { systemPrompt: [...existing, ...context] };
  });
  omp.on("turn_start", () => runtime.turnStart());
  omp.on("turn_end", () => runtime.turnEnd());
  omp.on("tool_call", (event) => runtime.toolCall(event));
  omp.on("tool_result", (event) => runtime.toolResult(event));
  omp.on("session_before_compact", () => runtime.beforeCompact());
  omp.on("session_compact", () => runtime.compact());
}
