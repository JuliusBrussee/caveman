// Fixture for the modules tests: a temp HOME and CAVEMAN_HOME, fake agents and
// Go binaries on a PATH that holds nothing from the machine running the tests.
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

export const HARNESS_FILES = [".claude/settings.json", ".claude.json", ".codex/config.toml", ".codex/hooks.json"];

// A caveman-blocks stand-in that behaves like rc.2: `version --json` names
// hooks_status_json; hooks status/install/uninstall act on each --harness
// (default: every harness whose home exists); install talks; and
// $HOME/.blocks-fail fails it. Hook state lives outside the harness files so
// their round-trip stays exact.
export const FAKE_BLOCKS = `cmd="$1 $2"
if [ $# -ge 2 ]; then shift 2; else shift $#; fi
names=
while [ $# -gt 0 ]; do if [ "$1" = --harness ]; then names="$names $2"; shift; fi; shift; done
if [ -z "$names" ]; then for h in claude-code:.claude codex:.codex; do [ -d "$HOME/\${h#*:}" ] && names="$names \${h%%:*}"; done; fi
case "$cmd" in
  "version --json") printf '%s\\n' '{"version":"test","capabilities":["hooks_status_json"]}' ;;
  "hooks status") sep=; printf '{"version":"test","harnesses":['
    for n in $names; do if [ -f "$HOME/.blocks-$n" ]; then i=true; else i=false; fi; printf '%s{"name":"%s","installed":%s}' "$sep" "$n" "$i"; sep=,; done
    printf ']}\\n' ;;
  "hooks install") if [ -f "$HOME/.blocks-fail" ]; then echo "binary: copied"; echo "codex: cannot write ~/.codex/hooks.json: permission denied" >&2; exit 1; fi
    echo "binary: $HOME/.local/bin/caveman-blocks (copied)"
    for n in $names; do : > "$HOME/.blocks-$n"; echo "$n: installed"; done
    echo "  Codex trusts each new or changed hook once: open /hooks in Codex and approve it."
    echo install >> "$HOME/blocks.log" ;;
  "hooks uninstall") for n in $names; do rm -f "$HOME/.blocks-$n"; done; echo uninstall >> "$HOME/blocks.log" ;;
esac`;

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
  if (blocks) script("caveman-blocks", FAKE_BLOCKS);
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
      // Nothing downloads from the real release.
      CAVE_BINARY_RELEASE_BASE: "http://127.0.0.1:9",
      CAVEMAN_BLOCKS_BIN: blocks ? join(bin, "caveman-blocks") : join(home, "missing", "caveman-blocks"),
      ...bins,
    },
    cleanup() {
      rmSync(home, { recursive: true, force: true });
    },
  };
}

// `cli`: another copy of the built CLI to run (one placed in a runner's cache).
export function runCli(argv, env, { cwd, timeoutMs = 30_000, cli: entry = cli } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [entry, ...argv], { env, cwd, stdio: ["pipe", "pipe", "pipe"] });
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
