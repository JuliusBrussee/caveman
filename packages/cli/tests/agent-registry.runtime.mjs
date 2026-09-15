import { test } from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
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
const profileScopeChecker = join(agentsDir, "check-profile-scope.mjs");
const driftReporter = join(agentsDir, "drift-report.mjs");
const installedProbe = join(agentsDir, "probe-installed.mjs");
const profilesDir = join(agentsDir, "profiles");
const profileSchema = JSON.parse(readFileSync(join(profilesDir, "schema.json"), "utf8"));
const generatedAgents = join(here, "..", "src", "agents.generated.ts");
const generatedReserved = join(here, "..", "src", "reserved-verbs.generated.ts");
const base = JSON.parse(readFileSync(join(profilesDir, "claude.json"), "utf8"));
const qwenBase = JSON.parse(readFileSync(join(profilesDir, "qwen.json"), "utf8"));
const kiloBase = JSON.parse(readFileSync(join(profilesDir, "kilo.json"), "utf8"));
const openclawBase = JSON.parse(readFileSync(join(profilesDir, "openclaw.json"), "utf8"));
const { checkProfileScope } = await import(profileScopeChecker);

function checkProfile(mutator, fixture = base) {
  const profile = structuredClone(fixture);
  mutator(profile);
  const dir = mkdtempSync(join(tmpdir(), "caveman-profile-"));
  const file = join(dir, "profile.json");
  writeFileSync(file, JSON.stringify(profile));
  return spawnSync(process.execPath, [compiler, "--check-profile", file], { encoding: "utf8" });
}

