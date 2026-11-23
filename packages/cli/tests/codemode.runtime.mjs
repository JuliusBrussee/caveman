import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync, chmodSync, existsSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  applyKiloCodemodeOverlay,
  applyOpenCodeCodemodeOverlay,
  buildHermesEphemeralCodemodeHome,
  detectOpenCodeConfigSchema,
  discoverCodemodeServers,
  discoverKiloOwnServerNames,
  discoverOpenCodeOwnServerNames,
  mergeHermesConfigEdits,
  NATIVE_CODEMODE_AGENTS,
  parseCodexMcpServersToml,
  parseHermesMcpServersYaml,
  patchEphemeralOpenClawCodemode,
  patchEphemeralQwenCodemode,
  rewriteHermesCodemodeYaml,
  stripExternalCodexMcpTables,
  syncBackHermesEphemeralHome,
  writeGeminiCodemodeSettingsOverlay,
} from "../dist/codemode.js";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

test("discoverCodemodeServers merges codex, claude, and project .mcp.json while excluding self", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-codemode-test-"));
  const home = join(root, "home");
  const ws = join(root, "repo");
  try {
    mkdirSync(join(home, ".codex"), { recursive: true });
    mkdirSync(ws, { recursive: true });

    writeFileSync(join(home, ".codex", "config.toml"), `
model = "gpt-5"

[mcp_servers.postgres]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-postgres"]
env = { PGHOST = "localhost" }

[mcp_servers.caveman]
command = "caveman-mcp"
`);

    writeFileSync(join(home, ".claude.json"), JSON.stringify({
      mcpServers: {
        github: { command: "github-mcp", args: ["stdio"] },
        "caveman-codemode": { command: "caveman", args: ["codemode"] },
      },
      projects: {
        [ws]: {
          mcpServers: {
            sentry: { command: "sentry-mcp" },
          },
        },
      },
    }));

    writeFileSync(join(ws, ".mcp.json"), JSON.stringify({
      mcpServers: {
        github: { command: "github-mcp-local", args: ["--repo", "demo"] },
        remote_http: { url: "https://example.invalid/mcp" },
      },
    }));

    // Default (untrusted workspace): ignores checkout `.mcp.json`, loads user-home configs only.
    const defaultDiscovered = discoverCodemodeServers(ws, home);
    assert.equal(defaultDiscovered.caveman, undefined);
    assert.equal(defaultDiscovered["caveman-codemode"], undefined);
    assert.equal(defaultDiscovered.remote_http, undefined, "untrusted workspace .mcp.json must be skipped by default");
    assert.equal(defaultDiscovered.github?.command, "github-mcp", "untrusted workspace .mcp.json must not shadow ~/.claude.json");
    assert.equal(defaultDiscovered.postgres?.command, "npx");
    assert.equal(defaultDiscovered.postgres?.env?.PGHOST, "localhost");
    assert.equal(defaultDiscovered.sentry?.command, "sentry-mcp");

    // Explicit opt-in (`trustWorkspace = true`): merges project `.mcp.json` on top.
    const trusted = discoverCodemodeServers(ws, home, process.env, true);
    assert.equal(trusted.remote_http?.url, "https://example.invalid/mcp");
    assert.equal(trusted.github?.command, "github-mcp-local");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("per-agent ephemeral codemode overlays preserve user auth/settings and never mutate originals", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-codemode-agents-"));
  const cmEntry = { command: "/bin/caveman-mcp", args: ["codemode", "--config", "/tmp/c.json", "--workspace", "/tmp/ws"] };
  try {
    // 1. Gemini CLI overlay preserves security/auth while swapping only mcpServers
    const geminiBase = join(root, "gemini-settings.json");
    writeFileSync(geminiBase, JSON.stringify({ security: { auth: { selectedType: "gemini-api-key" } }, mcpServers: { old: { command: "old-mcp" } } }));
    const geminiOut = writeGeminiCodemodeSettingsOverlay(root, geminiBase, cmEntry);
    const geminiDoc = JSON.parse(readFileSync(geminiOut, "utf8"));
    assert.equal(geminiDoc.security.auth.selectedType, "gemini-api-key");
    assert.equal(geminiDoc.mcpServers.old, undefined);
    assert.equal(geminiDoc.mcpServers["caveman-codemode"].command, "/bin/caveman-mcp");
    // Original untouched
    assert.equal(JSON.parse(readFileSync(geminiBase, "utf8")).mcpServers.old.command, "old-mcp");

    // 2. OpenCode overlay disables bridged external MCPs and registers caveman-codemode
    const ocRaw = applyOpenCodeCodemodeOverlay(JSON.stringify({ provider: { openai: { options: { baseURL: "http://127.0.0.1:8787/v1" } } } }), ["github", "linear"], cmEntry);
    const ocDoc = JSON.parse(ocRaw);
    assert.equal(ocDoc.provider.openai.options.baseURL, "http://127.0.0.1:8787/v1");
    assert.equal(ocDoc.mcp.github.enabled, false);
    assert.equal(ocDoc.mcp.linear.enabled, false);
    assert.deepEqual(ocDoc.mcp["caveman-codemode"].command, [cmEntry.command, ...cmEntry.args]);

    // 3. Kilo overlay adds caveman-codemode while keeping existing provider config
    const kiloDoc = JSON.parse(applyKiloCodemodeOverlay(JSON.stringify({ model: "caveman/gpt-5.5" }), cmEntry));
    assert.equal(kiloDoc.model, "caveman/gpt-5.5");
    assert.equal(kiloDoc.mcp["caveman-codemode"].enabled, true);

    // 4. OpenClaw ephemeral file patch swaps mcp.servers cleanly
    const clawPath = join(root, "openclaw.json");
    writeFileSync(clawPath, JSON.stringify({ model: "gpt-5", mcp: { servers: { old: { command: "old" } } } }));
    patchEphemeralOpenClawCodemode(clawPath, cmEntry);
    const clawDoc = JSON.parse(readFileSync(clawPath, "utf8"));
    assert.equal(clawDoc.model, "gpt-5");
    assert.equal(clawDoc.mcp.servers.old, undefined);
    assert.equal(clawDoc.mcp.servers["caveman-codemode"].command, "/bin/caveman-mcp");

    // 5. Hermes ephemeral home preserves model/auth and swaps only mcp_servers
    const hermesSrc = join(root, "hermes-src");
    const hermesDst = join(root, "hermes-dst");
    mkdirSync(hermesSrc, { recursive: true });
    mkdirSync(hermesDst, { recursive: true });
    writeFileSync(join(hermesSrc, "config.yaml"), "model:\n  name: gpt-5\nmcp_servers:\n  datadog:\n    command: dd-mcp\n");
    writeFileSync(join(hermesSrc, "auth.json"), "{\"ok\":true}\n");
    buildHermesEphemeralCodemodeHome(hermesSrc, hermesDst, cmEntry);
    const hermesCfg = readFileSync(join(hermesDst, "config.yaml"), "utf8");
    assert.match(hermesCfg, /name: gpt-5/);
    assert.doesNotMatch(hermesCfg, /datadog/);
    assert.match(hermesCfg, /caveman-codemode:/);
    assert.equal(readFileSync(join(hermesDst, "auth.json"), "utf8"), "{\"ok\":true}\n");

    // 6. Qwen ephemeral system settings overlay keeps model/auth policies while swapping only mcpServers
    const qwenSysPath = join(root, "qwen-system-settings.json");
    writeFileSync(qwenSysPath, JSON.stringify({ model: { name: "gpt-5.5" }, security: { auth: { selectedType: "openai" } }, mcpServers: { legacy: { command: "legacy-mcp" } } }));
    patchEphemeralQwenCodemode(qwenSysPath, cmEntry);
    const qwenDoc = JSON.parse(readFileSync(qwenSysPath, "utf8"));
    assert.equal(qwenDoc.model.name, "gpt-5.5");
    assert.equal(qwenDoc.security.auth.selectedType, "openai");
    assert.equal(qwenDoc.mcpServers.legacy, undefined);
    assert.equal(qwenDoc.mcpServers["caveman-codemode"].command, "/bin/caveman-mcp");

    // 7. Native codemode agent registry accurately tracks Claude Code, Codex CLI, OpenCode, and Hermes
    assert.equal(NATIVE_CODEMODE_AGENTS.has("claude"), true);
    assert.equal(NATIVE_CODEMODE_AGENTS.has("codex"), true);
    assert.equal(NATIVE_CODEMODE_AGENTS.has("opencode"), true);
    assert.equal(NATIVE_CODEMODE_AGENTS.has("hermes"), true);
    assert.equal(NATIVE_CODEMODE_AGENTS.has("pi"), false);
    assert.equal(NATIVE_CODEMODE_AGENTS.has("gemini"), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Hermes codemode YAML rewrite, 4-space parsing, and ephemeral HERMES_HOME sync-back", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-hermes-cm-"));
  const cmEntry = { command: "/bin/caveman-mcp", args: ["codemode", "--config", "/tmp/c.json"] };
  try {
    // 1. rewriteHermesCodemodeYaml handles inline `mcp_servers: {}` and trailing `...` without duplicate keys.
    const inlineEmpty = `model:\n  name: gpt-5.4\nmcp_servers: {} # empty\n...\n`;
    const rewritten = rewriteHermesCodemodeYaml(inlineEmpty, cmEntry);
    assert.equal((rewritten.match(/^mcp_servers:/gm) ?? []).length, 1, `duplicate mcp_servers in:\n${rewritten}`);
    assert.ok(rewritten.indexOf("mcp_servers:") < rewritten.indexOf("..."), `mcp_servers must precede YAML doc end:\n${rewritten}`);

    // 2. parseHermesMcpServersYaml handles 4-space YAML, enabled: false, URL #fragments, inline maps, tools include/exclude.
    const parsed = parseHermesMcpServersYaml(`
mcp_servers:
    docs:
        url: https://docs.example.com/mcp#v2 # keep fragment
        headers: { Authorization: "Bearer \${env:DOCS_KEY}" }
        tools:
            include:
                - search_docs
            exclude: [delete_docs]
    off_srv:
        command: skip-me
        enabled: false
`);
    assert.equal(parsed.off_srv, undefined);
    assert.equal(parsed.docs?.url, "https://docs.example.com/mcp#v2");
    assert.equal(parsed.docs?.headers?.Authorization, "Bearer ${env:DOCS_KEY}");
    assert.deepEqual(parsed.docs?.includeTools, ["search_docs"]);
    assert.deepEqual(parsed.docs?.excludeTools, ["delete_docs"]);

    // 3. syncBackHermesEphemeralHome persists atomically replaced auth.json, newly created state.db,
    //    and new memories/ directory back into sourceHome without clobbering config.yaml.
    const srcHome = join(root, "real-hermes");
    const tmpHome = join(root, "ephem-hermes");
    mkdirSync(srcHome, { recursive: true });
    mkdirSync(tmpHome, { recursive: true });
    const origYaml = "model:\n  name: gpt-5.4\nmcp_servers:\n  datadog:\n    command: dd-mcp\n";
    writeFileSync(join(srcHome, "config.yaml"), origYaml);
    writeFileSync(join(srcHome, "auth.json"), "{\"access_token\":\"old\"}\n");

    buildHermesEphemeralCodemodeHome(srcHome, tmpHome, cmEntry);
    writeFileSync(join(tmpHome, "codemode-servers.json"), "{}\n");

    // Simulate Hermes atomically replacing auth.json (unlink symlink + write regular file),
    // creating brand-new state.db, and writing a new memories/ directory.
    unlinkSync(join(tmpHome, "auth.json"));
    writeFileSync(join(tmpHome, "auth.json"), "{\"access_token\":\"rotated-new\"}\n");
    writeFileSync(join(tmpHome, "state.db"), "sqlite-bytes");
    mkdirSync(join(tmpHome, "memories"), { recursive: true });
    writeFileSync(join(tmpHome, "memories", "note.md"), "remember this\n");

    syncBackHermesEphemeralHome(tmpHome, srcHome);
    assert.equal(readFileSync(join(srcHome, "auth.json"), "utf8"), "{\"access_token\":\"rotated-new\"}\n");
    assert.equal(readFileSync(join(srcHome, "state.db"), "utf8"), "sqlite-bytes");
    assert.equal(readFileSync(join(srcHome, "memories", "note.md"), "utf8"), "remember this\n");
    assert.equal(readFileSync(join(srcHome, "config.yaml"), "utf8"), origYaml, "unmodified config.yaml must stay byte-identical");
    assert.equal(existsSync(join(srcHome, "codemode-servers.json")), false, "ephemeral codemode-servers.json must NOT leak into sourceHome");

    // 4. In-session `hermes mcp add` edits to ephemeral config.yaml ARE persisted back
    //    while preserving original pre-wrap servers (`datadog`) and stripping `caveman-codemode`.
    const addedServerYaml = readFileSync(join(tmpHome, "config.yaml"), "utf8") +
      "  linear:\n    url: https://linear.example/mcp\n";
    writeFileSync(join(tmpHome, "config.yaml"), addedServerYaml);
    syncBackHermesEphemeralHome(tmpHome, srcHome);
    const mergedYaml = readFileSync(join(srcHome, "config.yaml"), "utf8");
    assert.match(mergedYaml, /datadog:/, "original pre-wrap server must be preserved");
    assert.match(mergedYaml, /linear:/, "newly added server from hermes mcp add must persist");
    assert.doesNotMatch(mergedYaml, /caveman-codemode:/, "ephemeral caveman-codemode must never leak back");
    assert.equal(existsSync(join(srcHome, ".caveman-codemode-baseline.yaml")), false, "baseline sidecar must not leak");

    // Sanity check pure merge helper with trailing YAML doc end marker.
    const mergedDocEnd = mergeHermesConfigEdits(
      "model:\n  name: gpt-6\nmcp_servers:\n  caveman-codemode:\n    command: x\n  sentry:\n    command: sentry-mcp\n...\n",
      "model:\n  name: gpt-5.4\nmcp_servers:\n  datadog:\n    command: dd-mcp\n...\n",
    );
    assert.match(mergedDocEnd, /name: gpt-6/);
    assert.match(mergedDocEnd, /datadog:/);
    assert.match(mergedDocEnd, /sentry:/);
    assert.doesNotMatch(mergedDocEnd, /caveman-codemode:/);
    assert.ok(mergedDocEnd.indexOf("mcp_servers:") < mergedDocEnd.indexOf("..."));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("OpenCode/Kilo own-server scoping, JSONC parsing, Gemini/Qwen mcp.allowed, and Codex dotted TOML", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-multi-cm-"));
  const home = join(root, "home");
  const ws = join(root, "ws");
  const cmEntry = { command: "/bin/caveman-mcp", args: ["codemode"] };
  try {
    mkdirSync(join(home, ".config", "opencode"), { recursive: true });
    mkdirSync(join(home, ".config", "kilo"), { recursive: true });
    mkdirSync(ws, { recursive: true });

    // JSONC with comments + trailing commas in OpenCode and Kilo configs.
    writeFileSync(
      join(home, ".config", "opencode", "opencode.jsonc"),
      `{
        // OpenCode MCP config
        "mcp": {
          "oc_native": { "type": "local", "command": ["oc-mcp"], "enabled": true, },
        },
      }`,
    );
    writeFileSync(
      join(home, ".config", "kilo", "kilo.jsonc"),
      `{ /* Kilo config */ "mcp": { "kilo_srv": { "type": "remote", "url": "https://kilo.invalid/mcp", }, }, }`,
    );
    // Foreign Claude server must NOT appear in OpenCode's own server list!
    writeFileSync(join(home, ".claude.json"), JSON.stringify({ mcpServers: { claude_only: { command: "claude-mcp" } } }));

    const ocOwn = discoverOpenCodeOwnServerNames(ws, home, {});
    assert.deepEqual(ocOwn, ["oc_native"]);
    const kiloOwn = discoverKiloOwnServerNames(ws, home, {});
    assert.deepEqual(kiloOwn, ["kilo_srv"]);

    const ocOverlay = JSON.parse(applyOpenCodeCodemodeOverlay(undefined, ocOwn, cmEntry));
    assert.equal(ocOverlay.mcp.oc_native.enabled, false);
    assert.equal(ocOverlay.mcp.claude_only, undefined, "foreign Claude servers must never be injected into OpenCode overlay");

    const kiloOverlay = JSON.parse(applyKiloCodemodeOverlay(undefined, cmEntry, kiloOwn));
    assert.equal(kiloOverlay.mcp.kilo_srv.enabled, false);
    assert.equal(kiloOverlay.mcp["caveman-codemode"].enabled, true);

    // Gemini and Qwen overlays emit mcp.allowed = ["caveman-codemode"] so additive merges don't double-load bridged servers.
    const geminiOut = JSON.parse(readFileSync(writeGeminiCodemodeSettingsOverlay(root, "", cmEntry), "utf8"));
    assert.deepEqual(geminiOut.mcp.allowed, ["caveman-codemode"]);
    const qwenPath = join(root, "qwen-sys.json");
    writeFileSync(qwenPath, JSON.stringify({ mcp: { excluded: ["caveman"] } }));
    patchEphemeralQwenCodemode(qwenPath, cmEntry);
    const qwenOut = JSON.parse(readFileSync(qwenPath, "utf8"));
    assert.deepEqual(qwenOut.mcp.allowed, ["caveman-codemode"]);
    assert.equal(qwenOut.mcp.excluded, undefined);

    // Codex TOML quoted dotted server names, bearer_token_env_var, env_http_headers, multiline args, enabled = false.
    const codexParsed = parseCodexMcpServersToml(`
[mcp_servers."foo.bar"]
url = "https://codex.invalid/mcp"
bearer_token_env_var = "API_TOK"
env_http_headers = { "X-Org" = "ORG_ID" }

[mcp_servers.multi]
command = "node"
args = [
  "a.js",
  "--stdio"
]
env_vars = ["HOME"]

[mcp_servers.off]
command = "skip"
enabled = false
`);
    assert.equal(codexParsed.off, undefined);
    assert.equal(codexParsed["foo.bar"]?.headers?.Authorization, "Bearer ${API_TOK}");
    assert.equal(codexParsed["foo.bar"]?.headers?.["X-Org"], "${ORG_ID}");
    assert.deepEqual(codexParsed.multi?.args, ["a.js", "--stdio"]);
    assert.equal(codexParsed.multi?.env?.HOME, "${HOME}");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("stripExternalCodexMcpTables drops mcp_servers sections while preserving model config", () => {
  const input = `model = "gpt-5"

[mcp_servers.github]
command = "github-mcp"
args = ["stdio"]

[mcp_servers.github.env]
GITHUB_TOKEN = "abc"

[features]
shell = true
`;
  const parsed = parseCodexMcpServersToml(input);
  assert.equal(parsed.github?.command, "github-mcp");
  assert.equal(parsed.github?.env?.GITHUB_TOKEN, "abc");

  const stripped = stripExternalCodexMcpTables(input);
  assert.match(stripped, /model = "gpt-5"/);
  assert.match(stripped, /\[features\]/);
  assert.doesNotMatch(stripped, /mcp_servers/);
});

test("caveman codemode forwards arguments to caveman-mcp codemode", async () => {
  if (process.platform === "win32") return;
  const isolated = isolatedCliEnv();
  const stubPath = join(isolated.home, "stub-caveman-mcp.sh");
  try {
    writeFileSync(
      stubPath,
      `#!/bin/sh
if [ "$1" = "version" ]; then printf '%s\\n' '{"version":"0.5.0","capabilities":["mcp_recovery","codemode"]}'; exit 0; fi
echo "ARGS:$*"
`,
      { mode: 0o755 },
    );
    chmodSync(stubPath, 0o755);
    const out = await runCli(["codemode", "--no-builtins", "--workspace", "/tmp/demo"], {
      env: { ...isolated.env, CAVEMAN_MCP_BIN: stubPath },
    });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /ARGS:codemode --no-builtins --workspace \/tmp\/demo/);

    // --tools-only forwards to `caveman-mcp tools` (companion server for native codemodes)
    const toolsOut = await runCli(["codemode", "--tools-only", "--workspace", "/tmp/demo"], {
      env: { ...isolated.env, CAVEMAN_MCP_BIN: stubPath },
    });
    assert.equal(toolsOut.code, 0, toolsOut.stderr);
    assert.match(toolsOut.stdout, /ARGS:tools --workspace \/tmp\/demo/);
  } finally {
    isolated.cleanup();
  }
});

test("caveman codemode and wrap --codemode require a native caveman-mcp with codemode capability", async () => {
  if (process.platform === "win32") return;
  const isolated = isolatedCliEnv();
  const binDir = join(isolated.home, "bin");
  mkdirSync(binDir, { recursive: true });
  const agentOut = join(isolated.home, "claude-captured.json");
  const claudeStub = join(binDir, "claude");
  const npxStub = join(binDir, "npx");
  const staleMcpStub = join(binDir, "stale-caveman-mcp");
  const currentMcpStub = join(binDir, "current-caveman-mcp");
  try {
    writeFileSync(
      claudeStub,
      `#!/bin/sh
mcp_cfg=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--mcp-config" ]; then mcp_cfg="$a"; fi
  prev="$a"
done
if [ -n "$mcp_cfg" ] && [ -f "$mcp_cfg" ]; then cp "$mcp_cfg" "${agentOut}"; else printf '%s' "NO_CODEMODE_MCP" > "${agentOut}"; fi
`,
      { mode: 0o755 },
    );
    writeFileSync(npxStub, `#!/bin/sh\nexit 0\n`, { mode: 0o755 });
    writeFileSync(
      staleMcpStub,
      `#!/bin/sh\nif [ "$1" = "version" ]; then printf '%s\\n' '{"version":"0.4.0","capabilities":["mcp_recovery"]}'; exit 0; fi\nexit 2\n`,
      { mode: 0o755 },
    );
    writeFileSync(
      currentMcpStub,
      `#!/bin/sh\nif [ "$1" = "version" ]; then printf '%s\\n' '{"version":"0.5.0","capabilities":["mcp_recovery","codemode"]}'; exit 0; fi\nexit 0\n`,
      { mode: 0o755 },
    );

    // Configure proxy=false in CAVEMAN_HOME so this test exercises the wrapped
    // Claude overlay directly without spawning a local proxy listener.
    const caveHome = isolated.env.CAVEMAN_HOME || join(isolated.home, ".caveman");
    mkdirSync(caveHome, { recursive: true });
    writeFileSync(
      join(caveHome, "cloud.json"),
      JSON.stringify({ wrap: { proxy: false } }, null, 2),
    );

    // 1. Empty Caveman home with only npx available (no native caveman-mcp):
    //    must NOT fall back to the unscoped `npx -y caveman-mcp` npm placeholder.
    const missingCmd = await runCli(["codemode"], {
      env: { ...isolated.env, PATH: `${binDir}:/usr/bin:/bin`, CAVEMAN_MCP_BIN: "" },
    });
    assert.notEqual(missingCmd.code, 0);
    assert.match(missingCmd.stderr, /caveman setup --install/);

    const missingWrap = await runCli(["wrap", "--codemode", "claude"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: "",
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
      },
    });
    assert.equal(missingWrap.code, 0, missingWrap.stderr);
    assert.match(missingWrap.stderr, /caveman-mcp not found.*caveman setup --install/);
    assert.equal(readFileSync(agentOut, "utf8"), "NO_CODEMODE_MCP");

    // 2. Stale local caveman-mcp lacking "codemode" capability must fail clearly on both entry points.
    const staleCmd = await runCli(["codemode"], {
      env: { ...isolated.env, PATH: `${binDir}:/usr/bin:/bin`, CAVEMAN_MCP_BIN: staleMcpStub },
    });
    assert.notEqual(staleCmd.code, 0);
    assert.match(staleCmd.stderr, /lacks Code Mode support/);

    const staleRes = await runCli(["wrap", "--codemode", "claude"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: staleMcpStub,
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
      },
    });
    assert.equal(staleRes.code, 0, staleRes.stderr);
    assert.match(staleRes.stderr, /lacks Code Mode support/);
    assert.equal(readFileSync(agentOut, "utf8"), "NO_CODEMODE_MCP");

    // 3. Current native caveman-mcp advertising "codemode" registers the overlay cleanly.
    const okRes = await runCli(["wrap", "--codemode", "claude"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: currentMcpStub,
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
      },
    });
    assert.equal(okRes.code, 0, okRes.stderr);
    const okCfg = JSON.parse(readFileSync(agentOut, "utf8"));
    const cm = okCfg.mcpServers?.["caveman-codemode"];
    assert.ok(cm, `expected caveman-codemode in .mcp.json: ${JSON.stringify(okCfg)}`);
    assert.equal(cm.command, currentMcpStub);
    assert.equal(cm.args[0], "codemode");
  } finally {
    isolated.cleanup();
  }
});

test("OpenCode V2 Code Mode discovers mcp.servers, masks with disabled: true, preserves V1, and fails closed on invalid V2 mcp (#1564)", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-oc-v2-cm-"));
  const home = join(root, "home");
  const ws = join(root, "ws");
  const cmEntry = { command: "/bin/caveman-mcp", args: ["codemode", "--config", "/tmp/c.json"] };
  try {
    mkdirSync(join(home, ".config", "opencode"), { recursive: true });
    mkdirSync(ws, { recursive: true });

    const v2Path = join(home, ".config", "opencode", "opencode.json");
    const v2Original = JSON.stringify({
      providers: { openai: { settings: { baseURL: "http://127.0.0.1:8787/v1" } } },
      mcp: {
        servers: {
          docs: { type: "remote", url: "https://docs.example.invalid/mcp", disabled: false },
          local_tool: { type: "local", command: ["tool-mcp", "--stdio"] },
          already_off: { type: "remote", url: "https://off.example.invalid/mcp", disabled: true },
          caveman: { type: "local", command: ["caveman-mcp"] },
        },
      },
    }, null, 2);
    writeFileSync(v2Path, v2Original);

    // 1. Discovery reads V2 mcp.servers, skips disabled: true and self.
    const discovered = discoverCodemodeServers(ws, home, {});
    assert.equal(discovered.docs?.url, "https://docs.example.invalid/mcp");
    assert.equal(discovered.local_tool?.command, "tool-mcp");
    assert.deepEqual(discovered.local_tool?.args, ["--stdio"]);
    assert.equal(discovered.already_off, undefined);
    assert.equal(discovered.caveman, undefined);

    const own = discoverOpenCodeOwnServerNames(ws, home, {});
    assert.deepEqual(own.sort(), ["docs", "local_tool"]);

    // 2. Schema detection recognizes V2 from on-disk config and from host major >= 2.
    assert.equal(detectOpenCodeConfigSchema(ws, home, {}), "v2");
    assert.equal(detectOpenCodeConfigSchema(ws, home, {}, false, 2), "v2");
    assert.equal(detectOpenCodeConfigSchema(ws, home, {}, false, 1), "v1");

    // 3. V2 overlay writes mcp.servers.<name>.disabled = true (never V1 enabled: false).
    const v2Overlay = JSON.parse(applyOpenCodeCodemodeOverlay(
      JSON.stringify({ providers: { openai: { settings: { baseURL: "http://127.0.0.1:8787/v1" } } } }),
      own,
      cmEntry,
      { schema: "v2" },
    ));
    assert.equal(v2Overlay.mcp.docs, undefined, "V2 overlay must not emit V1 flat mcp.docs");
    assert.equal(v2Overlay.mcp.servers.docs.disabled, true);
    assert.equal(v2Overlay.mcp.servers.docs.enabled, undefined);
    assert.equal(v2Overlay.mcp.servers.local_tool.disabled, true);
    assert.deepEqual(v2Overlay.mcp.servers["caveman-codemode"], {
      type: "local",
      command: [cmEntry.command, ...cmEntry.args],
      disabled: false,
    });

    // 4. V2 auto-detection from rawConfigJson when schema option is omitted.
    const autoV2 = JSON.parse(applyOpenCodeCodemodeOverlay(
      JSON.stringify({ mcp: { servers: { keep: { type: "local", command: ["keep"] } } } }),
      ["docs"],
      cmEntry,
    ));
    assert.equal(autoV2.mcp.servers.docs.disabled, true);
    assert.equal(autoV2.mcp.servers.keep.type, "local");
    assert.equal(autoV2.mcp.servers["caveman-codemode"].disabled, false);

    // 5. V1 path remains preserved when schema is v1.
    const v1Overlay = JSON.parse(applyOpenCodeCodemodeOverlay(
      JSON.stringify({ provider: { openai: { options: { baseURL: "http://127.0.0.1:8787/v1" } } } }),
      ["docs"],
      cmEntry,
      { schema: "v1" },
    ));
    assert.equal(v1Overlay.mcp.docs.enabled, false);
    assert.equal(v1Overlay.mcp.servers, undefined);
    assert.equal(v1Overlay.mcp["caveman-codemode"].enabled, true);

    // 6. Malformed V2 mcp.servers throws so caller reports limitation instead of pretending active.
    assert.throws(
      () => applyOpenCodeCodemodeOverlay(JSON.stringify({ mcp: { servers: ["bad"] } }), ["docs"], cmEntry, { schema: "v2" }),
      /mcp\.servers field is not a JSON object/,
    );
    assert.throws(
      () => applyOpenCodeCodemodeOverlay("not-json{", ["docs"], cmEntry, { schema: "v2" }),
      /OpenCode V2 overlay cannot disable bridged MCP servers/,
    );

    // 7. On-disk config stays byte-identical.
    assert.equal(readFileSync(v2Path, "utf8"), v2Original);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Hermes Code Mode preserves flow-style and multi-line mcp_servers and fails closed on unmergeable YAML (#1563)", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-hermes-flow-cm-"));
  const cmEntry = { command: "/bin/caveman-mcp", args: ["codemode", "--config", "/tmp/c.json"] };
  try {
    // 1. Exact reproduction from #1563: single-line flow-style mcp_servers preserved on model edit.
    const originalFlow = "model: old\nmcp_servers: {existing: {url: https://example.invalid/mcp}}\n";
    const editedFlow = "model: new\nmcp_servers:\n  caveman-codemode:\n    command: caveman-mcp\n";
    const mergedFlow = mergeHermesConfigEdits(editedFlow, originalFlow);
    assert.match(mergedFlow, /^model: new/m);
    assert.match(mergedFlow, /existing:\s*\{url: https:\/\/example\.invalid\/mcp\}/);
    assert.doesNotMatch(mergedFlow, /caveman-codemode/);

    // 2. Discovery parses both single-line and multi-line flow-style mcp_servers + block-style inline flow entries.
    const discoveredFlow = parseHermesMcpServersYaml(originalFlow);
    assert.equal(discoveredFlow.existing?.url, "https://example.invalid/mcp");

    const multiLineFlow = [
      "model: old",
      "mcp_servers: {",
      '  "docs-srv": { url: "https://docs.example.invalid/mcp#v2", headers: { Authorization: "Bearer tok" } },',
      '  local: { command: "node", args: ["srv.js", "--stdio"] },',
      "  wrapped: {",
      "    command: npx,",
      "    args: [",
      "      hello",
      "        world,",
      "      'single",
      "        quoted',",
      '      "double',
      '        quoted",',
      '      "line\\',
      '        cont"',
      "    ]",
      "  },",
      "  disabled_srv: { command: skip, enabled: false },",
      "  disabled_py: { command: skip_py, enabled: False },",
      "  disabled_upper: { command: skip_upper, enabled: FALSE }",
      "}",
      "other: keep",
      "",
    ].join("\n");
    const discoveredMulti = parseHermesMcpServersYaml(multiLineFlow);
    assert.equal(discoveredMulti["docs-srv"]?.url, "https://docs.example.invalid/mcp#v2");
    assert.equal(discoveredMulti["docs-srv"]?.headers?.Authorization, "Bearer tok");
    assert.equal(discoveredMulti.local?.command, "node");
    assert.deepEqual(discoveredMulti.local?.args, ["srv.js", "--stdio"]);
    assert.deepEqual(discoveredMulti.wrapped?.args, ["hello world", "single quoted", "double quoted", "linecont"]);
    assert.equal(discoveredMulti.disabled_srv, undefined);
    assert.equal(discoveredMulti.disabled_py, undefined);
    assert.equal(discoveredMulti.disabled_upper, undefined);

    // 3. rewriteHermesCodemodeYaml strips multi-line flow map completely (no stray column-0 `}`).
    const rewrittenMulti = rewriteHermesCodemodeYaml(multiLineFlow, cmEntry);
    assert.match(rewrittenMulti, /^other: keep$/m);
    assert.doesNotMatch(rewrittenMulti, /^\}\s*$/m, `stray closing brace leaked:\n${rewrittenMulti}`);
    assert.match(rewrittenMulti, /caveman-codemode:/);

    // 4. Flow-style original + in-session `hermes mcp add` block entry keeps BOTH servers and drops caveman-codemode.
    const editedWithNew = "model: new\nmcp_servers:\n  caveman-codemode:\n    command: caveman-mcp\n  linear:\n    url: https://linear.example.invalid/mcp\n";
    const mergedBoth = mergeHermesConfigEdits(editedWithNew, originalFlow);
    assert.match(mergedBoth, /^model: new$/m);
    assert.match(mergedBoth, /existing: \{url: https:\/\/example\.invalid\/mcp\}/);
    assert.match(mergedBoth, /linear:/);
    assert.doesNotMatch(mergedBoth, /caveman-codemode/);
    const reparsed = parseHermesMcpServersYaml(mergedBoth);
    assert.equal(reparsed.existing?.url, "https://example.invalid/mcp");
    assert.equal(reparsed.linear?.url, "https://linear.example.invalid/mcp");

    // 4b. Mixed indentation (flow original + 4-space block edit, or 2-space + 4-space) normalizes sibling indent.
    const editedFourSpace = [
      "model: new",
      "mcp_servers:",
      "    caveman-codemode:",
      "        command: caveman-mcp",
      "    linear:",
      "        url: https://linear.example.invalid/mcp",
      "",
    ].join("\n");
    const mergedFourSpace = mergeHermesConfigEdits(editedFourSpace, originalFlow);
    assert.match(mergedFourSpace, /^    existing: \{url: https:\/\/example\.invalid\/mcp\}$/m);
    assert.match(mergedFourSpace, /^    linear:$/m);
    assert.match(mergedFourSpace, /^        url: https:\/\/linear\.example\.invalid\/mcp$/m);
    assert.doesNotMatch(mergedFourSpace, /^  existing:/m, `mixed sibling indent emitted:\n${mergedFourSpace}`);
    const reparsedFour = parseHermesMcpServersYaml(mergedFourSpace);
    assert.equal(reparsedFour.existing?.url, "https://example.invalid/mcp");
    assert.equal(reparsedFour.linear?.url, "https://linear.example.invalid/mcp");

    const origTwoSpaceBlock = "model: old\nmcp_servers:\n  existing:\n    url: https://example.invalid/mcp\n";
    const mergedBlockIndent = mergeHermesConfigEdits(editedFourSpace, origTwoSpaceBlock);
    assert.match(mergedBlockIndent, /^    existing:$/m);
    assert.match(mergedBlockIndent, /^      url: https:\/\/example\.invalid\/mcp$/m);
    assert.match(mergedBlockIndent, /^    linear:$/m);
    const reparsedBlockIndent = parseHermesMcpServersYaml(mergedBlockIndent);
    assert.equal(reparsedBlockIndent.existing?.url, "https://example.invalid/mcp");
    assert.equal(reparsedBlockIndent.linear?.url, "https://linear.example.invalid/mcp");

    // 5. Unsupported / unparseable YAML shapes fail closed: merge throws, syncBack leaves original untouched and warns.
    const anchorOriginal = "model: old\nmcp_servers: &shared\n  existing:\n    url: https://example.invalid/mcp\n";
    assert.throws(
      () => mergeHermesConfigEdits(editedFlow, anchorOriginal),
      /cannot preserve original Hermes mcp_servers/,
    );
    assert.throws(
      () => mergeHermesConfigEdits(editedFlow, "model: old\nmcp_servers: {unclosed: {url: https://example.invalid}\n"),
      /cannot preserve original Hermes mcp_servers/,
    );

    const srcHome = join(root, "real-hermes");
    const tmpHome = join(root, "ephem-hermes");
    mkdirSync(srcHome, { recursive: true });
    mkdirSync(tmpHome, { recursive: true });
    writeFileSync(join(srcHome, "config.yaml"), anchorOriginal);
    buildHermesEphemeralCodemodeHome(srcHome, tmpHome, cmEntry);
    writeFileSync(join(tmpHome, "config.yaml"), editedFlow);

    let warned = "";
    const origWrite = process.stderr.write.bind(process.stderr);
    process.stderr.write = (chunk) => {
      warned += String(chunk);
      return true;
    };
    try {
      syncBackHermesEphemeralHome(tmpHome, srcHome);
    } finally {
      process.stderr.write = origWrite;
    }
    assert.equal(readFileSync(join(srcHome, "config.yaml"), "utf8"), anchorOriginal, "unmergeable original config.yaml must stay untouched");
    assert.match(warned, /Hermes config\.yaml sync-back skipped.*original config\.yaml left untouched/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("caveman wrap --codemode opencode uses V2 mcp.servers.disabled on OpenCode 2.x and reports limitation on malformed V2 mcp (#1564)", async () => {
  if (process.platform === "win32") return;
  const isolated = isolatedCliEnv();
  const binDir = join(isolated.home, "bin");
  mkdirSync(binDir, { recursive: true });
  const ocConfigDir = join(isolated.home, ".config", "opencode");
  mkdirSync(ocConfigDir, { recursive: true });
  const ocCapture = join(isolated.home, "opencode-captured.json");
  const ocStub = join(binDir, "opencode");
  const mcpStub = join(binDir, "caveman-mcp");
  try {
    writeFileSync(
      join(ocConfigDir, "opencode.json"),
      JSON.stringify({
        providers: { openai: { settings: { baseURL: "https://api.openai.com/v1" } } },
        mcp: {
          servers: {
            docs: { type: "remote", url: "https://docs.example.invalid/mcp", disabled: false },
          },
        },
      }, null, 2),
    );
    writeFileSync(
      ocStub,
      `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.0.22"; exit 0; fi
"${process.execPath}" -e 'const fs=require("fs"); fs.writeFileSync(process.argv[1], JSON.stringify({args: process.argv.slice(2), env: process.env.OPENCODE_CONFIG_CONTENT || ""}));' "${ocCapture}" "$@"
`,
      { mode: 0o755 },
    );
    writeFileSync(
      mcpStub,
      `#!/bin/sh\nif [ "$1" = "version" ]; then printf '%s\\n' '{"version":"0.5.0","capabilities":["mcp_recovery","codemode"]}'; exit 0; fi\nexit 0\n`,
      { mode: 0o755 },
    );
    const caveHome = isolated.env.CAVEMAN_HOME || join(isolated.home, ".caveman");
    mkdirSync(caveHome, { recursive: true });
    writeFileSync(join(caveHome, "cloud.json"), JSON.stringify({ wrap: { proxy: false } }, null, 2));

    const res = await runCli(["wrap", "--codemode", "opencode", "run", "hello"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: mcpStub,
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
      },
    });
    assert.equal(res.code, 0, res.stderr);
    assert.match(res.stderr, /codemode active \(opencode\)/);
    const captured = JSON.parse(readFileSync(ocCapture, "utf8"));
    assert.ok(captured.args.includes("--standalone"), `OpenCode 2.x wrap must inject --standalone: ${JSON.stringify(captured.args)}`);
    const overlay = JSON.parse(captured.env);
    assert.equal(overlay.mcp?.docs, undefined, "OpenCode 2.x overlay must not write V1 flat mcp.docs");
    assert.equal(overlay.mcp?.servers?.docs?.disabled, true);
    assert.equal(overlay.mcp?.servers?.["caveman-codemode"]?.disabled, false);

    // Malformed V2 mcp.servers in user OPENCODE_CONFIG_CONTENT must warn and NOT claim codemode active.
    const badRes = await runCli(["wrap", "--codemode", "opencode", "run", "hello"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: mcpStub,
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
        OPENCODE_CONFIG_CONTENT: JSON.stringify({ mcp: { servers: ["not-an-object"] } }),
      },
    });
    assert.equal(badRes.code, 0, badRes.stderr);
    assert.match(badRes.stderr, /mcp\.servers field is not a JSON object.*skipping OpenCode Code Mode overlay/);
    assert.doesNotMatch(badRes.stderr, /codemode active \(opencode\)/);
  } finally {
    isolated.cleanup();
  }
});
