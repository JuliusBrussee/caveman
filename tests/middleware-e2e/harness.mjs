// Shared plumbing for the middleware end-to-end (run.mjs), version-skew (skew.mjs) and perf (perf.mjs) scripts:
// build and start a real caveman-proxy, provision client environments under the OS temp dir, and run the drivers.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { existsSync } from 'node:fs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const root = path.resolve(fileURLToPath(new URL('../..', import.meta.url)));
export const here = path.dirname(fileURLToPath(import.meta.url));
export const TOKEN = 'caveman-e2e-operator-token-0123456789';
export const sha256 = value => createHash('sha256').update(value).digest('hex');
export const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const scratch = [];
/** A per-run scratch dir, removed by finish(). */
export const tempDir = async prefix => { const dir = await mkdtemp(path.join(tmpdir(), `caveman-${prefix}-`)); scratch.push(dir); return dir; };

let checks = 0, failures = 0;
/** One TAP line per step; a failing step is reported and the run continues. */
export async function step(name, fn) {
  checks++;
  try {
    const note = await fn();
    console.log(`ok ${checks} - ${name}${note ? ` # ${note}` : ''}`);
  } catch (error) {
    failures++;
    console.log(`not ok ${checks} - ${name}\n  ---\n  message: ${JSON.stringify(String(error?.message ?? error))}\n  ...`);
  }
}
export async function finish() {
  await Promise.all(scratch.map(dir => rm(dir, { recursive: true, force: true })));
  console.log(`1..${checks}\n# pass ${checks - failures}, fail ${failures}`);
  process.exitCode = failures ? 1 : 0;
}

/** Runs a command; resolves {code, stdout, stderr}; rejects on a non-zero exit unless allowFail. */
export function sh(command, args, { cwd = root, env = process.env, allowFail = false } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', code => {
      if (code === 0 || allowFail) resolve({ code, stdout, stderr });
      else reject(new Error(`${command} ${args.join(' ')} exited ${code}\n${stderr.slice(-4000)}${stdout.slice(-4000)}`));
    });
  });
}

export async function buildProxy(dir) {
  if (process.env.CAVEMAN_E2E_PROXY_BIN) return process.env.CAVEMAN_E2E_PROXY_BIN;
  const out = path.join(dir, process.platform === 'win32' ? 'caveman-proxy.exe' : 'caveman-proxy');
  await sh('go', ['build', '-o', out, './proxy/cmd/caveman-proxy']);
  return out;
}

async function freePort() {
  const server = createServer().listen(0, '127.0.0.1');
  await once(server, 'listening');
  const { port } = server.address();
  server.close();
  await once(server, 'close');
  return port;
}

/** A caveman-proxy on a random loopback port with its own temp home. Inherited CAVEMAN_* and CAVE_* variables are
 * dropped so a developer's shell cannot change what is tested. */
export async function startRuntime(bin, env = {}) {
  const home = await tempDir('runtime-home');
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const inherited = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^CAVE(MAN)?_/.test(key)));
  const child = spawn(bin, [], { env: { ...inherited, CAVEMAN_HOME: home, CAVEMAN_LISTEN: `127.0.0.1:${port}`, CAVEMAN_AUTH_TOKEN: TOKEN, ...env },
    stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { log = (log + chunk).slice(-8000); });
  const stop = async (signal = 'SIGTERM') => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill(signal);
      await Promise.race([once(child, 'exit'), sleep(5000).then(() => child.kill('SIGKILL'))]);
    }
    await rm(home, { recursive: true, force: true });
  };
  for (const started = Date.now(); Date.now() - started < 30_000; await sleep(100)) {
    if (child.exitCode !== null) break;
    try {
      const response = await fetch(`${base}/caveman/v1/middleware/capabilities`, { headers: { authorization: `Bearer ${TOKEN}` } });
      if (response.ok) return { base, home, child, stop, log: () => log };
    } catch { /* not listening yet */ }
  }
  await stop();
  throw new Error(`caveman-proxy did not serve capabilities within 30s:\n${log}`);
}

