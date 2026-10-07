// `caveman providers add|remove|login|local`: the provider credentials the
// local runtime may route to, beyond the harness's own. The routing ask sends
// Cloud a pool built from these (which models each login reaches, never a
// secret); requests Cloud sends to one of them go straight from this machine
// to that provider on that credential.
//
// Same split as the Cloud login: $CAVEMAN_HOME/provider-logins.json lists ids
// and where each secret lives; the secret goes to the macOS keychain (service
// caveman-provider) or a 0600 file under $CAVEMAN_HOME/provider-logins/. The
// runtime (proxy/internal/pool) reads both; its providers.json is the
// registry this list mirrors (a test keeps them in step).
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, closeSync, mkdirSync, openSync, readFileSync, renameSync, statSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { cavemanHome } from "./config-home.js";

export type ProviderLogin = { id: string; name: string; kind: "api_key" | "oauth"; env: string[]; terms?: string };

export const PROVIDER_LOGINS: ProviderLogin[] = [
  { id: "anthropic", name: "Anthropic", kind: "api_key", env: ["ANTHROPIC_API_KEY"] },
  { id: "openai", name: "OpenAI", kind: "api_key", env: ["OPENAI_API_KEY"] },
  { id: "chatgpt", name: "ChatGPT plan (Sign in with ChatGPT)", kind: "oauth", env: [], terms: "OpenAI's Sign in with ChatGPT for local apps (preview): a login of Caveman's own, never Codex's. Experimental." },
  { id: "opencode-go", name: "OpenCode Go", kind: "api_key", env: ["OPENCODE_API_KEY"], terms: "Used with your agent's own User-Agent and a stable session header, as OpenCode Go allows." },
  { id: "openrouter", name: "OpenRouter", kind: "api_key", env: ["OPENROUTER_API_KEY"] },
  { id: "fireworks", name: "Fireworks AI", kind: "api_key", env: ["FIREWORKS_API_KEY"] },
  { id: "deepseek", name: "DeepSeek", kind: "api_key", env: ["DEEPSEEK_API_KEY"] },
  { id: "moonshot", name: "Moonshot (Kimi)", kind: "api_key", env: ["MOONSHOT_API_KEY"] },
  { id: "zai", name: "Z.ai (GLM, pay as you go)", kind: "api_key", env: ["ZAI_API_KEY", "ZHIPU_API_KEY"] },
  { id: "zai-coding-plan", name: "Z.ai GLM Coding Plan", kind: "api_key", env: [], terms: "Only Claude Code, Codex and OpenCode requests use the plan, as Z.ai's terms list." },
  { id: "gemini", name: "Google Gemini API", kind: "api_key", env: ["GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"], terms: "An API key only; Google logins are never used." },
  { id: "xai", name: "xAI", kind: "api_key", env: ["XAI_API_KEY"] },
  { id: "groq", name: "Groq", kind: "api_key", env: ["GROQ_API_KEY"] },
  { id: "together", name: "Together AI", kind: "api_key", env: ["TOGETHER_API_KEY"] },
];

// Logins that cannot be added, and why: their terms forbid a third-party client.
export const REFUSED_LOGINS: Record<string, string> = {
  claude: "Claude Pro/Max logins work only in Anthropic's own apps; add an Anthropic API key instead (caveman providers add anthropic).",
  "claude-code": "Claude Pro/Max logins work only in Anthropic's own apps; add an Anthropic API key instead (caveman providers add anthropic).",
  google: "Google bans accounts whose Gemini CLI or Antigravity login is used by other tools; add a Gemini API key instead (caveman providers add gemini).",
  "gemini-cli": "Google bans accounts whose Gemini CLI or Antigravity login is used by other tools; add a Gemini API key instead (caveman providers add gemini).",
  copilot: "GitHub Copilot is supported only through OpenCode's own GitHub login.",
  "github-copilot": "GitHub Copilot is supported only through OpenCode's own GitHub login.",
  codex: "Codex's own login stays Codex's; sign in for Caveman with `caveman providers login chatgpt`.",
};

const KEYCHAIN_SERVICE = "caveman-provider";

type Index = { version: number; cloud?: boolean; logins: { id: string; kind: string; store: "keychain" | "file"; added_at?: string }[] };

