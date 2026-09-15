import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { createServer as createNetServer, connect } from "node:net";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  writeFileSync,
  symlinkSync,
  createWriteStream,
  copyFileSync,
  chmodSync,
  realpathSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";
// Opt-in native-host proof. Supply all three binaries; never discover user
// accounts or connect to a hosted model. macOS sandbox denies external network
// and user-home reads; all provider credentials and workspaces are synthetic.
if (process.platform !== "darwin")
  throw new Error(
    "This native probe currently requires macOS sandbox-exec; it does not certify Windows.",
  );
const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const required = (name) => {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name} to an explicit binary path`);
  return resolve(value);
};
const native = realpathSync(required("CAVEMAN_NATIVE_CODEX_BIN"));
const inputProxy = required("CAVEMAN_PROXY_BIN");
const inputMcp = required("CAVEMAN_MCP_BIN");
if ([native, process.execPath, repo].some((value) => /[\"'\\\r\n]/.test(value)))
  throw new Error(
    "Probe binary and repository paths must not contain quotes, backslashes or newlines",
  );
const custom = process.argv.includes("--custom-home");
const explicitDb = process.argv.includes("--explicit-db");
const root = mkdtempSync("/tmp/cnc-");

const home = join(root, "home"),
  codexHome = join(root, "codex-home"),
  caveHome = custom ? join(root, "cave-home") : join(home, ".caveman"),
  cwd = join(root, "cwd"),
  bin = join(root, "bin");
for (const p of [
  home,
  codexHome,
  caveHome,
  cwd,
  bin,
  join(root, "tmp"),
  join(home, ".caveman-cloud"),
])
  mkdirSync(p, { recursive: true });
const recoveryDb = explicitDb
  ? join(root, "explicit recovery.db")
  : join(caveHome, "ccr.db");
const proxyBin = join(bin, "caveman-proxy"),
  mcpBin = join(bin, "caveman-mcp");
copyFileSync(inputProxy, proxyBin);
chmodSync(proxyBin, 0o755);
copyFileSync(inputMcp, mcpBin);
chmodSync(mcpBin, 0o755);
symlinkSync(process.execPath, join(bin, "node"));
const profile = join(root, "sandbox.sb");
writeFileSync(
  profile,
  `(version 1)\n(allow default)\n(deny network*)\n(allow network-outbound (remote ip "localhost:*"))\n(deny file-write*)\n(allow file-write* (subpath "${root}") (subpath "/private${root}") (literal "/dev/null"))\n(deny file-read-data (subpath "/Users") (subpath "/private/tmp") (subpath "/private/var/folders") (subpath "/Volumes"))\n(allow file-read-data (subpath "${root}") (subpath "/private${root}") (literal "${process.execPath}") (literal "${native}") (subpath "${repo}/packages/cli/dist"))\n`,
);
writeFileSync(
  join(bin, "codex"),
  `#!/bin/sh\nexec /usr/bin/sandbox-exec -f '${profile}' '${native}' "$@"\n`,
  { mode: 0o755 },
);
const sha = (b) => createHash("sha256").update(b).digest("hex");
const marker = "CAVEMAN_NATIVE_CODEX_PAYLOAD",
  sentinel = "CAVEMAN_NATIVE_CODEX_RECOVERY_OK";
const prompt = Array.from(
  { length: 100 },
  (_, i) =>
    `${marker} section ${i % 7}: preserve this repeated operator context.`,
).join("\n");
writeFileSync(join(root, "prompt.txt"), prompt);
const result = {
  root,
  customCavemanHome: custom,
  explicitRecoveryDatabase: explicitDb,
  hostVersion: null,
  hostBinary: native,
  hostSha256: sha(readFileSync(native)),
  proxySha256: sha(readFileSync(proxyBin)),
  mcpSha256: sha(readFileSync(mcpBin)),
  cliSha256: sha(readFileSync(join(repo, "packages/cli/dist/index.js"))),
  evidence: [],
  limitation:
    "Actual native Codex CLI, real proxy and real stdio MCP, local synthetic Responses SSE provider. No hosted inference, Windows runtime, or hook trust/bypass certification.",
};
let cli,
  proxy,
  proxyExit,
  stage = "initialize",
  selectedHandle,
  originalUser,
  toolName,
  toolNamespace;