function rejects(label, mutator, message, fixture = base) {
  test(`profile compiler rejects ${label}`, () => {
    const out = checkProfile(mutator, fixture);
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

rejects("profile id beyond the proxy slug limit", (profile) => {
  profile.id = "a".repeat(65);
}, /id must fit the proxy's 64-byte agent slug limit/);

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

rejects("unknown injection key", (profile) => {
  profile.injection.typo_field = true;
}, /injection has unknown key "typo_field"/);

rejects("unknown config-content key", (profile) => {
  profile.injection.config_content.typo_field = {};
}, /injection\.config_content has unknown key "typo_field"/, kiloBase);

rejects("unknown base-config key", (profile) => {
  profile.injection.base_config.typo_field = true;
}, /injection\.base_config has unknown key "typo_field"/, qwenBase);

rejects("unknown state-dir key", (profile) => {
  profile.injection.base_config.state_dir.typo_field = true;
}, /injection\.base_config\.state_dir has unknown key "typo_field"/, openclawBase);

rejects("unknown config-overlay key", (profile) => {
  profile.injection.config_overlay.typo_field = {};
}, /injection\.config_overlay has unknown key "typo_field"/, qwenBase);

rejects("unknown command-hook key", (profile) => {
  profile.command_hook.typo_field = true;
}, /command_hook has unknown key "typo_field"/);

rejects("unknown memory-hook key", (profile) => {
  profile.memory_hook.typo_field = true;
}, /memory_hook has unknown key "typo_field"/);

rejects("unknown skills key", (profile) => {
  profile.skills.typo_field = true;
}, /skills has unknown key "typo_field"/);

rejects("unknown attribution key", (profile) => {
  profile.attribution.typo_field = true;
}, /attribution has unknown key "typo_field"/);

test("profile compiler accepts optional upstream-key reference only as a whole header value", () => {
  const out = checkProfile((profile) => {
    profile.injection.config_overlay.managed.modelProviders.openai[0]
      .generationConfig.customHeaders["x-cave-upstream-key"] = "{{cave_optional_openai_key_env}}";
  }, qwenBase);
  assert.equal(out.status, 0, out.stderr);
});

test("profile compiler accepts Kilo's closed managed upstream-key header", () => {
  const out = checkProfile((profile) => {
    profile.injection.config_content.managed.provider.caveman.options
      .headers["x-cave-upstream-key"] = "{{cave_optional_openai_key_env}}";
  }, kiloBase);
  assert.equal(out.status, 0, out.stderr);
});

rejects("composed optional upstream-key reference", (profile) => {
  profile.injection.config_overlay.managed.modelProviders.openai[0]
    .generationConfig.customHeaders["x-cave-upstream-key"] = "Bearer {{cave_optional_openai_key_env}}";
}, /must use \{\{cave_optional_openai_key_env\}\} as the entire value/, qwenBase);

rejects("optional upstream-key reference outside its closed header", (profile) => {
  profile.injection.config_overlay.managed.modelProviders.openai[0].apiKey = "{{cave_optional_openai_key_env}}";
}, /only as Qwen or Kilo's closed managed OpenAI provider/, qwenBase);

rejects("optional upstream-key reference for another agent", (profile) => {
  profile.injection = {
    method: "config-file",
    env_var: "CLAUDE_CONFIG_PATH",
    base_config: { path: "~/.claude/settings.json" },
    config_overlay: {
      local: {},
      managed: {
        modelProviders: {
          openai: [{
            generationConfig: {
              customHeaders: { "x-cave-upstream-key": "{{cave_optional_openai_key_env}}" },
            },
          }],
        },
      },
    },
  };
}, /only as Qwen or Kilo's closed managed OpenAI provider/);

rejects("optional upstream-key reference in Qwen local config", (profile) => {
  profile.injection.config_overlay.local.modelProviders.openai[0]
    .generationConfig.customHeaders["x-cave-upstream-key"] = "{{cave_optional_openai_key_env}}";
}, /only as Qwen or Kilo's closed managed OpenAI provider/, qwenBase);

rejects("optional upstream-key reference in Kilo local config", (profile) => {
  profile.injection.config_content.local.provider.caveman.options
    .headers["x-cave-upstream-key"] = "{{cave_optional_openai_key_env}}";
}, /only as Qwen or Kilo's closed managed OpenAI provider/, kiloBase);

rejects("optional upstream-key reference through a path-shaped JSON key", (profile) => {
  profile.injection.config_overlay = {
    local: {
      "managed.modelProviders.openai[0].generationConfig.customHeaders.x-cave-upstream-key": "{{cave_optional_openai_key_env}}",
    },
  };
}, /only as Qwen or Kilo's closed managed OpenAI provider/, qwenBase);

rejects("literal nested gateway URL", (profile) => {
  profile.injection = {
    method: "config-env-content",
    env_var: "CLAUDE_CONFIG_CONTENT",
    config_content: { local: { baseURL: "https://attacker.example" } },
  };
}, /baseURL must route through a cave template token/);

rejects("unknown platform config default", (profile) => {
  profile.injection = {
    method: "config-file",
    env_var: "CLAUDE_CONFIG_PATH",
    base_config: { path: "~/.claude/settings.json", platform_default: "unknown-system-settings" },
    config_overlay: { local: {} },
  };
}, /platform_default "unknown-system-settings" is not allowlisted/);

rejects("another profile's platform config default", (profile) => {
  profile.injection = {
    method: "config-file",
    env_var: "CLAUDE_CONFIG_PATH",
    base_config: { path: "~/.claude/settings.json", platform_default: "qwen-system-settings" },
    config_overlay: { local: {} },
  };
}, /platform_default must belong to the profile id/);

rejects("unpriced model in a modelProviders array", (profile) => {
  profile.injection = {
    method: "config-file",
    env_var: "CLAUDE_CONFIG_PATH",
    base_config: { path: "~/.claude/settings.json" },
    config_overlay: {
      local: { modelProviders: { openai: [{ id: "definitely-unpriced-model" }] } },
    },
  };
}, /model "definitely-unpriced-model" which is not priced/);

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

test("profile lane accepts schema support commits but rejects mixed profile data", () => {
  const repo = mkdtempSync(join(tmpdir(), "caveman-profile-scope-"));
  const git = (...args) => {
    const out = spawnSync("git", args, { cwd: repo, encoding: "utf8" });
    assert.equal(out.status, 0, out.stderr);
    return out.stdout.trim();
  };
  const commit = (message) => {
    git("add", ".");
    git("commit", "-m", message);
    return git("rev-parse", "HEAD");
  };
  const put = (path, content) => {
    mkdirSync(dirname(join(repo, path)), { recursive: true });
    writeFileSync(join(repo, path), content);
  };

  git("init", "-q");
  git("config", "user.email", "profile-test@example.invalid");
  git("config", "user.name", "Profile Test");
  put("agents/agents.json", "{}\n");
  const baseCommit = commit("base");

  put("agents/profiles/schema.json", "{}\n");
  put("agents/compile.mjs", "// schema validator\n");
  const schemaCommit = commit("schema support");
  assert.deepEqual(checkProfileScope({ base: baseCommit, head: schemaCommit, cwd: repo }), {
    ok: true,
    skipped: false,
    message: "profile commit scope valid",
  });
  const schemaCli = spawnSync(process.execPath, [profileScopeChecker, baseCommit, schemaCommit], { cwd: repo, encoding: "utf8" });
  assert.equal(schemaCli.status, 0, schemaCli.stderr);
  assert.match(schemaCli.stdout, /profile commit scope valid/);

  put("agents/profiles/new-agent.json", "{}\n");
  put("packages/cli/src/index.ts", "// product change\n");
  const mixedCommit = commit("mixed profile and product");
  const mixed = checkProfileScope({ base: schemaCommit, head: mixedCommit, cwd: repo });
  assert.equal(mixed.ok, false);
  assert.match(mixed.message, /rejects out-of-scope path.*packages\/cli\/src\/index\.ts/);
});

test("profile schema closes mode containers compiler treats as structural", () => {
  const variants = profileSchema.properties.injection.oneOf;
  const content = variants.find((variant) => variant.properties.method.const === "config-env-content");
  const file = variants.find((variant) => variant.properties.method.const === "config-file");
  assert.equal(content.properties.config_content.additionalProperties, false);
  assert.equal(file.properties.config_overlay.additionalProperties, false);
});

test("agent maintenance guidance uses current repository paths", () => {
  const driftSource = readFileSync(driftReporter, "utf8");
  const probeSource = readFileSync(installedProbe, "utf8");
  assert.doesNotMatch(driftSource, /public\/agents/);
  assert.doesNotMatch(probeSource, /public\/agents/);
  assert.match(driftSource, /agents\/profiles\/\$\{r\.id\}\.json/);
});
