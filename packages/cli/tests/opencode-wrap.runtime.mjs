import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { test } from "node:test";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");
const { buildWrapEnv } = await import(pathToFileURL(join(dist, "index.js")).href);
const { PROFILES } = await import(pathToFileURL(join(dist, "agents.generated.js")).href);
const profile = PROFILES.find((candidate) => candidate.id === "opencode");

function withInlineConfig(raw, run) {
  const home = mkdtempSync(join(tmpdir(), "cave-opencode-config "));
  const patch = {
    HOME: home, USERPROFILE: home, CAVEMAN_HOME: home,
    CAVEMAN_CONFIG: join(home, "missing.yaml"),
    CAVE_NO_KEYCHAIN: "1", CAVEMAN_TELEMETRY: "0",
    OPENCODE_CONFIG_CONTENT: raw,
  };
  const old = new Map(Object.keys(patch).map((key) => [key, process.env[key]]));
  Object.assign(process.env, patch);
  try { return run(); }
  finally {
    for (const [key, value] of old) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    rmSync(home, { recursive: true, force: true });
  }
}

test("opencode wrap retains inline model, permission, MCP and provider account configuration", () => {
  const original = {
    model: "openai/gpt-4o-mini",
    permission: { "*": "deny", read: "allow" },
    mcp: { team: { type: "local", command: ["node", "./team-mcp.mjs"], enabled: true } },
    instructions: ["./team.md"],
    provider: {
      openai: { options: { apiKey: "{env:TEAM_API_KEY}", headers: { "OpenAI-Project": "team-project" } } },
      team: { npm: "@ai-sdk/openai-compatible", options: { baseURL: "https://team.example/v1" } },
    },
  };
  withInlineConfig(JSON.stringify(original), () => {
    const env = buildWrapEnv(profile, "http://127.0.0.1:8787", "false");
    const routed = JSON.parse(env.OPENCODE_CONFIG_CONTENT);
    for (const key of ["model", "permission", "mcp", "instructions"]) assert.deepEqual(routed[key], original[key]);
    assert.equal(routed.provider.openai.options.apiKey, "{env:TEAM_API_KEY}");
    assert.equal(routed.provider.openai.options.headers["OpenAI-Project"], "team-project");
    assert.deepEqual(routed.provider.team, original.provider.team);
    assert.equal(routed.provider.openai.options.baseURL, "http://127.0.0.1:8787/w/opencode/v1");
    assert.equal(process.env.OPENCODE_CONFIG_CONTENT, JSON.stringify(original));
  });
});

test("opencode wrap retains JSONC values without expanding native environment or file references", () => {
  const raw = `{
    // Native inline config supports comments and trailing commas.
    "model": "team/model",
    "instructions": ["{file:./instructions.md}",],
    "provider": {"team": {"options": {
      "apiKey": "{env:TEAM_API_KEY}",
      "baseURL": "https://team.example/v1?note=literal/*value*/",
    },},},
  }`;
  withInlineConfig(raw, () => {
    const routed = JSON.parse(buildWrapEnv(profile, "http://127.0.0.1:8787", "false").OPENCODE_CONFIG_CONTENT);
    assert.equal(routed.model, "team/model");
    assert.deepEqual(routed.instructions, ["{file:./instructions.md}"]);
    assert.equal(routed.provider.team.options.apiKey, "{env:TEAM_API_KEY}");
    assert.equal(routed.provider.team.options.baseURL, "https://team.example/v1?note=literal/*value*/");
  });
});

for (const raw of ["{ malformed-secret", "[]", "null", '"synthetic-secret"']) {
  test(`opencode refuses an invalid inline configuration instead of discarding it: ${raw[0]}`, () => {
    withInlineConfig(raw, () => {
      assert.throws(() => buildWrapEnv(profile, "http://127.0.0.1:8787", "false"), (error) => {
        assert.match(error.message, /opencode inline configuration/);
        assert.doesNotMatch(error.message, /secret/);
        return true;
      });
      assert.equal(process.env.OPENCODE_CONFIG_CONTENT, raw);
    });
  });
}
