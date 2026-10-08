#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { execSync } = require('child_process');

const STATE_VERSION = 1;
const HOOK_SCRIPT_NAME = 'cursor-dedupe-tools.js';
const MAX_CONVERSATIONS = 50;
const TREE_MTIME_MAX_FILES = 500;

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

function trimConversations(state, max = MAX_CONVERSATIONS) {
  const ids = Object.keys(state.conversations);
  if (ids.length <= max) return state;
  const next = { ...state, conversations: { ...state.conversations } };
  for (let i = 0; i < ids.length - max; i++) delete next.conversations[ids[i]];
  return next;
}

function saveState(stateDir, state) {
  fs.mkdirSync(stateDir, { recursive: true });
  const trimmed = trimConversations(state);
  fs.writeFileSync(stateFilePath(stateDir), `${JSON.stringify(trimmed, null, 2)}\n`, { mode: 0o600 });
  return trimmed;
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

function parseReadRange(toolInputValue) {
  const start = toolInputValue.offset !== undefined && toolInputValue.offset !== null && toolInputValue.offset !== ''
    ? Math.max(1, Number(toolInputValue.offset) || 1)
    : 1;
  const limit = toolInputValue.limit;
  const hasLimit = limit !== undefined && limit !== null && limit !== '';
  const end = hasLimit ? start + (Number(limit) || 0) - 1 : Number.MAX_SAFE_INTEGER;
  return { start, end, full: !isRangedRead(toolInputValue) };
}

function rangeContained(requested, stored) {
  if (!stored || !Array.isArray(stored.ranges)) return false;
  return stored.ranges.some((r) => requested.start >= r.start && requested.end <= r.end);
}

function mergeReadRange(stored, range) {
  const ranges = stored && Array.isArray(stored.ranges) ? [...stored.ranges] : [];
  ranges.push(range);
  return ranges;
}

function fileFingerprint(filePath) {
  const stat = fs.statSync(filePath);
  const hash = crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
  return { mtimeMs: stat.mtimeMs, size: stat.size, hash };
}

function convState(state, convId) {
  if (!state.conversations[convId]) {
    state.conversations[convId] = { reads: {}, shell: {}, crossWarned: {}, searches: {} };
  }
  const c = state.conversations[convId];
  if (!c.searches) c.searches = {};
  return c;
}

function fingerprintsMatch(a, b) {
  return a && b && a.hash === b.hash && a.mtimeMs === b.mtimeMs && a.size === b.size;
}

function recordRead(c, norm, fp, range) {
  const prev = c.reads[norm];
  if (prev && prev.hash === fp.hash && prev.mtimeMs === fp.mtimeMs) {
    c.reads[norm] = {
      ...prev,
      ranges: mergeReadRange(prev, range),
      full: prev.full === true || range.full === true,
    };
    return;
  }
  c.reads[norm] = {
    mtimeMs: fp.mtimeMs,
    hash: fp.hash,
    ranges: [range],
    full: range.full === true,
  };
}

function decideRead(input, options = {}) {
  const stateDir = options.stateDir || defaultStateDir(options.env);
  const state = options.state || loadState(stateDir);
  const cwd = options.cwd || process.cwd();
  const convId = conversationId(input);
  const ti = toolInput(input);
  const filePath = readPath(input);

  if (!filePath) return { permission: 'allow', state };

  const norm = normalizePath(filePath, cwd);
  if (!fs.existsSync(norm)) return { permission: 'allow', state };

  let fp;
  try {
    fp = fileFingerprint(norm);
  } catch {
    return { permission: 'allow', state };
  }

  const c = convState(state, convId);
  const ranged = isRangedRead(ti);

  if (ranged) {
    const requested = parseReadRange(ti);
    const seen = c.reads[norm];
    if (seen && seen.hash === fp.hash && seen.mtimeMs === fp.mtimeMs && rangeContained(requested, seen)) {
      return {
        permission: 'deny',
        agent_message: 'Already read this line range from this unchanged file in this chat. Use the earlier tool result or request lines outside that range.',
        state,
      };
    }
    recordRead(c, norm, fp, { start: requested.start, end: requested.end, full: false });
    state.fingerprints[norm] = fp;
    return { permission: 'allow', state };
  }

  const seen = c.reads[norm];
  if (seen && seen.mtimeMs === fp.mtimeMs && seen.hash === fp.hash && seen.full) {
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

  recordRead(c, norm, fp, { start: 1, end: Number.MAX_SAFE_INTEGER, full: true });
  state.fingerprints[norm] = fp;
  return { permission: 'allow', state };
}

function normalizeSearchText(value) {
  return String(value || '').trim().replace(/\s+/g, ' ');
}

function searchRoot(ti, cwd) {
  const raw = ti.path || ti.target_directory || ti.glob_pattern || '.';
  return normalizePath(raw, cwd);
}

function searchQueryKey(toolKind, ti, cwd) {
  const pattern = normalizeSearchText(ti.pattern || ti.query || ti.glob_pattern || '');
  const glob = normalizeSearchText(ti.glob || '');
  const root = searchRoot(ti, cwd);
  return `${toolKind}:${pattern}:${root}:${glob}`;
}

function searchTreeSnapshot(rootPath, options = {}) {
  const maxFiles = options.maxFiles || TREE_MTIME_MAX_FILES;
  if (!rootPath || !fs.existsSync(rootPath)) return null;
  let newest;
  let sizeSum = 0;
  let count = 0;
  const visit = (p) => {
    if (count >= maxFiles) return;
    let st;
    try {
      st = fs.statSync(p);
    } catch {
      return;
    }
    count++;
    if (st.isDirectory()) {
      let entries;
      try {
        entries = fs.readdirSync(p, { withFileTypes: true });
      } catch {
        return;
      }
      for (const ent of entries) {
        if (count >= maxFiles) return;
        if (ent.name === 'node_modules' || ent.name === '.git') continue;
        visit(path.join(p, ent.name));
      }
      return;
    }
    sizeSum += st.size;
    if (newest === undefined || st.mtimeMs > newest) newest = st.mtimeMs;
  };
  visit(rootPath);
  return newest === undefined ? null : { mtimeMs: newest, sizeSum };
}

function newestMtimeUnder(rootPath, options = {}) {
  const snapshot = searchTreeSnapshot(rootPath, options);
  return snapshot ? snapshot.mtimeMs : null;
}

function decideSearch(input, toolKind, options = {}) {
  const stateDir = options.stateDir || defaultStateDir(options.env);
  const state = options.state || loadState(stateDir);
  const cwd = options.cwd || process.cwd();
  const convId = conversationId(input);
  const ti = toolInput(input);
  const key = searchQueryKey(toolKind, ti, cwd);
  const root = searchRoot(ti, cwd);

  let snapshot;
  try {
    snapshot = searchTreeSnapshot(fs.existsSync(root) ? root : cwd, options);
  } catch {
    return { permission: 'allow', state };
  }
  if (snapshot === null) return { permission: 'allow', state };

  const c = convState(state, convId);
  const seen = c.searches[key];
  if (seen && seen.treeMtime === snapshot.mtimeMs && seen.treeSize === snapshot.sizeSum) {
    return {
      permission: 'deny',
      agent_message: `Already ran this ${toolKind} on an unchanged tree in this chat. Use the earlier tool result or change the pattern/path.`,
      state,
    };
  }

  c.searches[key] = { treeMtime: snapshot.mtimeMs, treeSize: snapshot.sizeSum };
  return { permission: 'allow', state };
}

function decideGrep(input, options = {}) {
  return decideSearch(input, 'grep', options);
}

function decideGlob(input, options = {}) {
  return decideSearch(input, 'glob', options);
}

const EXACT_GIT_COMMANDS = new Set(['git status', 'git diff', 'git log -n']);

function gitWorktreeFingerprint(cwd = process.cwd()) {
  try {
    const head = execSync('git rev-parse HEAD', { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
    const porcelain = execSync('git status --porcelain', {
      cwd,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    return `${head}:${porcelain}`;
  } catch {
    return null;
  }
}

function shellKey(command, cwd = process.cwd()) {
  const t = String(command || '').trim().replace(/\s+/g, ' ');
  if (!t) return null;
  if (EXACT_GIT_COMMANDS.has(t)) return t;
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
  const cwd = options.cwd || process.cwd();
  const command = input.command || input.shell_command || toolInput(input).command;
  const key = shellKey(command, cwd);
  if (!key) return { permission: 'allow', state };

  const c = convState(state, convId);
  if (EXACT_GIT_COMMANDS.has(key)) {
    const fp = gitWorktreeFingerprint(cwd);
    if (!fp) return { permission: 'allow', state };
    if (c.shell[key] === fp) {
      return {
        permission: 'deny',
        agent_message: `Already ran \`${key}\` on this unchanged worktree in this chat. Use the earlier output.`,
        state,
      };
    }
    c.shell[key] = fp;
    return { permission: 'allow', state };
  }

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
  const mode = process.argv[2] || 'read';
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
    let result;
    if (mode === 'shell') result = decideShell(input, { stateDir });
    else if (mode === 'grep') result = decideGrep(input, { stateDir });
    else if (mode === 'glob') result = decideGlob(input, { stateDir });
    else result = decideRead(input, { stateDir });
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
  MAX_CONVERSATIONS,
  decideRead,
  decideShell,
  decideGrep,
  decideGlob,
  decideSearch,
  loadState,
  saveState,
  trimConversations,
  stateFilePath,
  defaultStateDir,
  shellKey,
  gitWorktreeFingerprint,
  newestMtimeUnder,
  searchTreeSnapshot,
  searchQueryKey,
};
