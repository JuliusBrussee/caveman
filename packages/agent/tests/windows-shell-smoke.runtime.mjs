import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

import { createCodingAgent } from "../dist/code.js";

test("coding-agent host shell runs on current OS", async () => {
  const workspace = await mkdtemp(resolve(tmpdir(), "caveman-agent-shell-"));
  try {
    const codingAgent = createCodingAgent({ workspace, model: "anthropic/faux-1" });
    const shell = codingAgent.definition.tools.find((item) => item.name === "bash");
    const result = await shell.execute({ command: "echo caveman-portable-shell" });
    assert.match(String(result), /exit 0/);
    assert.match(String(result), /caveman-portable-shell/);
  } finally {
    await rm(workspace, { recursive: true, force: true });
  }
});
