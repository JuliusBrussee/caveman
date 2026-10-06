// Fixture for the modules tests: a temp HOME and CAVEMAN_HOME, fake agents and
// Go binaries on a PATH that holds nothing from the machine running the tests.
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

export const HARNESS_FILES = [".claude/settings.json", ".claude.json", ".codex/config.toml", ".codex/hooks.json"];

export function modulesFixture({ agents = ["claude", "codex"], binaries = true, blocks = false } = {}) {
  const home = mkdtempSync(join(tmpdir(), "caveman-modules-"));
  const bin = join(home, "bin");
  mkdirSync(bin, { recursive: true });
  symlinkSync(process.execPath, join(bin, "node"));
  const script = (name, body) => {
    const path = join(bin, name);
    writeFileSync(path, `#!/bin/sh\n${body}\n`, { mode: 0o755 });
    chmodSync(path, 0o755);
    return path;
  };
  for (const agent of agents) script(agent, `if [ "$1" = "--version" ]; then echo '${agent} 1.0.0'; fi`);
  const bins = {
    CAVEMAN_PROXY_BIN: script("caveman-proxy", `case "$1" in
  version) printf '%s\\n' '{"version":"1.0.0","capabilities":["run_state","native_runtime_v1","native_hook_bridge_v1","typed_ccr"]}' ;;
  status) if [ -f "$CAVEMAN_HOME/run/$4.json" ]; then cat "$CAVEMAN_HOME/run/$4.json"; else printf '%s\\n' '{"owner":"unknown"}'; fi ;;
  stats) printf '%s\\n' '{"spans":0,"tokens_in":0,"token_accounting":{},"basis":"inferred"}' ;;
esac`),
    CAVEMAN_MCP_BIN: script("caveman-mcp", `if [ "$1" = "version" ]; then printf '%s\\n' '{"version":"1.0.0","capabilities":["mcp_recovery"]}'; fi`),
    CAVEMAN_ENGINE_BIN: script("caveman-engine", "exit 0"),
    CAVEMEM_BIN: script("cavemem", "exit 0"),
    CAVEMAN_SHRINK_BIN: script("caveman-shrink", "exit 0"),
    CAVEMAN_BROWSE_BIN: script("caveman-browse", "exit 0"),
  };
  if (!binaries) for (const key of Object.keys(bins)) bins[key] = join(home, "missing", key);
  if (blocks) {
    // Like the real one: a harness is listed once its home exists, install
    // hooks every listed one and talks, and $HOME/.blocks-fail fails it. Hook
    // state lives outside the harness files so their round-trip stays exact.
    script("caveman-blocks", `harnesses() { for h in claude-code:.claude codex:.codex; do [ -d "$HOME/\${h#*:}" ] && echo "\${h%%:*}"; done; }
case "$1 $2" in
  "hooks status") sep=; printf '{"version":"test","harnesses":['
    for n in $(harnesses); do if [ -f "$HOME/.blocks-$n" ]; then i=true; else i=false; fi; printf '%s{"name":"%s","installed":%s}' "$sep" "$n" "$i"; sep=,; done
    printf ']}\\n' ;;
  "hooks install") if [ -f "$HOME/.blocks-fail" ]; then echo "binary: copied"; echo "codex: cannot write ~/.codex/hooks.json: permission denied" >&2; exit 1; fi
    echo "binary: $HOME/.local/bin/caveman-blocks (copied)"
    for n in $(harnesses); do : > "$HOME/.blocks-$n"; echo "$n: installed"; done
    echo "  Codex trusts each new or changed hook once: open /hooks in Codex and approve it."
    echo install >> "$HOME/blocks.log" ;;
  "hooks uninstall") rm -f "$HOME/.blocks-claude-code" "$HOME/.blocks-codex"; echo uninstall >> "$HOME/blocks.log" ;;
esac`);
  }
  return {
    home,
    bin,
    env: {
      HOME: home,
      USERPROFILE: home,
      CAVEMAN_HOME: join(home, ".caveman"),
      PATH: `${bin}:/usr/bin:/bin`,
      CAVE_NO_KEYCHAIN: "1",
      CAVEMAN_TELEMETRY: "0",
      NO_COLOR: "1",
      CI: "1",
      // Port 9 (discard): nothing a developer machine runs, unlike 8787.
      CAVE_GATEWAY_URL: "http://127.0.0.1:9",
      CAVEMAN_LISTEN: "127.0.0.1:9",
      CAVE_API_URL: "http://127.0.0.1:9",
      CAVE_BINARY_PROBE_TIMEOUT_MS: "10000",
      ...bins,
    },
    cleanup() {
      rmSync(home, { recursive: true, force: true });
    },
  };
}

export function runCli(argv, env, { cwd, timeoutMs = 30_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env, cwd, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`timed out: caveman ${argv.join(" ")}`));
    }, timeoutMs);
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
    child.stdin.end();
  });
}

// Every file under HOME except the fake binaries: path → bytes.
export function snapshot(home) {
  const out = {};
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (path === join(home, "bin")) continue;
      if (statSync(path).isDirectory()) walk(path);
      else out[relative(home, path)] = readFileSync(path, "base64");
    }
  };
  walk(home);
  return out;
}

export function harness(home) {
  return Object.fromEntries(HARNESS_FILES.map((file) => {
    const path = join(home, file);
    return [file, existsSync(path) ? readFileSync(path, "utf8") : null];
  }));
}
