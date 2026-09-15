import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { isolatedCliEnv, runCli, runCliWithApi } from "./_cli.mjs";

test("setup --agent-native codex installs complete native integration plus MCP and workflow suite", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bin = join(isolated.home, "bin");
    mkdirSync(bin, { recursive: true });
    const codex = join(bin, "codex");
    const mcp = join(bin, "caveman-mcp");
    const proxy = join(bin, "caveman-proxy");
    writeFileSync(codex, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 1.0.0'; fi\n", { mode: 0o755 });
    writeFileSync(mcp, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
    writeFileSync(proxy, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\n", { mode: 0o755 });
    Object.assign(isolated.env, {
      PATH: `${bin}:${isolated.env.PATH}`,
      CAVEMAN_MCP_BIN: mcp,
      CAVEMAN_PROXY_BIN: proxy,
    });
    const out = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    const config = readFileSync(join(isolated.home, ".codex", "config.toml"), "utf8");
    assert.match(config, /\[mcp_servers\.caveman-cloud\]/);
    assert.match(config, /cloud", "mcp-serve"/);
    assert.match(config, /# >>> caveman:native-root/);
    assert.match(readFileSync(join(isolated.home, ".codex", "hooks.json"), "utf8"), /native-hook codex/);
    assert.ok(existsSync(join(isolated.home, "integrations", "codex.json")));
    assert.ok(existsSync(join(isolated.home, "integrations", "codex.agent-native-bundle.json")));
    assert.ok(existsSync(join(isolated.home, "mcp", "codex.caveman-cloud.json")));
    for (const skill of ["caveman-setup", "caveman-discover", "caveman-evidence-review", "caveman-optimize", "caveman-manage"]) {
      assert.ok(existsSync(join(isolated.home, ".codex", "skills", skill, "SKILL.md")), `${skill} missing`);
    }
    const setupSkillPath = join(isolated.home, ".codex", "skills", "caveman-setup", "SKILL.md");
    const configPath = join(isolated.home, ".codex", "config.toml");
    const cloudMarkerPath = join(isolated.home, "mcp", "codex.caveman-cloud.json");
    assert.match(out.stderr, /complete agent-native bundle ready/);
    assert.match(out.stderr, /coding policy: Core 2\.2\.0 on/);

    const bundleJournalPath = join(isolated.home, "integrations", "codex.agent-native-bundle.json");
    const pendingJournalPath = join(isolated.home, "integrations", "codex.agent-native-bundle.pending.json");
    const removalJournalPath = join(isolated.home, "integrations", "codex.agent-native-bundle.removing.json");

    writeFileSync(pendingJournalPath, readFileSync(bundleJournalPath));
    rmSync(cloudMarkerPath);
    const recoveredInstall = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(recoveredInstall.code, 0, recoveredInstall.stderr);
    assert.match(recoveredInstall.stderr, /completed interrupted codex agent-native bundle before continuing/);
    assert.ok(existsSync(cloudMarkerPath), "setup recovery did not restore MCP ownership marker");
    assert.equal(existsSync(pendingJournalPath), false);

    const targetJournalBytes = readFileSync(bundleJournalPath);
    const targetJournal = JSON.parse(targetJournalBytes);
    const targetSetupSkill = readFileSync(setupSkillPath, "utf8");
    const priorSetupSkill = `${targetSetupSkill}\n<!-- prior Caveman pack -->\n`;
    const committedJournal = structuredClone(targetJournal);
    committedJournal.pack_version = "2.1.0";
    committedJournal.skills.find((skill) => skill.file === setupSkillPath).after_sha256 = `sha256:${createHash("sha256").update(priorSetupSkill).digest("hex")}`;
    committedJournal.cloud_mcp = { command: join(bin, "prior-caveman"), args: targetJournal.cloud_mcp.args };
    writeFileSync(setupSkillPath, priorSetupSkill);
    writeFileSync(bundleJournalPath, JSON.stringify(committedJournal, null, 2) + "\n");
    writeFileSync(pendingJournalPath, targetJournalBytes);
    writeFileSync(cloudMarkerPath, JSON.stringify({ tool: "caveman_context", ...committedJournal.cloud_mcp }, null, 2) + "\n");
    writeFileSync(
      configPath,
      readFileSync(configPath, "utf8").replace(
        /(\[mcp_servers\.caveman-cloud\]\ncommand = )[^\n]+/,
        `$1${JSON.stringify(committedJournal.cloud_mcp.command)}`,
      ),
    );
    const recoveredUpgrade = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(recoveredUpgrade.code, 0, recoveredUpgrade.stderr);
    assert.match(recoveredUpgrade.stderr, /completed interrupted codex agent-native bundle before continuing/);
    assert.equal(readFileSync(setupSkillPath, "utf8"), targetSetupSkill);
    assert.deepEqual(JSON.parse(readFileSync(cloudMarkerPath, "utf8")).command, targetJournal.cloud_mcp.command);
    assert.equal(JSON.parse(readFileSync(bundleJournalPath, "utf8")).pack_version, targetJournal.pack_version);

    writeFileSync(removalJournalPath, readFileSync(bundleJournalPath));
    rmSync(setupSkillPath);
    writeFileSync(
      configPath,
      readFileSync(configPath, "utf8").replace(/\n?\[mcp_servers\.caveman-cloud\]\n[\s\S]*$/, ""),
    );
    const recoveredRemoval = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(recoveredRemoval.code, 0, recoveredRemoval.stderr);
    assert.match(recoveredRemoval.stderr, /rolled back interrupted codex agent-native bundle removal/);
    assert.ok(existsSync(setupSkillPath));
    assert.ok(existsSync(cloudMarkerPath), "removal recovery did not restore MCP ownership marker");
    assert.match(readFileSync(configPath, "utf8"), /\[mcp_servers\.caveman-cloud\]/);
    assert.equal(existsSync(removalJournalPath), false);

    const bundleLock = join(isolated.home, "integrations", ".lock-agent-native-bundle-codex");
    mkdirSync(bundleLock, { recursive: true });
    writeFileSync(join(bundleLock, "owner.json"), JSON.stringify({ pid: process.pid, token: "test" }));
    const concurrent = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.notEqual(concurrent.code, 0);
    assert.match(concurrent.stderr, /integration change already running for agent-native-bundle-codex/);
    rmSync(bundleLock, { recursive: true });

    const repeated = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(repeated.code, 0, repeated.stderr);
    assert.match(repeated.stderr, /complete agent-native bundle ready/);
    for (const override of [{ CAVEMAN_NATIVE_MODE: "record" }, { CAVEMAN_NATIVE_PROFILE: "record-only" }]) {
      const recordSetup = await runCli(["setup", "--agent-native", "codex"], { env: { ...isolated.env, ...override } });
      assert.equal(recordSetup.code, 0, recordSetup.stderr);
      assert.match(recordSetup.stderr, /Core 2\.2\.0 configured on; inactive under record mode\/profile/);
    }

    const canonicalSetupSkill = readFileSync(setupSkillPath, "utf8");
    writeFileSync(setupSkillPath, `${canonicalSetupSkill}\nuser edit\n`);
    const refusedSkillOverwrite = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.notEqual(refusedSkillOverwrite.code, 0);
    assert.match(refusedSkillOverwrite.stderr, /refusing to overwrite user skill edits/);
    assert.match(readFileSync(setupSkillPath, "utf8"), /user edit/);
    writeFileSync(setupSkillPath, canonicalSetupSkill);

    const drifted = readFileSync(configPath, "utf8").replace(
      /(\[mcp_servers\.caveman-cloud\]\ncommand = )[^\n]+/,
      '$1"changed-after-setup"',
    );
    writeFileSync(configPath, drifted);
    const refusedRemoval = await runCli(["setup", "--agent-native", "codex", "--remove"], { env: isolated.env });
    assert.notEqual(refusedRemoval.code, 0);
    assert.match(refusedRemoval.stderr, /MCP changed after setup; refusing destructive bundle removal/);
    assert.ok(existsSync(join(isolated.home, "integrations", "codex.agent-native-bundle.json")));

    const repaired = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.equal(repaired.code, 0, repaired.stderr);

    const hooksPath = join(isolated.home, ".codex", "hooks.json");
    const canonicalHooks = readFileSync(hooksPath, "utf8");
    const changedHooks = JSON.parse(canonicalHooks);
    const changedEntry = changedHooks.hooks.PreToolUse.find((entry) => JSON.stringify(entry).includes("shrink-hook"));
    assert.ok(changedEntry, "fixture missing owned shrink-hook entry");
    changedEntry.changed_after_setup = true;
    writeFileSync(hooksPath, JSON.stringify(changedHooks, null, 2) + "\n");
    const refusedNativeDriftRemoval = await runCli(["setup", "--agent-native", "codex", "--remove"], { env: isolated.env });
    assert.notEqual(refusedNativeDriftRemoval.code, 0);
    assert.match(refusedNativeDriftRemoval.stderr, /Caveman hook changed after enable; refusing destructive disable/);
    assert.ok(existsSync(setupSkillPath), "skill changed before native removal preflight failed");
    assert.match(readFileSync(configPath, "utf8"), /mcp_servers\.caveman-cloud/);
    writeFileSync(hooksPath, canonicalHooks);

    const removed = await runCli(["setup", "--agent-native", "codex", "--remove"], { env: isolated.env });
    assert.equal(removed.code, 0, removed.stderr);
    assert.match(removed.stderr, /agent-native bundle removed/);
    const restoredConfigPath = join(isolated.home, ".codex", "config.toml");
    if (existsSync(restoredConfigPath)) {
      assert.doesNotMatch(readFileSync(restoredConfigPath, "utf8"), /mcp_servers\.caveman-cloud|caveman:native-root/);
    }
    assert.equal(existsSync(join(isolated.home, ".codex", "hooks.json")), false);
    assert.equal(existsSync(join(isolated.home, "integrations", "codex.agent-native-bundle.json")), false);
    assert.equal(existsSync(join(isolated.home, ".codex", "skills", "caveman-setup", "SKILL.md")), false);
  } finally {
    isolated.cleanup();
  }
});

test("setup --agent-native preflight failure leaves no partial public components", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bin = join(isolated.home, "bin");
    mkdirSync(bin, { recursive: true });
    const codex = join(bin, "codex");
    const mcp = join(bin, "caveman-mcp");
    writeFileSync(codex, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 1.0.0'; fi\n", { mode: 0o755 });
    writeFileSync(mcp, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
    Object.assign(isolated.env, {
      PATH: `${bin}:${isolated.env.PATH}`,
      CAVEMAN_MCP_BIN: mcp,
      CAVEMAN_PROXY_BIN: join(bin, "missing-proxy"),
    });
    const out = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.notEqual(out.code, 0);
    assert.equal(existsSync(join(isolated.home, ".codex", "config.toml")), false);
    assert.equal(existsSync(join(isolated.home, ".codex", "skills", "caveman-setup", "SKILL.md")), false);
    assert.equal(existsSync(join(isolated.home, "integrations", "codex.agent-native-bundle.json")), false);
  } finally {
    isolated.cleanup();
  }
});

test("setup --agent-native refuses unjournaled stale cloud MCP before native writes", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bin = join(isolated.home, "bin");
    mkdirSync(bin, { recursive: true });
    const codex = join(bin, "codex");
    const mcp = join(bin, "caveman-mcp");
    const proxy = join(bin, "caveman-proxy");
    writeFileSync(codex, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 1.0.0'; fi\n", { mode: 0o755 });
    writeFileSync(mcp, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
    writeFileSync(proxy, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\n", { mode: 0o755 });
    Object.assign(isolated.env, { PATH: `${bin}:${isolated.env.PATH}`, CAVEMAN_MCP_BIN: mcp, CAVEMAN_PROXY_BIN: proxy });
    const configPath = join(isolated.home, ".codex", "config.toml");
    mkdirSync(join(isolated.home, ".codex"), { recursive: true });
    const stale = '[mcp_servers.caveman-cloud]\ncommand = "other"\nargs = ["stale"]\n';
    writeFileSync(configPath, stale);

    const out = await runCli(["setup", "--agent-native", "codex"], { env: isolated.env });
    assert.notEqual(out.code, 0);
    assert.match(out.stderr, /not Caveman-journaled; refusing overwrite/);
    assert.equal(readFileSync(configPath, "utf8"), stale);
    assert.equal(existsSync(join(isolated.home, "integrations", "codex.json")), false);
    assert.equal(existsSync(join(isolated.home, ".codex", "skills", "caveman-setup", "SKILL.md")), false);
  } finally {
    isolated.cleanup();
  }
});

test("setup --agent-native preserves an unjournaled Claude cloud MCP registration", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bin = join(isolated.home, "bin");
    mkdirSync(bin, { recursive: true });
    const claude = join(bin, "claude");
    const mcp = join(bin, "caveman-mcp");
    const proxy = join(bin, "caveman-proxy");
    writeFileSync(claude, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'claude 1.0.0'; fi\n", { mode: 0o755 });
    writeFileSync(mcp, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
    writeFileSync(proxy, "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\n", { mode: 0o755 });
    Object.assign(isolated.env, { PATH: `${bin}:${isolated.env.PATH}`, CAVEMAN_MCP_BIN: mcp, CAVEMAN_PROXY_BIN: proxy });
    const configPath = join(isolated.home, ".claude.json");
    const before = JSON.stringify({ mcpServers: { "caveman-cloud": { command: "user-command", args: ["keep"] } }, theme: "keep" }, null, 2) + "\n";
    writeFileSync(configPath, before);

    const out = await runCli(["setup", "--agent-native", "claude"], { env: isolated.env });
    assert.notEqual(out.code, 0);
    assert.match(out.stderr, /Claude caveman-cloud MCP exists but is not Caveman-journaled; refusing overwrite/);
    assert.equal(readFileSync(configPath, "utf8"), before);
    assert.equal(existsSync(join(isolated.home, ".claude", "skills", "caveman-setup", "SKILL.md")), false);
    assert.equal(existsSync(join(isolated.home, "integrations", "claude.json")), false);

    const clean = JSON.stringify({ theme: "keep" }, null, 2) + "\n";
    writeFileSync(configPath, clean);
    const installed = await runCli(["setup", "--agent-native", "claude"], { env: isolated.env });
    assert.equal(installed.code, 0, installed.stderr);
    const activeBytes = readFileSync(configPath, "utf8");
    const active = JSON.parse(activeBytes);
    assert.match(active.mcpServers["caveman-cloud"].command, /node/);
    assert.ok(existsSync(join(isolated.home, ".claude", "skills", "caveman-setup", "SKILL.md")));

    active.mcpServers["caveman-cloud"].env = { KEEP: "user" };
    writeFileSync(configPath, JSON.stringify(active, null, 2) + "\n");
    const refusedChangedShape = await runCli(["setup", "--agent-native", "claude"], { env: isolated.env });
    assert.notEqual(refusedChangedShape.code, 0);
    assert.match(refusedChangedShape.stderr, /changed after setup; refusing to overwrite user fields/);
    assert.deepEqual(JSON.parse(readFileSync(configPath, "utf8")).mcpServers["caveman-cloud"].env, { KEEP: "user" });
    const refusedChangedShapeRemoval = await runCli(["setup", "--agent-native", "claude", "--remove"], { env: isolated.env });
    assert.notEqual(refusedChangedShapeRemoval.code, 0);
    assert.match(refusedChangedShapeRemoval.stderr, /MCP changed after setup; refusing destructive bundle removal/);

    writeFileSync(configPath, activeBytes);
    const removed = await runCli(["setup", "--agent-native", "claude", "--remove"], { env: isolated.env });
    assert.equal(removed.code, 0, removed.stderr);
    assert.equal(readFileSync(configPath, "utf8"), clean);
  } finally {
    isolated.cleanup();
  }
});

test("Codex cloud MCP uninstall removes complete block and preserves unrelated config", async () => {
  const isolated = isolatedCliEnv();
  try {
    const configPath = join(isolated.home, ".codex", "config.toml");
    const unrelated = [
      'model = "gpt-test"',
      "",
      "[mcp_servers.keep]",
      'command = "keep"',
      'args = ["one", "two"]',
      "",
    ].join("\n");
    mkdirSync(join(isolated.home, ".codex"), { recursive: true });
    writeFileSync(configPath, unrelated);
    const installed = await runCli(["mcp", "install", "codex", "--server", "caveman-cloud"], { env: isolated.env });
    assert.equal(installed.code, 0, installed.stderr);
    assert.match(readFileSync(configPath, "utf8"), /cloud", "mcp-serve"/);

    const out = await runCli(["mcp", "uninstall", "codex", "--server", "caveman-cloud"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    const cleaned = readFileSync(configPath, "utf8");
    assert.equal(cleaned, unrelated);
    assert.doesNotMatch(cleaned, /cloud", "mcp-serve"|mcp_servers\.caveman-cloud/);
  } finally {
    isolated.cleanup();
  }
});

test("trace search sends closed structured filters and selected project", async () => {
  const out = await runCliWithApi([
    "cloud",
    "traces",
    "search",
    "--workflow",
    "support-reply",
    "--has-error",
    "true",
    "--min-cost-usd",
    "0.25",
    "--limit",
    "25",
    "--sort",
    "total_cost_usd",
    "--dir",
    "desc",
  ], {
    respond: () => ({ body: { data: [] } }),
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.requests.length, 1);
  assert.equal(out.requests[0].path, "/api/v1/traces/search?project_id=proj-alias");
  assert.deepEqual(JSON.parse(out.requests[0].body), {
    filters: {
      workflow: ["support-reply"],
      has_error: true,
      min_cost_usd: 0.25,
    },
    sort: { by: "total_cost_usd", dir: "desc" },
    page_size: 25,
  });
});

test("trace show scopes detail and spans to selected project", async () => {
  const out = await runCliWithApi(["cloud", "traces", "show", "0123456789abcdef", "--spans"], {
    respond: () => ({ body: {} }),
  });
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(
    out.requests.map((request) => request.path).sort(),
    [
      "/api/v1/traces/0123456789abcdef/spans?project_id=proj-alias",
      "/api/v1/traces/0123456789abcdef?project_id=proj-alias",
    ].sort(),
  );
});

test("experiment CLI rejects lifecycle mutation verbs without HTTP", async () => {
  const out = await runCliWithApi([
    "cloud",
    "experiments",
    "approve",
    "exp-1",
    "--confirm",
    "approve:exp-1",
  ]);
  assert.equal(out.code, 2);
  assert.equal(out.requests.length, 0);
  assert.match(out.stderr, /experiments list\|show <id>\|results <id>/);
});
