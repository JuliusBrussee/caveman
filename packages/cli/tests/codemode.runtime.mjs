import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync, chmodSync, existsSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  applyKiloCodemodeOverlay,
  applyOpenCodeCodemodeOverlay,
  buildHermesEphemeralCodemodeHome,
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
    writeFileSync(stubPath, `#!/bin/sh\necho "ARGS:$*"\n`, { mode: 0o755 });
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

test("caveman wrap --codemode preserves npx prefix args and refuses stale caveman-mcp without codemode capability", async () => {
  if (process.platform === "win32") return;
  const isolated = isolatedCliEnv();
  const binDir = join(isolated.home, "bin");
  mkdirSync(binDir, { recursive: true });
  const agentOut = join(isolated.home, "claude-captured.json");
  const claudeStub = join(binDir, "claude");
  const npxStub = join(binDir, "npx");
  const staleMcpStub = join(binDir, "stale-caveman-mcp");
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

    // Configure proxy=false in CAVEMAN_HOME so this test exercises the wrapped
    // Claude overlay directly without spawning a local proxy listener.
    const caveHome = isolated.env.CAVEMAN_HOME || join(isolated.home, ".caveman");
    mkdirSync(caveHome, { recursive: true });
    writeFileSync(
      join(caveHome, "cloud.json"),
      JSON.stringify({ wrap: { proxy: false } }, null, 2),
    );

    // 1. Only npx available (no caveman-mcp on PATH): Claude overlay must carry `-y caveman-mcp codemode ...`
    //    (not `npx codemode ...`).
    const npxRes = await runCli(["wrap", "--codemode", "claude"], {
      env: {
        ...isolated.env,
        PATH: `${binDir}:/usr/bin:/bin`,
        CAVEMAN_MCP_BIN: "",
        CAVE_GATEWAY_URL: "http://127.0.0.1:18802",
      },
    });
    assert.equal(npxRes.code, 0, npxRes.stderr);
    const npxCfg = JSON.parse(readFileSync(agentOut, "utf8"));
    const cm = npxCfg.mcpServers?.["caveman-codemode"];
    assert.ok(cm, `expected caveman-codemode in .mcp.json: ${JSON.stringify(npxCfg)}`);
    assert.ok(cm.command.endsWith("/npx") || cm.command === "npx", `expected npx command, got ${cm.command}`);
    assert.deepEqual(cm.args.slice(0, 3), ["-y", "caveman-mcp", "codemode"], `npx prefix args dropped: ${JSON.stringify(cm.args)}`);

    // 2. Stale local caveman-mcp lacking "codemode" capability must NOT register broken codemode overlay.
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
  } finally {
    isolated.cleanup();
  }
});
