import assert from "node:assert/strict";
import test from "node:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

test("learn implement launches a supported agent with the guarded learn prompt", async () => {
  const isolated = isolatedCliEnv();
  const cwd = mkdtempSync(join(tmpdir(), "caveman-learn-implement-"));
  const argvPath = join(isolated.home, "agent-argv.json");
  const claude = join(isolated.home, "bin", "claude");
  writeFileSync(claude, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(process.env.AGENT_ARGV_PATH, JSON.stringify(process.argv.slice(2)));
`, { mode: 0o755 });
  mkdirSync(join(isolated.home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(isolated.home, ".caveman-cloud", "config.json"), JSON.stringify({
    wrap: { proxy: false, shrink: false, mcp: false, browse: false },
  }));
  isolated.env.PATH = `${join(isolated.home, "bin")}:${process.env.PATH}`;
  isolated.env.AGENT_ARGV_PATH = argvPath;

  try {
    const out = await runCli(
      ["learn", "implement", "claude", "--prompt", "Focus on project instructions first."],
      { env: isolated.env, cwd },
    );
    assert.equal(out.code, 0, out.stderr);
    const argv = JSON.parse(readFileSync(argvPath, "utf8"));
    const prompt = argv.at(-1);
    assert.match(prompt, /Use the caveman-learn skill/);
    assert.match(prompt, /caveman learn report --json/);
    assert.match(prompt, /Never edit load_bearing findings/);
    assert.match(prompt, /ask before every edit/);
    assert.match(prompt, /Focus on project instructions first/);

    const skill = join(cwd, ".claude", "skills", "caveman-learn", "SKILL.md");
    assert.equal(existsSync(skill), true, "agent handoff must install its safety guide");
    assert.match(readFileSync(skill, "utf8"), /Consent per edit/);
    assert.match(out.stderr, /learn guide installed/);
  } finally {
    isolated.cleanup();
  }
});

test("learn implement help is short and does not launch an agent", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runCli(["learn", "implement", "--help"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /caveman learn implement \[claude\|codex\]/);
    assert.match(out.stdout, /asks before every edit/);
  } finally {
    isolated.cleanup();
  }
});
