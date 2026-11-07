import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join, resolve } from 'node:path';
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const { portableInvocation, resolveWindowsCommand } = require('../../installer/lib/portable-process.js');
const root = process.cwd();
// The profile pin is the version under test; compile.mjs keeps it equal to the
// agent-conformance matrix pin, so this lane never needs its own edit per bump.
const pinned = JSON.parse(readFileSync(resolve('agents/profiles/codex.json'), 'utf8')).tested_agent_version;
assert.match(pinned, /^\d+\.\d+\.\d+$/, 'agents/profiles/codex.json must pin a release version');
const evidence = resolve('evidence');
mkdirSync(evidence, { recursive: true });

function environment(home) {
  const env = {};
  for (const key of ['PATH', 'PATHEXT', 'SystemRoot', 'WINDIR', 'ComSpec', 'TEMP', 'TMP'])
    if (process.env[key]) env[key] = process.env[key];
  Object.assign(env, {
    HOME: home, USERPROFILE: home, CODEX_HOME: join(home, '.codex'),
    APPDATA: join(home, 'AppData', 'Roaming'), LOCALAPPDATA: join(home, 'AppData', 'Local'),
    XDG_CONFIG_HOME: join(home, '.config'), XDG_DATA_HOME: join(home, '.local', 'share'),
    XDG_CACHE_HOME: join(home, '.cache'), CAVEMAN_HOME: join(home, '.caveman'),
    CAVEMAN_TELEMETRY: '0', DO_NOT_TRACK: '1', OTEL_SDK_DISABLED: 'true',
    CI: '1', NO_COLOR: '1',
  });
  for (const key of ['CODEX_HOME', 'APPDATA', 'LOCALAPPDATA', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME'])
    mkdirSync(env[key], { recursive: true });
  return env;
}

function run(command, args, env, cwd, input = null) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { env, cwd, windowsHide: true, stdio: [input === null ? 'ignore' : 'pipe', 'pipe', 'pipe'] });
    if (input !== null) child.stdin.end(input);
    let stdout = '', stderr = '';
    child.stdout.on('data', chunk => stdout += chunk);
    child.stderr.on('data', chunk => stderr += chunk);
    const timer = setTimeout(() => { child.kill(); reject(new Error(`Timed out: ${command}`)); }, 90000);
    child.once('error', error => { clearTimeout(timer); reject(error); });
    child.once('close', (code, signal) => { clearTimeout(timer); resolve({ code, signal, stdout, stderr }); });
  });
}

function reply(response, marker, model) {
  const item = { id: 'msg_route', type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: marker, annotations: [] }] };
  const base = { id: 'resp_route', object: 'response', created_at: 1, model, output: [] };
  const events = [
    { type: 'response.created', response: { ...base, status: 'in_progress' } },
    { type: 'response.output_item.added', output_index: 0, item: { ...item, status: 'in_progress', content: [] } },
    { type: 'response.content_part.added', item_id: item.id, output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } },
    { type: 'response.output_text.delta', item_id: item.id, output_index: 0, content_index: 0, delta: marker },
    { type: 'response.output_text.done', item_id: item.id, output_index: 0, content_index: 0, text: marker },
    { type: 'response.content_part.done', item_id: item.id, output_index: 0, content_index: 0, part: item.content[0] },
    { type: 'response.output_item.done', output_index: 0, item },
    { type: 'response.completed', response: { ...base, status: 'completed', output: [item], usage: { input_tokens: 8, output_tokens: 4, total_tokens: 12 } } },
  ];
  response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
  events.forEach((event, index) => response.write(`event: ${event.type}\ndata: ${JSON.stringify({ sequence_number: index, ...event })}\n\n`));
  response.end();
}