const requests = [],
  errors = [];
const persist = () => {
  writeFileSync(join(root, "requests.json"), JSON.stringify(requests, null, 2));
  writeFileSync(join(root, "report.json"), JSON.stringify(result, null, 2));
};
const sendSSE = (res, items) => {
  const id = "resp_cnc_" + requests.length;
  let sequence_number = 0;
  res.writeHead(200, {
    "content-type": "text/event-stream",
    "cache-control": "no-cache",
  });
  const event = (type, data) =>
    res.write(
      `event: ${type}\ndata: ${JSON.stringify({ type, sequence_number: sequence_number++, ...data })}\n\n`,
    );
  event("response.created", {
    response: { id, object: "response", status: "in_progress", output: [] },
  });
  for (let index = 0; index < items.length; index++) {
    const item = items[index];
    event("response.output_item.added", {
      output_index: index,
      item: item.type === "function_call" ? { ...item, arguments: "" } : item,
    });
    if (item.type === "function_call") {
      event("response.function_call_arguments.delta", {
        item_id: item.id,
        output_index: index,
        delta: item.arguments,
      });
      event("response.function_call_arguments.done", {
        item_id: item.id,
        output_index: index,
        arguments: item.arguments,
      });
    } else {
      event("response.content_part.added", {
        item_id: item.id,
        output_index: index,
        content_index: 0,
        part: { type: "output_text", text: "", annotations: [] },
      });
      event("response.output_text.delta", {
        item_id: item.id,
        output_index: index,
        content_index: 0,
        delta: item.content[0].text,
      });
      event("response.output_text.done", {
        item_id: item.id,
        output_index: index,
        content_index: 0,
        text: item.content[0].text,
      });
      event("response.content_part.done", {
        item_id: item.id,
        output_index: index,
        content_index: 0,
        part: item.content[0],
      });
    }
    event("response.output_item.done", { output_index: index, item });
  }
  event("response.completed", {
    response: {
      id,
      object: "response",
      status: "completed",
      output: items,
      usage: {
        input_tokens: 4000,
        output_tokens: 20,
        total_tokens: 4020,
        input_tokens_details: { cached_tokens: 0 },
        output_tokens_details: { reasoning_tokens: 0 },
      },
    },
  });
  res.end();
};
const textItem = (text) => ({
  type: "message",
  id: "msg_cnc_" + requests.length,
  role: "assistant",
  status: "completed",
  content: [{ type: "output_text", text, annotations: [] }],
});
const upstream = createServer((req, res) => {
  let raw = "";
  req.on("data", (b) => (raw += b));
  req.on("end", () => {
    try {
      if (req.url !== "/v1/responses") {
        (result.metadataRequests ??= []).push({
          method: req.method,
          path: req.url,
        });
        persist();
        res.writeHead(404, { "content-type": "application/json" });
        res.end('{"error":{"message":"Only local Responses endpoint exists"}}');
        return;
      }
      const body = JSON.parse(raw);
      requests.push({
        method: req.method,
        path: req.url,
        headerNames: Object.keys(req.headers).sort(),
        body,
        rawSha256: sha(raw),
      });
      persist();
      assert.equal(
        req.headers.authorization,
        "Bearer sk-native-codex-synthetic",
      );
      assert.equal(body.stream, true);
      const recovered = body.input.find(
        (i) =>
          i.type === "function_call_output" &&
          i.call_id === "call_cnc_retrieve",
      );
      if (recovered) {
        result.recoveryOutput = recovered.output;
        let output = recovered.output;
        if (typeof output === "string") {
          try {
            output = JSON.parse(output);
          } catch {}
        }
        const strings =
          typeof output === "string"
            ? [output]
            : Array.isArray(output)
              ? output
                  .filter((c) => c.type === "text" || c.type === "input_text")
                  .map((c) => c.text)
              : (output?.content
                  ?.filter((c) => c.type === "text")
                  .map((c) => c.text) ?? []);
        result.recoveryExact = strings.some((s) => s === originalUser);
        if (!result.recoveryExact) {
          result.recoveryOutputStrings = strings;
          sendSSE(res, [textItem("CAVEMAN_NATIVE_CODEX_RECOVERY_FAILED")]);
          persist();
          return;
        }
        result.evidence.push(
          "Native Codex dispatched actual MCP recovery and forwarded the exact original user message in its next real streamed request.",
        );
        sendSSE(res, [textItem(sentinel)]);
      } else {
        assert.equal(requests.length, 1, "unexpected extra initial request");
        const user = body.input.find(
          (i) => i.role === "user" && JSON.stringify(i).includes(marker),
        );
        assert.ok(user, "provider request omitted synthetic user prompt");
        const content =
          typeof user.content === "string"
            ? user.content
            : user.content
                .filter((c) => c.type === "input_text")
                .map((c) => c.text)
                .join("\n");
        selectedHandle = content.match(/<<ccr:(ccr_[^>\s]+)>>/)?.[1];
        assert.ok(
          selectedHandle,
          "native streaming request lacked generated CCR handle",
        );
        const db = new DatabaseSync(recoveryDb, { readOnly: true });
        try {
          const r = db
            .prepare(
              "SELECT original,tokens_before,tokens_after FROM recoveries WHERE handle=?",
            )
            .get(selectedHandle);
          assert.ok(r);
          originalUser = Buffer.from(r.original).toString("utf8");
          assert.ok(originalUser.includes(prompt));
          result.storedRecovery = {
            handle: selectedHandle,
            tokensBefore: r.tokens_before,
            tokensAfter: r.tokens_after,
            originalSha256: sha(originalUser),
          };
          assert.ok(Number(r.tokens_before) > Number(r.tokens_after));
        } finally {
          db.close();
        }
        assert.notEqual(content, originalUser);
        result.handle = selectedHandle;
        const names = body.tools.flatMap((t) =>
          t.type === "namespace"
            ? t.tools.map((inner) => ({ name: inner.name, namespace: t.name }))
            : [{ name: t.name ?? t.function?.name }],
        );
        result.toolNames = names;
        const selectedTool = names.find((t) =>
          t.name?.includes("caveman_retrieve"),
        );
        toolName = selectedTool?.name;
        toolNamespace = selectedTool?.namespace;
        assert.ok(
          toolName,
          "native host did not expose real MCP recovery tool",
        );
        result.evidence.push(
          "Native streamed request reached real proxy, was compressed with a generated CCR handle, and advertised native MCP retrieval.",
        );
        sendSSE(res, [
          {
            type: "function_call",
            id: "fc_cnc_retrieve",
            call_id: "call_cnc_retrieve",
            name: toolName,
            ...(toolNamespace ? { namespace: toolNamespace } : {}),
            arguments: JSON.stringify({ recovery_handle: selectedHandle }),
            status: "completed",
          },
        ]);
      }
      persist();
    } catch (e) {
      errors.push(String(e.stack || e));
      result.providerErrors = errors;
      persist();
      if (!res.headersSent)
        res.writeHead(400, { "content-type": "application/json" });
      res.end('{"error":{"message":"Local proof assertion failed"}}');
    }
  });
});
const listen = (s) =>
  new Promise((resolve, reject) => {
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => resolve(s.address().port));
  });
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  for (
    let n = 0;
    n < 30 && child.exitCode === null && child.signalCode === null;
    n++
  )
    await delay(50);
  if (child.exitCode === null && child.signalCode === null)
    child.kill("SIGKILL");
}
try {
  const upPort = await listen(upstream);
  const probe = createNetServer();
  const port = await listen(probe);
  await new Promise((r) => probe.close(r));
  const config = join(caveHome, "caveman.yaml");
  writeFileSync(
    config,
    `mode: compress\nlisten: 127.0.0.1:${port}\nproviders:\n  openai:\n    base_url: http://127.0.0.1:${upPort}\n  anthropic:\n    base_url: http://127.0.0.1:${upPort}\n  gemini:\n    base_url: http://127.0.0.1:${upPort}\n`,
  );
  writeFileSync(
    join(home, ".caveman-cloud/config.json"),
    JSON.stringify({
      think: { mode: "compress", toon: false, shrink: false },
      execute: {
        mcp: "auto",
        browse_tool: false,
        browse_cli: false,
        delegate: false,
        proxy: true,
      },
    }),
    { mode: 0o600 },
  );
  writeFileSync(
    join(codexHome, "auth.json"),
    JSON.stringify({ OPENAI_API_KEY: "sk-native-codex-synthetic" }),
    { mode: 0o600 },
  );
  writeFileSync(
    join(codexHome, "config.toml"),
    `model = "gpt-5.3-codex"\napproval_policy = "never"\nweb_search = "disabled"\ncheck_for_update_on_startup = false\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n[features]\nshell_tool = false\nshell_snapshot = false\n`,
  );
  const env = {
    HOME: home,
    USERPROFILE: home,
    PATH: `${bin}:/usr/bin:/bin`,
    SHELL: "/bin/sh",
    TMPDIR: join(root, "tmp"),
    CODEX_HOME: codexHome,
    XDG_CONFIG_HOME: join(home, ".config"),
    XDG_CACHE_HOME: join(home, ".cache"),
    XDG_DATA_HOME: join(home, ".local/share"),
    LANG: "en_US.UTF-8",
    LC_ALL: "en_US.UTF-8",
    TERM: "dumb",
    NO_COLOR: "1",
    ...(custom ? { CAVEMAN_HOME: caveHome } : {}),
    ...(explicitDb ? { CAVEMAN_CCR_DB: recoveryDb } : {}),
    CAVEMAN_CONFIG: config,
    CAVEMAN_PROXY_OWNER: "wrap",
    CAVEMAN_RECOVERY: "mcp",
    CAVE_ENGINE_TOON: "0",
    CAVE_SSRF_ALLOWLIST: "127.0.0.1",
    CAVEMAN_TELEMETRY: "0",
    CAVEMAN_OFFLINE: "1",
    CAVEMAN_PROXY_BIN: proxyBin,
    CAVEMAN_MCP_BIN: mcpBin,
    CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`,
    CAVE_NO_KEYCHAIN: "1",
    CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
    OPENAI_API_KEY: "sk-native-codex-synthetic",
  };
  result.environmentVariableNames = Object.keys(env).sort();
  result.proxyPort = port;
  result.providerPort = upPort;
  stage = "native version";
  const version = spawnSync(join(bin, "codex"), ["--version"], {
    env,
    cwd,
    timeout: 10000,
    encoding: "utf8",
  });
  assert.equal(
    version.status,
    0,
    JSON.stringify({
      signal: version.signal,
      error: version.error?.message,
      stderr: version.stderr,
    }),
  );
  result.hostVersion = version.stdout.trim();
  stage = "MCP preflight";
  const mcpPreflight = spawnSync(mcpBin, ["version", "--json"], {
    env,
    cwd,
    timeout: 10000,
    encoding: "utf8",
  });
  result.mcpPreflight = {
    status: mcpPreflight.status,
    stdout: mcpPreflight.stdout,
    stderr: mcpPreflight.stderr,
    error: String(mcpPreflight.error ?? ""),
  };
  assert.equal(mcpPreflight.status, 0, JSON.stringify(result.mcpPreflight));
  stage = "proxy startup";
  proxy = spawn(proxyBin, ["serve"], {
    env,
    cwd,
    stdio: ["ignore", "pipe", "pipe"],
  });
  proxy.stdout.pipe(createWriteStream(join(root, "proxy.stdout.log")));
  proxy.stderr.pipe(createWriteStream(join(root, "proxy.stderr.log")));
  proxy.on("exit", (code, signal) => (proxyExit = { code, signal }));
  let ready = false;
  for (let n = 0; n < 320; n++) {
    ready = await new Promise((r) => {
      const s = connect({ host: "127.0.0.1", port });
      s.once("connect", () => {
        s.destroy();
        r(true);
      });
      s.once("error", () => r(false));
    });
    if (ready) break;
    await delay(25);
  }
  assert.ok(ready);
  await delay(200);
  const status = spawnSync(
    proxyBin,
    ["status", "--json", "--port", String(port)],
    { env, cwd, timeout: 2000, encoding: "utf8" },
  );
  assert.equal(status.status, 0, status.stderr);
  assert.equal(JSON.parse(status.stdout).owner, "wrap");
  stage = "native Codex command";
  const args = [
    join(repo, "packages/cli/dist/index.js"),
    "wrap",
    "codex",
    "exec",
    "--skip-git-repo-check",
    "--ephemeral",
    "--sandbox",
    "read-only",
    "-c",
    'mcp_servers.caveman.tools.caveman_retrieve.approval_mode="approve"',
    "--json",
    "-o",
    join(root, "last-message.txt"),
    prompt,
  ];
  result.command = [
    "node",
    ...args.map((a) => (a === prompt ? "<synthetic prompt.txt>" : a)),
  ];
  let stdout = "",
    stderr = "";
  cli = spawn(process.execPath, args, {
    env,
    cwd,
    stdio: ["ignore", "pipe", "pipe"],
  });
  cli.stdout.on("data", (b) => {
    stdout += b;
    writeFileSync(join(root, "native.stdout.log"), stdout);
  });
  cli.stderr.on("data", (b) => {
    stderr += b;
    writeFileSync(join(root, "native.stderr.log"), stderr);
  });
  const outcome = await new Promise((resolve, reject) => {
    const t = setTimeout(() => {
      cli.kill("SIGTERM");
      reject(new Error("native Codex timed out at 45 seconds"));
    }, 45000);
    cli.once("error", (e) => {
      clearTimeout(t);
      reject(e);
    });
    cli.once("exit", (code, signal) => {
      clearTimeout(t);
      resolve({ code, signal });
    });
  });
  result.nativeExit = outcome;
  assert.equal(outcome.code, 0, stderr);
  assert.equal(errors.length, 0, errors.join("\n"));
  assert.equal(requests.length, 2);
  assert.ok(
    result.recoveryExact,
    "Native MCP did not retrieve the exact user message",
  );
  assert.equal(readFileSync(join(root, "last-message.txt"), "utf8"), sentinel);
  stage = "recorded evidence";
  await delay(300);
  const db = new DatabaseSync(join(caveHome, "caveman.db"), { readOnly: true });
  try {
    result.rows = db
      .prepare(
        "SELECT agent_slug,compression_tokens_before,compression_tokens_after,recovery_handle,raw_request_sha256,transformed_request_sha256,savings_usd FROM requests ORDER BY id",
      )
      .all();
    assert.ok(
      result.rows.some(
        (r) =>
          r.agent_slug === "codex" &&
          r.raw_request_sha256 !== r.transformed_request_sha256,
      ),
    );
  } finally {
    db.close();
  }
  result.success = true;
} catch (e) {
  result.success = false;
  result.failedStage = stage;
  result.error = String(e.stack || e);
  process.exitCode = 1;
} finally {
  await stop(cli);
  await stop(proxy);
  upstream.closeAllConnections();
  await new Promise((r) => upstream.close(r));
  result.proxyExit = proxyExit;
  persist();
  console.log(
    JSON.stringify(
      {
        root,
        success: result.success,
        hostVersion: result.hostVersion,
        recoveryExact: result.recoveryExact,
        error: result.error,
        report: join(root, "report.json"),
      },
      null,
      2,
    ),
  );
}
