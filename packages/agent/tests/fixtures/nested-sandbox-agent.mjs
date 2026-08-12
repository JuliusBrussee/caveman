import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import {
  agent,
  auto,
  schema,
  subagent,
  tool,
} from "../../dist/index.js";
import { modularValue } from "../tool-modules/sandbox-tool-module.mjs";

const grandchild = agent({
  id: "nested-grandchild",
  instructions: "Use deep_read when requested.",
  model: "anthropic/claude-haiku-4-5",
  tools: [
    tool({
      name: "deep_read",
      description: "Read deep local module value.",
      input: schema.object({}),
      effect: "read",
      execute() {
        return `nested-grandchild:${modularValue()}`;
      },
    }),
  ],
  sandbox: "required",
});

const child = agent({
  id: "nested-child",
  instructions: "Use requested child tool.",
  model: "anthropic/claude-haiku-4-5",
  tools: [
    tool({
      name: "nested_read",
      description: "Read child local module value.",
      input: schema.object({}),
      effect: "read",
      execute() {
        return `nested-child:${modularValue()}`;
      },
    }),
    tool({
      name: "nested_home_read",
      description: "Attempt host-home read from child sandbox.",
      input: schema.object({}),
      effect: "read",
      execute() {
        return readFile(`${homedir()}/.ssh/config`, "utf8");
      },
    }),
    subagent({
      name: "delegate_deep",
      description: "Delegate to grandchild.",
      agent: grandchild,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    }),
  ],
  sandbox: "required",
});

export default agent({
  id: "nested-parent",
  instructions: "Delegate requested work.",
  model: auto(),
  tools: [
    tool({
      name: "nested_read",
      description: "Root collision sentinel.",
      input: schema.object({}),
      effect: "read",
      execute() {
        return "root-wrong-tool";
      },
    }),
    subagent({
      name: "delegate_child",
      description: "Delegate to child.",
      agent: child,
      maxCostUsd: 10,
      maxContextTokens: 10_000,
    }),
  ],
  sandbox: "required",
});
