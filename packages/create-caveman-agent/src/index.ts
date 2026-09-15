#!/usr/bin/env node
import { spawn } from "node:child_process";
import { mkdir, rename, rm, stat, writeFile } from "node:fs/promises";
import { basename, dirname, resolve } from "node:path";
import { createInterface } from "node:readline/promises";

type Provider = "anthropic" | "openai" | "google";

const MODELS: Record<Provider, string> = {
  anthropic: "anthropic/claude-haiku-4-5",
  openai: "openai/gpt-5.4-mini",
  google: "google/gemini-2.5-flash",
};

const USAGE = "usage: npm create @caveman-ai/agent@latest <project> [--provider anthropic|openai|google] [--no-install]";

async function main(): Promise<void> {
  const args = process.argv.slice(2);
  if (args.length === 1 && (args[0] === "--help" || args[0] === "-h")) {
    process.stdout.write(`${USAGE}\n`);
    return;
  }
  const parsed = parseArgs(args);
  const targetArg = parsed.target;
  const target = resolve(targetArg);
  const name = safeName(basename(target));
  await assertAbsent(target);
  const provider = await chooseProvider(parsed.provider);
  const temporary = resolve(dirname(target), `.${basename(target)}.${process.pid}.${crypto.randomUUID()}.tmp`);
  try {
    await mkdir(resolve(temporary, "src"), { recursive: true });
    await mkdir(resolve(temporary, "evals"), { recursive: true });
    await mkdir(resolve(temporary, ".caveman"), { recursive: true });
    const files = projectFiles(name, provider);
    for (const [path, content] of Object.entries(files)) {
      await writeFile(resolve(temporary, path), content, { flag: "wx", mode: 0o600 });
    }
    if (parsed.install) await installDependencies(temporary);
    await rename(temporary, target);
  } catch (error) {
    await rm(temporary, { recursive: true, force: true });
    throw error;
  }
  process.stdout.write([
    `created ${name}`,
    `provider ${provider} (${MODELS[provider]})`,
    `cd ${targetArg}`,
    ...(parsed.install ? [] : ["npm install"]),
    "npm run dev",
    "eval starts unapproved; inspect it, set approved: true, then npm run build",
    "",
  ].join("\n"));
}

function parseArgs(args: string[]): {
  target: string;
  provider?: string;
  install: boolean;
} {
  let provider: string | undefined;
  let install = true;
  const positional: string[] = [];
  for (let index = 0; index < args.length; index++) {
    const value = args[index]!;
    if (value === "--provider") {
      const candidate = args[++index];
      if (!candidate || candidate.startsWith("--")) throw new Error("--provider requires a value");
      provider = candidate;
      continue;
    }
    if (value === "--no-install") {
      install = false;
      continue;
    }
    if (value.startsWith("--")) throw new Error(`unknown option ${value}`);
    positional.push(value);
  }
  if (positional.length !== 1) {
    throw new Error(
      USAGE,
    );
  }
  return {
    target: positional[0]!,
    ...(provider === undefined ? {} : { provider }),
    install,
  };
}

async function installDependencies(directory: string): Promise<void> {
  const npmExecPath = process.env.npm_execpath;
  const windowsShell = !npmExecPath && process.platform === "win32";
  const command = npmExecPath
    ? process.execPath
    : windowsShell
      ? process.env.ComSpec ?? "cmd.exe"
      : "npm";
  const args = npmExecPath
    ? [npmExecPath, "install", "--no-audit", "--no-fund", "--ignore-scripts"]
    : windowsShell
      ? ["/d", "/s", "/c", "npm install --no-audit --no-fund --ignore-scripts"]
      : ["install", "--no-audit", "--no-fund", "--ignore-scripts"];
  await new Promise<void>((done, reject) => {
    const child = spawn(command, args, {
      cwd: directory,
      env: dependencyInstallEnv(),
      stdio: "inherit",
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) done();
      else reject(new Error(`npm install failed (${signal ?? code})`));
    });
  });
}

function dependencyInstallEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const key of [
    "PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SystemRoot", "ComSpec", "PATHEXT",
    "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "CI", "NO_COLOR", "FORCE_COLOR",
    "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
    "NPM_CONFIG_CACHE", "NPM_CONFIG_REGISTRY", "NPM_CONFIG_USERCONFIG",
    "npm_config_cache", "npm_config_registry", "npm_config_userconfig",
  ]) {
    if (process.env[key] !== undefined) env[key] = process.env[key];
  }
  return env;
}

async function chooseProvider(flag: string | undefined): Promise<Provider> {
  if (flag !== undefined) return parseProvider(flag);
  const detected: Provider[] = [];
  if (process.env.ANTHROPIC_API_KEY) detected.push("anthropic");
  if (process.env.OPENAI_API_KEY) detected.push("openai");
  if (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) detected.push("google");
  if (detected.length === 1) return detected[0]!;
  if (!process.stdin.isTTY || !process.stdout.isTTY) {
    throw new Error(
      detected.length === 0
        ? "no provider credential detected; pass --provider"
        : "multiple provider credentials detected; pass --provider",
    );
  }
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    const answer = await rl.question("Provider (anthropic/openai/google): ");
    return parseProvider(answer);
  } finally {
    rl.close();
  }
}

