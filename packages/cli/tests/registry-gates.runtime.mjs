// registry-gates.runtime.mjs — the fail-closed honesty gates the two registry compilers
// enforce (issues #135, #136). Each gate is proven both ways: a poisoned input must fail
// the compile, and the shipped registries must pass.
import { test } from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, writeFileSync, readFileSync, cpSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

const here = dirname(fileURLToPath(import.meta.url));
const packageParent = join(here, "..", "..");
const publicRoot = existsSync(join(packageParent, "agents")) ? packageParent : join(packageParent, "..");
const agentsCompile = join(publicRoot, "agents", "compile.mjs");
const integrationsCompile = join(publicRoot, "integrations", "compile.mjs");
const profilesDir = join(publicRoot, "agents", "profiles");

function run(script, argv, env) {
  return spawnSync(process.execPath, [script, ...argv], { encoding: "utf8", env: { ...process.env, ...env } });
}

function tmp(prefix) {
  return mkdtempSync(join(tmpdir(), prefix));
}

// ---- #136: integration-recipe catalog cross-check ----
test("integration compile fails closed on an unpriced recipe model", () => {
  const dir = tmp("cave-recipes-bad-");
  const cli = tmp("cave-cli-out-");
  writeFileSync(join(dir, "x.json"), JSON.stringify({
    schema_version: "1", id: "x", display_name: "X", lang: "ts", wire_protocol: "openai-responses",
    code: "const m = { model: \"gpt-4.1\", baseURL: \"{{baseURL}}/w/{{app}}/openai/v1\" };", models: ["gpt-4.1"],
  }));
  const res = run(integrationsCompile, [], { CAVEMAN_RECIPES_DIR: dir, CAVEMAN_CLI_DIR: cli });
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /not priced in the provider catalog/);
});

test("integration compile fails closed when code pins a model not declared in models", () => {
  const dir = tmp("cave-recipes-undeclared-");
  const cli = tmp("cave-cli-out-");
  writeFileSync(join(dir, "x.json"), JSON.stringify({
    schema_version: "1", id: "x", display_name: "X", lang: "ts", wire_protocol: "openai-responses",
    code: "const m = { model: \"gpt-5.5\", baseURL: \"{{baseURL}}/w/{{app}}/openai/v1\" };", models: [],
  }));
  const res = run(integrationsCompile, [], { CAVEMAN_RECIPES_DIR: dir, CAVEMAN_CLI_DIR: cli });
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /not declared in models/);
});

// The passing path (a catalog-present model compiles cleanly) is covered by the real
// `node public/integrations/compile.mjs` in the CLI build/test; it is not re-run here
// because that compiler also writes the committed recipes.json + web copy to fixed paths.

// ---- #135: injection_completeness gate (declared tier must match the CLI's real routing) ----
test("agent compile fails closed when a profile with a builder claims declarative", () => {
  const dir = tmp("cave-profile-");
  const bad = join(dir, "openclaw.json");
  const profile = JSON.parse(readFileSync(join(profilesDir, "openclaw.json"), "utf8"));
  profile.injection_completeness = "declarative";
  writeFileSync(bad, JSON.stringify(profile));
  const res = run(agentsCompile, ["--check-profile", bad], {});
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /cannot be "declarative": the CLI has a code builder for "openclaw"/);
});

test("agent compile fails closed when a builder-routed profile omits injection_completeness", () => {
  const dir = tmp("cave-profile-omit-");
  const bad = join(dir, "openclaw.json");
  const profile = JSON.parse(readFileSync(join(profilesDir, "openclaw.json"), "utf8"));
  delete profile.injection_completeness; // omitting the tier must NOT silently escape the gate
  writeFileSync(bad, JSON.stringify(profile));
  const res = run(agentsCompile, ["--check-profile", bad], {});
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /injection_completeness is required/);
});

test("agent compile fails loud when the builder-manifest scrape finds zero builders", () => {
  const cli = tmp("cave-cli-nobuilders-");
  mkdirSync(join(cli, "src"), { recursive: true });
  writeFileSync(join(cli, "src", "index.ts"), "export const nothing = 1;\n"); // no builder patterns
  const prof = join(cli, "claude.json");
  writeFileSync(prof, readFileSync(join(profilesDir, "claude.json"), "utf8"));
  const res = run(agentsCompile, ["--check-profile", prof], { CAVEMAN_CLI_DIR: cli });
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /found ZERO builders/);
});

// ---- #136: profile injection cannot pin an unpriced model ----
test("agent compile fails closed on an unpriced model pinned in a profile config", () => {
  const dir = tmp("cave-profile-model-");
  const bad = join(dir, "opencode.json");
  const profile = JSON.parse(readFileSync(join(profilesDir, "opencode.json"), "utf8"));
  profile.injection.config_content.managed.model = "caveman/gpt-4o"; // unpriced legacy id
  writeFileSync(bad, JSON.stringify(profile));
  const res = run(agentsCompile, ["--check-profile", bad], {});
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /not priced in the provider catalog/);
});

// ---- #135: agent-conformance pins are derived from tested_agent_version ----
test("agent compile fails closed when a conformance pin diverges from the profile pin", () => {
  const dir = tmp("cave-wf-");
  const cli = tmp("cave-cli-out-");
  // Copy real profiles so the compile only trips on the workflow pin mismatch.
  const profiles = join(dir, "profiles");
  cpSync(profilesDir, profiles, { recursive: true });
  const wf = join(dir, "agent-conformance.yml");
  writeFileSync(wf, [
    "jobs:",
    "  upstream-binary:",
    "    strategy:",
    "      matrix:",
    "        include:",
    "          - id: claude",
    "            install: npm install --global @anthropic-ai/claude-code@1.2.3",
    "",
  ].join("\n"));
  const res = run(agentsCompile, [], { CAVEMAN_PROFILES_DIR: profiles, CAVEMAN_CLI_DIR: cli, CAVEMAN_CONFORMANCE_WORKFLOW: wf });
  assert.equal(res.status, 1, res.stdout + res.stderr);
  assert.match(res.stderr, /pins claude@1\.2\.3 but profile tested_agent_version is/);
});
