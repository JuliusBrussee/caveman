import {
  agent,
  auto,
  eval as defineEval,
  output,
  schema,
  tool,
  type AgentDefinition,
  type EvalDefinition,
  type RunOptions,
  type StandardToolSchema,
  type ToolDefinition,
} from "../src/index.js";
import type { ClaudeRunOptions } from "../src/claude.js";
import type { ToolLoopAgent } from "ai";
import type { Agent as MastraAgent } from "@mastra/core/agent";
import type { ClientSession } from "eve/client";
import type {
  EveSessionBinding,
  MastraAgentBinding,
  VercelToolLoopAgentBinding,
} from "../src/adapters.js";

type Assert<T extends true> = T;
type VercelBindingCompatible = Assert<ToolLoopAgent extends VercelToolLoopAgentBinding ? true : false>;
type MastraBindingCompatible = Assert<MastraAgent extends MastraAgentBinding ? true : false>;
type EveBindingCompatible = Assert<ClientSession extends EveSessionBinding ? true : false>;

const lookup = tool({
  name: "lookup",
  description: "Lookup value.",
  input: schema.object({ id: schema.string() }),
  effect: "read",
  execute({ id }) {
    return { id };
  },
});

const defined: AgentDefinition = agent({
  id: "types",
  instructions: "Exact.",
  model: auto(),
  tools: [lookup],
  output: output({
    maxTokens: 100,
    schema: schema.object({ answer: schema.string() }),
  }),
});

const typedTool: ToolDefinition = lookup;
const standardInput = {
  "~standard": {
    version: 1 as const,
    vendor: "fixture",
    types: undefined as unknown as { input: { id: string }; output: { id: string } },
    validate(value: unknown) {
      return { value: value as { id: string } };
    },
    jsonSchema: {
      input() {
        return { type: "object", properties: { id: { type: "string" } }, required: ["id"] };
      },
      output() {
        return { type: "object", properties: { id: { type: "string" } }, required: ["id"] };
      },
    },
  },
} satisfies StandardToolSchema<{ id: string }, { id: string }>;
const standardTool = tool({
  name: "standard_lookup",
  description: "Lookup through Standard Schema.",
  input: standardInput,
  effect: "read",
  execute({ id }) {
    return { id };
  },
});
const validateOnlyStandard = {
  "~standard": {
    version: 1 as const,
    vendor: "fixture",
    types: undefined as unknown as { input: { id: string }; output: { id: string } },
    validate(value: unknown) {
      return { value: value as { id: string } };
    },
  },
};
const validateOnlyTool = tool({
  name: "validate_only_lookup",
  description: "Lookup through validation-only Standard Schema.",
  input: validateOnlyStandard,
  inputJSONSchema: schema.object({ id: schema.string() }),
  effect: "read",
  execute({ id }) {
    return { id };
  },
});
const typedEval: EvalDefinition = defineEval({
  id: "types",
  input: "x",
  quality: [{ type: "contains", fragments: ["x"] }],
});
const callerPathOptions: RunOptions = {
  // @ts-expect-error descendant routing is framework-private
  subagentPath: ["child"],
};
const callerDepthOptions: RunOptions = {
  // @ts-expect-error recursion state is framework-private
  subagentDepth: 1,
};
const callerPlanOptions: RunOptions = {
  // @ts-expect-error compiled plans enter through validated CLI/compiler path
  candidatePlan: {},
};
const callerBuildOptions: RunOptions = {
  // @ts-expect-error build identity enters through validated CLI/compiler path
  lockedBuild: {},
};
const callerClaudeBuildOptions: ClaudeRunOptions = {
  // @ts-expect-error Claude Cave Build identity enters through validated compiler path
  lockedBuild: {},
};

void defined;
void typedTool;
void standardTool;
void validateOnlyTool;
void typedEval;
void callerPathOptions;
void callerDepthOptions;
void callerPlanOptions;
void callerBuildOptions;
void callerClaudeBuildOptions;
void (null as unknown as VercelBindingCompatible);
void (null as unknown as MastraBindingCompatible);
void (null as unknown as EveBindingCompatible);
