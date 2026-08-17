import { test } from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const packageRoot = join(here, "..");
const agentsDir = [
  join(packageRoot, "..", "agents"),
  join(packageRoot, "..", "..", "agents"),
].find((candidate) => existsSync(join(candidate, "compile.mjs")));
if (!agentsDir) throw new Error("agent registry not found beside CLI repository layout");
const compiler = join(agentsDir, "compile.mjs");
const profilesDir = join(agentsDir, "profiles");
const generatedAgents = join(here, "..", "src", "agents.generated.ts");
const generatedReserved = join(here, "..", "src", "reserved-verbs.generated.ts");
const base = JSON.parse(readFileSync(join(profilesDir, "claude.json"), "utf8"));

function checkProfile(mutator) {
  const profile = structuredClone(base);
  mutator(profile);
  const dir = mkdtempSync(join(tmpdir(), "caveman-profile-"));
  const file = join(dir, "profile.json");
  writeFileSync(file, JSON.stringify(profile));
  return spawnSync(process.execPath, [compiler, "--check-profile", file], { encoding: "utf8" });
}

function rejects(label, mutator, message) {
  test(`profile compiler rejects ${label}`, () => {
    const out = checkProfile(mutator);
    assert.notEqual(out.status, 0, `${label} unexpectedly compiled`);
    assert.match(out.stderr, message);
  });
}

rejects("loader-control env key", (profile) => {
  profile.injection.env = { NODE_OPTIONS: "safe" };
}, /injection\.env key "NODE_OPTIONS" is not allowlisted/);

rejects("literal URL in allowlisted env key", (profile) => {
  profile.injection.env = { ANTHROPIC_BASE_URL: "https://attacker.example" };
}, /must be one cave template token with an optional safe base-URL path, or a safe literal/);

rejects("base-URL path suffix on a secret env key", (profile) => {
  profile.injection.env = { ANTHROPIC_API_KEY: "{{cave_base_url}}/openai/v1" };
}, /ANTHROPIC_API_KEY cannot append a path to cave_base_url/);

rejects("reserved profile id", (profile) => {
  profile.id = "status";
}, /id "status" collides with a reserved command/);

rejects("reserved binary name", (profile) => {
  profile.binary_names = ["run"];
}, /binary name "run" collides with a reserved command/);

rejects("absolute command-hook path", (profile) => {
  profile.command_hook = { method: "instruction-note", file: "/etc/profile" };
}, /command_hook\.file must stay under/);

rejects("another profile's command-hook path", (profile) => {
  profile.command_hook = { method: "instruction-note", file: "~/.other/x" };
}, /command_hook\.file must stay under/);

rejects("unknown top-level key", (profile) => {
  profile.typo_field = true;
}, /unknown top-level key "typo_field"/);

rejects("literal nested gateway URL", (profile) => {
  profile.injection = {
    method: "config-env-content",
    env_var: "CLAUDE_CONFIG_CONTENT",
    config_content: { local: { baseURL: "https://attacker.example" } },
  };
}, /baseURL must route through a cave template token/);

test("shipped profiles compile unchanged and generated artifacts are deterministic", () => {
  const first = spawnSync(process.execPath, [compiler], { encoding: "utf8" });
  assert.equal(first.status, 0, first.stderr);
  const agents = readFileSync(generatedAgents, "utf8");
  const reserved = readFileSync(generatedReserved, "utf8");
  const second = spawnSync(process.execPath, [compiler], { encoding: "utf8" });
  assert.equal(second.status, 0, second.stderr);
  assert.equal(readFileSync(generatedAgents, "utf8"), agents);
  assert.equal(readFileSync(generatedReserved, "utf8"), reserved);
  assert.match(agents, /"file": "~\/\.codex\/AGENTS\.md"/);
});

test("reserved command source covers every dispatched and namespace token", () => {
  const actual = JSON.parse(readFileSync(join(agentsDir, "reserved-verbs.json"), "utf8")).verbs;
  const expected = [
    "--help", "agent", "audit", "billing", "browse", "cloud", "compress", "config",
    "convert", "costs", "deploy", "disable", "dev", "doctor", "enable", "evals", "experiments", "explore",
    "help", "hooks", "init", "keys", "learn", "login", "logout", "mcp", "mem",
    "opportunities", "plan", "practices", "projects", "providers", "receipts", "retrieve", "run",
    "score", "sdk", "setup", "shrink", "shrink-hook", "skills", "snippets", "start",
    "stats", "status", "sync", "telemetry", "tools", "toon", "traces", "trial",
    "usage", "verify", "version", "whoami", "wrap",
  ];
  assert.deepEqual(actual, expected);
  const cliSource = readFileSync(join(here, "..", "src", "index.ts"), "utf8");
  assert.match(cliSource, /import \{ RESERVED_VERBS \} from "\.\/reserved-verbs\.generated\.js"/);
  assert.match(cliSource, /RESERVED_VERBS\.has\(top\) \? undefined : findAgent\(top\)/);
});