function projectFiles(name: string, provider: Provider): Record<string, string> {
  return {
    "package.json": `${JSON.stringify({
      name,
      private: true,
      type: "module",
      engines: { node: ">=22.19.0" },
      scripts: {
        doctor: "caveman-agent doctor",
        dev: "caveman-agent dev src/agent.ts",
        run: "node --experimental-strip-types src/run.ts",
        build: "caveman-agent build caveman.config.ts",
        check: "caveman-agent check caveman.config.ts",
        typecheck: "tsc --noEmit",
      },
      dependencies: {
        "@caveman-ai/agent": "^0.1.0",
      },
      devDependencies: {
        "typescript": "5.9.3",
      },
    }, null, 2)}\n`,
    "tsconfig.json": `${JSON.stringify({
      compilerOptions: {
        target: "ES2024",
        module: "NodeNext",
        moduleResolution: "NodeNext",
        allowImportingTsExtensions: true,
        strict: true,
        noEmit: true,
        skipLibCheck: true,
      },
      include: ["src/**/*.ts", "evals/**/*.ts", "caveman.config.ts"],
    }, null, 2)}\n`,
    "src/agent.ts": `import { agent, auto, schema, tool } from "@caveman-ai/agent";

const readiness = tool({
  name: "readiness",
  description: "Read fixture readiness before answering.",
  input: schema.object({}),
  effect: "read",
  execute: () => ({ status: "ready" }),
});

export default agent({
  id: ${JSON.stringify(name)},
  instructions: "Call readiness once. Then reply with exactly: ready",
  model: auto(),
  tools: [readiness],
});
`,
    "src/run.ts": `import { run } from "@caveman-ai/agent";
import { fileURLToPath } from "node:url";
import definition from "./agent.ts";

const rootDir = fileURLToPath(new URL("..", import.meta.url));

// The economic dial, written out so it is visible on day one rather than
// discovered later. Every figure is an estimated public-catalog list-price
// subtotal, never an invoice.
const result = await run(definition, "Reply with exactly: ready", {
  rootDir,
  entryPath: "src/agent.ts",
  budget: {
    // Pre-call admission cap. The run reserves each call's estimated worst
    // case against this. If a provider's final usage exceeds that reserve,
    // the receipt records capBreached/overspent and the run stops.
    maxUsd: 0.5,
    // What happens as reserve headroom approaches the cap. "compact" (the
    // default) rewrites context while the same cap can fund useful work after;
    // "stop" skips straight to clamping the output and stopping.
    onExhausted: "compact",
  },
});

// When admission reaches the cap, the run returns partial work with a reason.
// Provider-reported overruns remain visible in the receipt.
console.log(result.stopReason, result.text);
console.log(\`estimated list-price subtotal: $\${result.receipt.totalEstimatedUsd}\`);
`,
    "evals/smoke.eval.ts": `import { eval as defineEval } from "@caveman-ai/agent";

export const smoke = defineEval({
  id: "smoke",
  approved: false,
  input: "Reply with exactly: ready",
  quality: [
    { type: "exact_match", expected: "ready" },
    { type: "tool_called", tools: ["readiness"] },
  ],
});
`,
    "caveman.config.ts": `import { defineBuild } from "@caveman-ai/agent/build";

export default defineBuild({
  entry: "src/agent.ts",
  evals: "evals/*.eval.ts",
  maxSearchCostUsd: 2,
});
`,
    ".caveman/provider.json": `${JSON.stringify({ provider, model: MODELS[provider] }, null, 2)}\n`,
    ".gitignore": "node_modules/\n.caveman/traces/\n.env*\n",
    "README.md": `# ${name}

Set ${credentialName(provider)} in your shell. Never commit provider credentials.

\`\`\`bash
npm install
npm run doctor
npm run dev
npm run run
npm run typecheck
\`\`\`

If doctor reports missing signed runtime artifacts, run \`caveman setup --install\`,
then rerun doctor.

\`src/run.ts\` shows the budget dial. A run declares one denomination
(\`maxUsd\` or \`maxTokens\`) and reserves each call's estimated worst case
against it before admission. When the cap binds, \`budget.onExhausted\` decides
what happens: \`"compact"\` compresses the context and carries on inside remaining
headroom, while \`"stop"\` clamps the output and stops. If a provider later
reports usage above its estimate, the receipt records \`capBreached\` and
\`overspent\` honestly, then the run stops.

Starter eval is intentionally unapproved. After confirming expected behavior,
change \`approved: false\` to \`approved: true\` in \`evals/smoke.eval.ts\`, then:

\`\`\`bash
npm run build
npm run check
\`\`\`

Local results are inferred. Verified savings remain $0 until active real
traffic passes existing Caveman rollout and ledger gates.
`,
  };
}

function credentialName(provider: Provider): string {
  if (provider === "anthropic") return "ANTHROPIC_API_KEY";
  if (provider === "openai") return "OPENAI_API_KEY";
  return "GEMINI_API_KEY (or GOOGLE_API_KEY)";
}

function parseProvider(value: string): Provider {
  const normalized = value.trim().toLowerCase();
  if (normalized === "anthropic" || normalized === "openai" || normalized === "google") return normalized;
  throw new Error(`unsupported provider ${JSON.stringify(value)}`);
}

function safeName(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "");
  if (!normalized) throw new Error("project name must contain a letter or number");
  return normalized.slice(0, 96);
}

async function assertAbsent(path: string): Promise<void> {
  try {
    await stat(path);
    throw new Error(`target already exists: ${path}`);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

main().catch((error) => {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`create-caveman-agent: ${message}\n`);
  process.exitCode = 1;
});