function indexPath() {
  return join(cavemanHome(), "provider-logins.json");
}

function secretPath(id: string) {
  return join(cavemanHome(), "provider-logins", id);
}

function readIndex(): Index {
  try {
    const parsed = JSON.parse(readFileSync(indexPath(), "utf8")) as Partial<Index>;
    return { ...parsed, version: 1, logins: Array.isArray(parsed.logins) ? parsed.logins : [] };
  } catch {
    return { version: 1, logins: [] };
  }
}

// withIndexLock runs one read-modify-write of the index under
// provider-logins.json.lock, the lock the runtime takes too (a background
// token refresh). A lock older than 10 s is a crashed writer's and is broken.
function withIndexLock<T>(fn: () => T): T {
  mkdirSync(cavemanHome(), { recursive: true, mode: 0o700 });
  const lock = `${indexPath()}.lock`;
  const deadline = Date.now() + 3000;
  for (;;) {
    try {
      closeSync(openSync(lock, "wx", 0o600));
      break;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      try {
        if (Date.now() - statSync(lock).mtimeMs > 10_000) {
          unlinkSync(lock);
          continue;
        }
      } catch { /* released meanwhile */ }
      if (Date.now() > deadline) throw new Error("caveman: provider-logins.json is locked by another caveman process; try again");
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 20);
    }
  }
  try {
    return fn();
  } finally {
    try { unlinkSync(lock); } catch { /* already gone */ }
  }
}

function writeIndex(index: Index) {
  mkdirSync(cavemanHome(), { recursive: true, mode: 0o700 });
  const tmp = `${indexPath()}.tmp`;
  writeFileSync(tmp, JSON.stringify(index, null, 2) + "\n", { mode: 0o600 });
  renameSync(tmp, indexPath());
}

function useKeychain() {
  return process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN;
}