const results = [];
for (let number = 1; number <= 2; number++) {
  const home = mkdtempSync(join(tmpdir(), `cave-codex-route-${number}-`));
  const env = environment(home);
  const workspace = join(home, 'workspace');
  mkdirSync(workspace);
  const key = `sk-dummy-local-route-${number}`;
  env.OPENAI_API_KEY = key;
  const config = 'model = "gpt-5.5"\nmodel_reasoning_effort = "low"\ncli_auth_credentials_store = "file"\n[analytics]\nenabled = false\n';
  writeFileSync(join(env.CODEX_HOME, 'config.toml'), config);
  mkdirSync(join(home, '.caveman-cloud'));
  writeFileSync(join(home, '.caveman-cloud', 'config.json'), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false, delegate: false } }));
  const binary = resolveWindowsCommand('codex', env);
  assert.ok(binary, 'npm-installed Codex must be on the isolated PATH');
  const versionInvocation = portableInvocation(binary, ['--version'], { env });
  const version = await run(versionInvocation.command, versionInvocation.args, env, workspace);
  assert.equal(version.code, 0, version.stderr);
  assert.match(version.stdout, new RegExp(`(^|[^0-9.])${pinned.replaceAll('.', '\\.')}($|[^0-9.])`));
  // Have the real binary create its own credential schema, with a dummy key
  // and file storage inside this scratch home (never the Windows keychain).
  const loginInvocation = portableInvocation(binary, ['login', '--with-api-key'], { env });
  const login = await run(loginInvocation.command, loginInvocation.args, env, workspace, `${key}\n`);
  assert.equal(login.code, 0, login.stderr);
  const auth = readFileSync(join(env.CODEX_HOME, 'auth.json'), 'utf8');
  assert.equal(JSON.parse(auth).OPENAI_API_KEY, key);
  const requests = [];
  const marker = `CAVE_ROUTE_OK_${number}`;
  const server = createServer(async (request, response) => {
    let raw = '';
    for await (const chunk of request) raw += chunk;
    const record = { method: request.method, path: request.url, headers: request.headers, body: raw ? JSON.parse(raw) : null };
    requests.push(record);
    if (request.method === 'POST' && request.url === '/w/codex/v1/responses')
      reply(response, marker, record.body.model);
    else { response.writeHead(404); response.end('{}'); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  env.CAVE_GATEWAY_URL = `http://127.0.0.1:${port}`;
  const args = [resolve('packages/cli/dist/index.js'), 'wrap', 'codex', 'exec', '--skip-git-repo-check', '--ephemeral', '--json', '--sandbox', 'read-only', `Reply only ${marker}. Do not use tools.`];
  let output;
  try { output = await run(process.execPath, args, env, workspace); }
  finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
  const result = { number, platform: process.platform, node: process.version, home, port, version, authMode: JSON.parse(auth).auth_mode, login, argv: args.slice(1), modelSource: 'scratch config.toml; no CLI model override', output, requests };
  writeFileSync(join(evidence, `route-${number}.json`), JSON.stringify(result, null, 2));
  console.log(JSON.stringify({ ...result, requests: requests.map(request => ({ ...request, body: request.body ? { model: request.body.model, stream: request.body.stream } : null })) }));
  assert.equal(output.code, 0, output.stderr);
  assert.ok(output.stdout.includes(marker), 'Codex must consume the complete scripted SSE reply');
  const modelRequests = requests.filter(request => request.method === 'POST');
  assert.equal(modelRequests.length, 1, 'exactly one scripted model request');
  const request = modelRequests[0];
  assert.equal(request.path, '/w/codex/v1/responses');
  assert.equal(request.headers.authorization, `Bearer ${key}`);
  assert.equal(request.headers['x-cave-agent'], undefined, 'current Codex wrapper attributes through its /w/codex path, not a custom header');
  assert.equal(request.body.model, 'gpt-5.5');
  assert.equal(request.body.stream, true);
  assert.equal(readFileSync(join(env.CODEX_HOME, 'config.toml'), 'utf8'), config);
  assert.equal(readFileSync(join(env.CODEX_HOME, 'auth.json'), 'utf8'), auth);
  results.push(result);
}
assert.notEqual(results[0].home, results[1].home);
assert.notEqual(results[0].port, results[1].port);
writeFileSync(join(evidence, 'route-summary.json'), JSON.stringify({ passed: 2, platform: process.platform, node: process.version, version: pinned, model: 'gpt-5.5', method: 'POST', path: '/w/codex/v1/responses', bearer: 'dummy-only', xCaveAgent: null, attribution: 'URL /w/codex', reply: 'scripted SSE consumed', exits: results.map(result => result.output.code), scope: 'actual CLI wrap; API-key path; proxy/MCP/shrink/browse disabled' }, null, 2));
