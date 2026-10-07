#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const STATE_VERSION = 1;
const HOOK_SCRIPT_NAME = 'cursor-dedupe-tools.js';

function defaultStateDir(env = process.env) {
  const home = env.HOME || env.USERPROFILE || os.homedir();
  return path.join(home, '.caveman');
}

function stateFilePath(stateDir) {
  return path.join(stateDir, 'cursor-dedupe-tools-state.json');
}

function emptyState() {
  return { version: STATE_VERSION, conversations: {}, fingerprints: {} };
}

function loadState(stateDir) {
  try {
    const raw = JSON.parse(fs.readFileSync(stateFilePath(stateDir), 'utf8'));
    return {
      version: STATE_VERSION,
      conversations: raw.conversations || {},
      fingerprints: raw.fingerprints || {},
    };
  } catch {
    return emptyState();
  }
}

function saveState(stateDir, state) {
  fs.mkdirSync(stateDir, { recursive: true });
  fs.writeFileSync(stateFilePath(stateDir), `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
}

function normalizePath(filePath, cwd = process.cwd()) {
  if (!filePath) return '';
  const resolved = path.isAbsolute(filePath) ? filePath : path.resolve(cwd, filePath);
  return process.platform === 'win32' ? resolved.toLowerCase() : resolved;
}

function conversationId(input) {
  return input.conversation_id || input.conversationId || input.session_id || input.sessionId || 'default';
}

function toolInput(input) {
  return input.tool_input || input.toolInput || input.input || input;
}

function readPath(input) {
  const ti = toolInput(input);
  return ti.path || ti.file_path || ti.filePath;
}

function isRangedRead(toolInputValue) {
  if (!toolInputValue || typeof toolInputValue !== 'object') return false;
  const offset = toolInputValue.offset;
  const limit = toolInputValue.limit;
  const hasOffset = offset !== undefined && offset !== null && offset !== '';
  const hasLimit = limit !== undefined && limit !== null && limit !== '';
  return hasOffset || hasLimit;
}

function fileFingerprint(filePath) {
  const stat = fs.statSync(filePath);
  const hash = crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
  return { mtimeMs: stat.mtimeMs, size: stat.size, hash };
}

function convState(state, convId) {
  if (!state.conversations[convId]) {
    state.conversations[convId] = { reads: {}, shell: {}, crossWarned: {} };
  }
  return state.conversations[convId];
}

function fingerprintsMatch(a, b) {
  return a && b && a.hash === b.hash && a.mtimeMs === b.mtimeMs && a.size === b.size;
}

function decideRead(input, options = {}) {
  const stateDir = options.stateDir || defaultStateDir(options.env);
  const state = options.state || loadState(stateDir);
  const cwd = options.cwd || process.cwd();
  const convId = conversationId(input);
  const ti = toolInput(input);
  const filePath = readPath(input);

  if (!filePath) return { permission: 'allow', state };
  if (isRangedRead(ti)) return { permission: 'allow', state };

  const norm = normalizePath(filePath, cwd);
  if (!fs.existsSync(norm)) return { permission: 'allow', state };

  let fp;
  try {
    fp = fileFingerprint(norm);
  } catch {
    return { permission: 'allow', state };
  }

  const c = convState(state, convId);
  const seen = c.reads[norm];
  if (seen && seen.mtimeMs === fp.mtimeMs && seen.hash === fp.hash) {
    return {
      permission: 'deny',
      agent_message: 'Already read this unchanged file in this chat. Use the earlier tool result or request a line range (offset/limit).',
      state,
    };
  }

  const stored = state.fingerprints[norm];
  if (fingerprintsMatch(stored, fp) && !seen && !c.crossWarned[norm]) {
    c.crossWarned[norm] = true;
    return {
      permission: 'deny',
      agent_message: 'File unchanged since a previous chat. Request a line range (offset/limit) unless you need the whole file.',
      state,
    };
  }

  c.reads[norm] = { mtimeMs: fp.mtimeMs, hash: fp.hash };
  state.fingerprints[norm] = fp;
  return { permission: 'allow', state };
}

function shellKey(command, cwd = process.cwd()) {
  const t = String(command || '').trim().replace(/\s+/g, ' ');
  if (!t) return null;
  if (/^npm test(\s|$)/.test(t)) return 'npm test';
  if (/^go test \.\/\.\.\./.test(t)) return 'go test ./...';
  const nodeTest = t.match(/^node --test(?:\s+)(.+)$/);
  if (nodeTest) {
    const target = path.isAbsolute(nodeTest[1]) ? nodeTest[1] : path.resolve(cwd, nodeTest[1]);
    const keyPath = process.platform === 'win32' ? target.toLowerCase() : target;
    return `node --test ${keyPath}`;
  }
  return null;
}

function decideShell(input, options = {}) {
  const stateDir = options.stateDir || defaultStateDir(options.env);
  const state = options.state || loadState(stateDir);
  const convId = conversationId(input);
  const command = input.command || input.shell_command || toolInput(input).command;
  const key = shellKey(command, options.cwd);
  if (!key) return { permission: 'allow', state };

  const c = convState(state, convId);
  if (c.shell[key]) {
    return {
      permission: 'deny',
      agent_message: `Already ran \`${key}\` in this chat. Use the earlier output or run a narrower test file.`,
      state,
    };
  }
  c.shell[key] = true;
  return { permission: 'allow', state };
}

function formatResponse(result) {
  if (result.permission === 'deny') {
    return JSON.stringify({
      permission: 'deny',
      agent_message: result.agent_message,
    });
  }
  return JSON.stringify({ permission: 'allow' });
}

async function main() {
  const mode = process.argv[2] === 'shell' ? 'shell' : 'read';
  let input = {};
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    const text = Buffer.concat(chunks).toString('utf8').trim();
    if (text) input = JSON.parse(text);
  } catch {
    process.stdout.write(formatResponse({ permission: 'allow' }));
    return;
  }

  try {
    const stateDir = defaultStateDir();
    const result = mode === 'shell'
      ? decideShell(input, { stateDir })
      : decideRead(input, { stateDir });
    if (!process.env.CAVEMAN_CURSOR_HOOK_NO_SAVE) saveState(stateDir, result.state);
    process.stdout.write(formatResponse(result));
  } catch {
    process.stdout.write(formatResponse({ permission: 'allow' }));
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  HOOK_SCRIPT_NAME,
  decideRead,
  decideShell,
  loadState,
  saveState,
  stateFilePath,
  defaultStateDir,
  shellKey,
};