// keychainSet sends the secret on stdin through `security -i`, never argv.
function keychainSet(id: string, secret: string): boolean {
  const quote = (value: string) => `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
  const run = spawnSync("security", ["-i"], { input: `add-generic-password -U -s ${KEYCHAIN_SERVICE} -a ${quote(id)} -w ${quote(secret)}\n`, stdio: ["pipe", "ignore", "ignore"] });
  if (run.status !== 0) return false;
  try {
    return execFileSync("security", ["find-generic-password", "-s", KEYCHAIN_SERVICE, "-a", id, "-w"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim() === secret;
  } catch {
    return false;
  }
}

function keychainDelete(id: string) {
  try {
    execFileSync("security", ["delete-generic-password", "-s", KEYCHAIN_SERVICE, "-a", id], { stdio: "ignore" });
  } catch (error) {
    if ((error as { status?: unknown }).status !== 44) throw new Error("could not remove the key from macOS Keychain");
  }
}

function find(id: string | undefined): ProviderLogin {
  const provider = PROVIDER_LOGINS.find((entry) => entry.id === id);
  if (provider) return provider;
  if (id && REFUSED_LOGINS[id]) throw new Error(`caveman: ${REFUSED_LOGINS[id]}`);
  throw new Error(`caveman: unknown provider ${JSON.stringify(id ?? "")}; one of: ${PROVIDER_LOGINS.map((entry) => entry.id).join(", ")}`);
}

function flag(argv: string[], name: string): string | undefined {
  const index = argv.indexOf(name);
  if (index >= 0) return argv[index + 1];
  return argv.find((value) => value.startsWith(`${name}=`))?.slice(name.length + 1);
}

// providersAdd stores an API key. The key comes from --key-env NAME, --stdin,
// or the provider's usual environment variable; never from argv, which any
// local process can read.
export function providersAdd(argv: string[], readStdin: () => string): Record<string, unknown> {
  const provider = find(argv[0]);
  if (provider.kind !== "api_key") throw new Error(`caveman: ${provider.id} is a sign-in, not a key: caveman providers login ${provider.id}`);
  let key = "";
  let source = "";
  const named = flag(argv, "--key-env");
  if (named) {
    key = process.env[named] ?? "";
    source = named;
  } else if (argv.includes("--stdin")) {
    key = readStdin();
    source = "stdin";
  } else {
    for (const name of provider.env) {
      if (process.env[name]) {
        key = process.env[name]!;
        source = name;
        break;
      }
    }
  }
  key = key.trim();
  if (!key) {
    const from = provider.env.length ? provider.env.join(" or ") : "--key-env NAME";
    throw new Error(`caveman: no key for ${provider.id}: set ${from}, or pipe it: caveman providers add ${provider.id} --stdin`);
  }
  if (/\s/.test(key) || key.length > 4096) throw new Error("caveman: that does not look like an API key");
  return withIndexLock(() => {
    const index = readIndex();
    const previous = index.logins.find((entry) => entry.id === provider.id);
    let store: "keychain" | "file" = "file";
    if (useKeychain() && keychainSet(provider.id, key)) {
      store = "keychain";
      removeSecretFile(provider.id); // a key never lives in both stores
    } else {
      mkdirSync(join(cavemanHome(), "provider-logins"), { recursive: true, mode: 0o700 });
      writeFileSync(secretPath(provider.id), key, { mode: 0o600 });
      chmodSync(secretPath(provider.id), 0o600);
      if (previous?.store === "keychain" && useKeychain()) keychainDelete(provider.id);
    }
    index.logins = index.logins.filter((entry) => entry.id !== provider.id);
    index.logins.push({ id: provider.id, kind: "api_key", store, added_at: previous?.added_at ?? new Date().toISOString() });
    writeIndex(index);
    return { added: provider.id, name: provider.name, store, from: source };
  });
}

function removeSecretFile(id: string) {
  try {
    unlinkSync(secretPath(id));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

export function providersRemove(argv: string[]): Record<string, unknown> {
  const provider = find(argv[0]);
  return withIndexLock(() => {
    const index = readIndex();
    const entry = index.logins.find((login) => login.id === provider.id);
    if (entry?.store === "keychain" || (!entry && useKeychain())) keychainDelete(provider.id);
    removeSecretFile(provider.id);
    index.logins = index.logins.filter((login) => login.id !== provider.id);
    writeIndex(index);
    return { removed: provider.id, was_added: Boolean(entry) };
  });
}

// providersCloud is `caveman providers cloud on|off`: whether routing may send
// a request through Caveman Cloud (on by default while signed in). Off, such
// an answer runs the model the agent asked for on its own credential.
export function providersCloud(argv: string[]): Record<string, unknown> {
  const setting = argv[0];
  if (setting !== "on" && setting !== "off") {
    return { cloud: readIndex().cloud === false ? "off" : "on" };
  }
  return withIndexLock(() => {
    const index = readIndex();
    index.cloud = setting === "on";
    writeIndex(index);
    return { cloud: setting };
  });
}

export function providersLocal(): Record<string, unknown> {
  const logins = readIndex().logins
    .filter((login) => PROVIDER_LOGINS.some((provider) => provider.id === login.id))
    .map((login) => {
      const provider = PROVIDER_LOGINS.find((entry) => entry.id === login.id)!;
      return { id: login.id, name: provider.name, kind: login.kind, store: login.store, added_at: login.added_at, ...(provider.terms ? { terms: provider.terms } : {}) };
    });
  return {
    logins,
    cloud: readIndex().cloud === false ? "off" : "on",
    available: PROVIDER_LOGINS.map((provider) => `${provider.id} (${provider.kind === "oauth" ? "login" : "key"})`),
    traffic: "Requests routed to one of these go straight from this machine to that provider. Only requests routed to a Caveman Cloud model pass through Caveman Cloud, whole (system prompt, tool results, code), on your Caveman project's stored provider key; `caveman providers cloud off` stops that.",
  };
}

// providersLogin runs a subscription sign-in in the runtime binary, which also
// refreshes it: only Sign in with ChatGPT today.
export function providersLogin(argv: string[], proxyBin: string): number {
  const provider = find(argv[0]);
  if (provider.kind !== "oauth") throw new Error(`caveman: ${provider.id} takes an API key: caveman providers add ${provider.id}`);
  const run = spawnSync(proxyBin, ["provider-login", provider.id], { stdio: "inherit" });
  if (run.error) throw new Error(`caveman: could not start ${proxyBin}: run \`caveman setup\` to install the runtime`);
  return run.status ?? 1;
}
