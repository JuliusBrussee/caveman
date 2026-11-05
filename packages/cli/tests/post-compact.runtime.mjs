import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { isolatedCliEnv } from "./_cli.mjs";

test("Codex PostCompact accepts no hookSpecificOutput; SessionStart restores Core", () => {
  const isolated = isolatedCliEnv({ CAVEMAN_NATIVE_MODE: "safe" });
  try {
    const configDir = join(isolated.home, ".caveman-cloud");
    mkdirSync(configDir, { recursive: true });
    writeFileSync(join(configDir, "config.json"), JSON.stringify({ execute: { proxy: false } }));
    for (const entry of ["index.js", "native-hook-fast.js"]) {
      const run = (agent, payload) => {
        const result = spawnSync(process.execPath, [
          fileURLToPath(new URL(`../dist/${entry}`, import.meta.url)), "native-hook", agent,
        ], { env: isolated.env, input: JSON.stringify(payload), encoding: "utf8", timeout: 10000 });
        assert.equal(result.status, 0, result.stderr);
        return result.stdout;
      };
      for (const trigger of ["manual", "auto"]) {
        assert.equal(run("codex", {
          hook_event_name: "PostCompact", session_id: "compact-test", trigger,
        }), "", "Codex PostCompact must not emit unsupported hookSpecificOutput");
      }
      const restored = JSON.parse(run("codex", {
        hook_event_name: "SessionStart", session_id: "compact-test", source: "compact",
      }));
      assert.equal(restored.hookSpecificOutput.hookEventName, "SessionStart");
      assert.match(restored.hookSpecificOutput.additionalContext, /Build simplest complete system/);
      const claude = JSON.parse(run("claude", {
        hook_event_name: "PostCompact", session_id: "compact-claude",
      }));
      assert.equal(claude.hookSpecificOutput.hookEventName, "PostCompact");
    }
    const events = readFileSync(join(isolated.home, "runtime", "native-events.jsonl"), "utf8")
      .trim().split("\n").map((line) => JSON.parse(line));
    assert.equal(events.filter((entry) => entry.event === "PostCompact").length, 6);
  } finally {
    isolated.cleanup();
  }
});
