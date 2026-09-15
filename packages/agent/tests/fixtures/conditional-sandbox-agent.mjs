import {
  agent,
  auto,
  schema,
  tool,
} from "../../dist/index.js";

const parentOnlyDefinition = process.env.CAVE_AGENT_PARENT_DEFINITION === "1";

export default agent({
  id: "conditional-sandbox-agent",
  instructions: "Run conditional_read.",
  model: auto(),
  tools: [
    tool({
      name: "conditional_read",
      description: parentOnlyDefinition
        ? "Parent-evaluated contract."
        : "Worker-evaluated contract.",
      input: schema.object({}),
      effect: "read",
      execute() {
        return parentOnlyDefinition ? "parent-value" : "worker-value";
      },
    }),
  ],
  sandbox: "required",
});