export function tokenMap(dir, principals) {
  const file = path.join(dir, 'tokens.json');
  return writeFile(file, JSON.stringify({ principals: principals.map(({ name, token, namespaces }) =>
    ({ name, namespaces, token_sha256: [sha256(token)] })) })).then(() => file);
}

export const kit = (base, ...args) => sh(process.execPath, [path.join(root, 'packages/shared/contracts/conformance/run.mjs'), base, ...args], { allowFail: true });
/** "pass N, fail M, skip K" from a kit run, or the failing lines. */
export function kitResult(result) {
  const summary = result.stdout.match(/# (pass \d+, fail \d+, skip \d+)/)?.[1] ?? 'no summary';
  if (result.code !== 0) throw new Error(`${summary}\n${result.stdout.split('\n').filter(line => /^not ok|^ {2}|^Bail/.test(line)).join('\n')}${result.stderr}`);
  return summary;
}

/** A venv under the temp dir, reused across runs while its requirement list is unchanged. */
export async function pythonEnv(name, requirements, python = process.env.CAVEMAN_E2E_PYTHON ?? 'python3') {
  const dir = path.join(tmpdir(), `caveman-e2e-${name}-${sha256(`${python} ${requirements.join(' ')}`).slice(0, 12)}`);
  const bin = path.join(dir, process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
  if (!existsSync(path.join(dir, '.complete'))) {
    await rm(dir, { recursive: true, force: true });
    await sh(python, ['-m', 'venv', dir]);
    await sh(bin, ['-m', 'pip', 'install', '-q', '--disable-pip-version-check', ...requirements]);
    await writeFile(path.join(dir, '.complete'), requirements.join('\n'));
  }
  return bin;
}

/** An npm install of exact published versions under the temp dir, reused across runs. */
export async function npmEnv(name, packages) {
  const dir = path.join(tmpdir(), `caveman-e2e-${name}-${sha256(packages.join(' ')).slice(0, 12)}`);
  if (!existsSync(path.join(dir, '.complete'))) {
    await rm(dir, { recursive: true, force: true });
    await sh('npm', ['install', '--prefix', dir, '--no-audit', '--no-fund', '--no-package-lock', ...packages]);
    await writeFile(path.join(dir, 'package.json'), JSON.stringify({ private: true, type: 'module' }));
    await writeFile(path.join(dir, '.complete'), packages.join('\n'));
  }
  return dir;
}

// The certified adapter per language: the AI SDK adapter (TS) and the OpenAI adapter (Python), each against a mock
// provider. Both print one JSON line and exit non-zero when the expectation fails.
export const HEAD_TS = path.join(root, 'packages/middleware/typescript');
export const HEAD_PYTHONPATH = [path.join(root, 'packages/sdk/python'), path.join(root, 'packages/middleware/python')].join(path.delimiter);

export async function driveTS(base, expect, { from = HEAD_TS, token = TOKEN } = {}) {
  const result = await sh(process.execPath, [path.join(here, 'drive-ts.mjs'), '--base', base, '--token', token, '--from', from, '--expect', expect], { allowFail: true });
  return driven(result, 'drive-ts');
}

export async function drivePython(base, expect, { python, pythonPath = HEAD_PYTHONPATH, token = TOKEN } = {}) {
  const env = { ...process.env, PYTHONPATH: pythonPath ?? '' };
  if (!pythonPath) delete env.PYTHONPATH;
  const result = await sh(python, [path.join(here, 'drive_py.py'), '--base', base, '--token', token, '--expect', expect], { allowFail: true, env });
  return driven(result, 'drive_py');
}

function driven(result, name) {
  const line = result.stdout.trim().split('\n').at(-1) ?? '';
  let parsed;
  try { parsed = JSON.parse(line); } catch { /* reported below */ }
  if (result.code !== 0 || !parsed) throw new Error(`${name} exited ${result.code}: ${line}\n${result.stderr.slice(-3000)}`);
  return parsed;
}

export function requireBuilt() {
  for (const file of ['packages/sdk/typescript/dist/middleware/index.js', 'packages/middleware/typescript/dist/ai-sdk.js']) {
    if (!existsSync(path.join(root, file))) {
      throw new Error(`${file} is missing; run: pnpm install --frozen-lockfile && pnpm --filter "@caveman-ai/middleware..." build`);
    }
  }
}
