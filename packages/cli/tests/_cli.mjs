import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

export function isolatedCliEnv(extra = {}) {
  const home = mkdtempSync(join(tmpdir(), "caveman-cli-"));
  const bin = join(home, "bin");
  mkdirSync(bin, { recursive: true });
  const noop = join(bin, "noop");
  writeFileSync(noop, `#!/bin/sh
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  printf '%s\\n' '{"version":"test","capabilities":["run_state","mcp_recovery"]}'
fi
exit 0
`, { mode: 0o755 });
  chmodSync(noop, 0o755);
  return {
    home,
    env: {
      ...process.env,
      HOME: home,
      CAVEMAN_HOME: home,
      CAVE_NO_KEYCHAIN: "1",
      NO_COLOR: "1",
      CI: "1",
      CAVEMAN_PROXY_BIN: noop,
      CAVEMAN_ENGINE_BIN: noop,
      CAVEMAN_MCP_BIN: noop,
      CAVEMAN_BROWSE_BIN: noop,
      CAVEMEM_BIN: noop,
      ...extra,
    },
    cleanup() {
      rmSync(home, { recursive: true, force: true });
    },
  };
}

export function runCli(argv, options = {}) {
  const prefix = options.prefix ?? process.env.CAVE_TEST_PREFIX ?? "";
  const fullArgv = prefix ? [prefix, ...argv] : argv;
  const owned = options.env ? null : isolatedCliEnv();
  const env = options.env ?? owned.env;
  const timeoutMs = options.timeoutMs ?? 10_000;
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...fullArgv], {
      env,
      cwd: options.cwd,
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      owned?.cleanup();
      if (error) reject(error);
      else resolve(value);
    };
    const timer = setTimeout(() => {
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 250).unref();
      finish(new Error(`CLI timed out after ${timeoutMs}ms: ${fullArgv.join(" ")}`));
    }, timeoutMs);
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", (error) => finish(error));
    child.on("exit", (code, signal) => finish(null, { code, signal, stdout, stderr, argv: fullArgv }));
    child.stdin.end(options.input ?? "");
  });
}

function listen(server) {
  return new Promise((resolve, reject) => {
    const onError = (error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolve(server.address().port);
    };
    server.once("error", onError);
    server.listen(0, "127.0.0.1", onListening);
  });
}

export async function runCliWithApi(argv, options = {}) {
  const requests = [];
  const sockets = new Set();
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      requests.push({ method: req.method, path: req.url, body, headers: req.headers });
      const response = options.respond?.(req, body) ?? { status: 200, body: {} };
      res.writeHead(response.status ?? 200, { "content-type": "application/json" });
      res.end(JSON.stringify(response.body ?? {}));
    });
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  const port = await listen(server);
  const isolated = isolatedCliEnv({
    CAVE_API_URL: `http://127.0.0.1:${port}`,
    CAVE_TOKEN: "alias-matrix-token",
    ...(options.telemetry
      ? {
          CAVEMAN_TELEMETRY: "1",
          CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry/cli`,
        }
      : {}),
    ...(options.env ?? {}),
  });
  mkdirSync(join(isolated.home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(isolated.home, ".caveman-cloud", "config.json"), JSON.stringify({
    baseURL: `http://127.0.0.1:${port}`,
    projectId: options.projectId ?? "proj-alias",
  }));
  await options.prepare?.({ home: isolated.home, env: isolated.env });
  try {
    const result = await runCli(argv, { ...options, env: isolated.env });
    return { ...result, requests };
  } finally {
    for (const socket of sockets) socket.destroy();
    await new Promise((resolve) => server.close(resolve));
    isolated.cleanup();
  }
}
