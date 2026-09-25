'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const child_process = require('child_process');

const OWNED = require('./owned-install');
const PORTABLE = require('./portable-process');
const { loadRuleBody } = require('./caveman-skill-body');
const CURSOR_MCP = require('./cursor-mcp-json');

const INTEGRATION = 'cursor';
const HOOK_SCRIPT_REL = 'hooks/caveman-session-start.js';
const HOOK_COMMAND = './hooks/caveman-session-start.js';
const RULE_REL = 'rules/caveman.mdc';
const HOOKS_JSON_REL = 'hooks.json';
const MERGE_JOURNAL_REL = '.caveman-cursor-merge.json';
const MCP_SHRINK_PKG = 'caveman-shrink';

const RULE_DESCRIPTION =
  'Caveman mode — terse communication that preserves technical substance and exact code/errors';

function cursorConfigDir(home) {
  return path.join(home, '.cursor');
}

function cavemanHome(home = os.homedir()) {
  return path.join(home, '.caveman');
}

function hasCmd(cmd) {
  try {
    if (process.platform === 'win32') {
      return PORTABLE.resolveWindowsCommand(cmd, process.env) !== null;
    }
    const r = child_process.spawnSync('sh', ['-c', `command -v '${String(cmd).replace(/'/g, `'\\''`)}'`], { stdio: 'ignore' });
    return r.status === 0;
  } catch (_) {
    return false;
  }
}

function isExecutable(file) {
  try {
    const stat = fs.statSync(file);
    if (!stat.isFile()) return false;
    if (process.platform === 'win32') return true;
    fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch (_) {
    return false;
  }
}

function resolveBinary(name, envVar, home) {
  const explicit = process.env[envVar];
  if (explicit && isExecutable(explicit)) return explicit;
  if (hasCmd(name)) {
    if (process.platform === 'win32') {
      const win = PORTABLE.resolveWindowsCommand(name, process.env);
      if (win) return win;
    } else {
      const r = child_process.spawnSync('sh', ['-c', `command -v '${String(name).replace(/'/g, `'\\''`)}'`], { encoding: 'utf8' });
      if (r.status === 0 && r.stdout.trim()) return r.stdout.trim();
    }
  }
  const local = path.join(cavemanHome(home), 'bin', process.platform === 'win32' ? `${name}.exe` : name);
  if (fs.existsSync(local) && isExecutable(local)) return local;
  return null;
}

function resolveMcpCommand(home) {
  const bin = resolveBinary('caveman-mcp', 'CAVEMAN_MCP_BIN', home);
  if (!bin) return null;
  return { command: bin, args: [] };
}

function resolveCavememMcpCommand(home) {
  const bin = resolveBinary('cavemem', 'CAVEMEM_BIN', home);
  if (!bin) return null;
  return { command: bin, args: ['mcp'] };
}

function resolveShrinkMcpCommand(upstreamTokens) {
  if (!Array.isArray(upstreamTokens) || upstreamTokens.length === 0) return null;
  if (!hasCmd('npx')) return null;
  return { command: 'npx', args: ['-y', MCP_SHRINK_PKG, ...upstreamTokens] };
}

function buildUserRuleMdc(ruleBody) {
  return `---
description: "${RULE_DESCRIPTION}"
alwaysApply: true
---

${ruleBody}`;
}

function buildSessionHookScript(ruleBody) {
  const primer = JSON.stringify(ruleBody.trim());
  return `#!/usr/bin/env node
'use strict';
// Caveman sessionStart hook — fail-open primer only (no savings claims).
const primer = ${primer};
function finish(payload) {
  try { process.stdout.write(JSON.stringify(payload)); } catch (_) { process.stdout.write('{}'); }
  process.exit(0);
}
let input = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => { input += chunk; });
process.stdin.on('end', () => {
  try { finish({ additional_context: primer }); }
  catch (_) { finish({}); }
});
process.stdin.on('error', () => finish({}));
`;
}

function readJsonObject(filePath) {
  try {
    const raw = fs.readFileSync(filePath, 'utf8').trim();
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error('not a JSON object');
    }
    return parsed;
  } catch (error) {
    if (error.code === 'ENOENT') return {};
    throw error;
  }
}

function writeJsonObject(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true, mode: 0o700 });
  fs.writeFileSync(filePath, JSON.stringify(value, null, 2) + '\n', { mode: 0o600 });
}

const readMcpMarker = CURSOR_MCP.readMcpMarker;
const mergeMcpServer = CURSOR_MCP.mergeMcpServer;
const unmergeMcpServer = CURSOR_MCP.unmergeMcpServer;

function mergeSessionStartHook(root) {
  const filePath = path.join(root, HOOKS_JSON_REL);
  let rootObj;
  try {
    rootObj = readJsonObject(filePath);
  } catch (error) {
    throw new Error(`${filePath}: ${error.message}`);
  }
  if (rootObj.version !== undefined && rootObj.version !== 1) {
    throw new Error(`${filePath} hooks.version must be 1 when present`);
  }
  rootObj.version = 1;
  if (!rootObj.hooks || typeof rootObj.hooks !== 'object' || Array.isArray(rootObj.hooks)) {
    rootObj.hooks = {};
  }
  const list = Array.isArray(rootObj.hooks.sessionStart) ? [...rootObj.hooks.sessionStart] : [];
  const already = list.some((entry) => entry && typeof entry.command === 'string'
    && entry.command.includes('caveman-session-start'));
  if (!already) list.push({ command: HOOK_COMMAND });
  rootObj.hooks.sessionStart = list;
  writeJsonObject(filePath, rootObj);
}

function unmergeSessionStartHook(root) {
  const filePath = path.join(root, HOOKS_JSON_REL);
  let rootObj;
  try {
    rootObj = readJsonObject(filePath);
  } catch (_) {
    return;
  }
  if (!rootObj || !rootObj.hooks || !Array.isArray(rootObj.hooks.sessionStart)) return;
  const next = rootObj.hooks.sessionStart.filter((entry) => !(entry && typeof entry.command === 'string'
    && entry.command.includes('caveman-session-start')));
  if (next.length === rootObj.hooks.sessionStart.length) return;
  if (next.length === 0) delete rootObj.hooks.sessionStart;
  else rootObj.hooks.sessionStart = next;
  if (rootObj.hooks && Object.keys(rootObj.hooks).length === 0) delete rootObj.hooks;
  if (Object.keys(rootObj).length === 0) {
    try { fs.unlinkSync(filePath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  } else {
    writeJsonObject(filePath, rootObj);
  }
}

function loadMergeJournal(root) {
  const filePath = path.join(root, MERGE_JOURNAL_REL);
  try {
    const parsed = JSON.parse(fs.readFileSync(filePath, 'utf8'));
    if (parsed?.version !== 1) return { version: 1, mcpServers: [], sessionStartHook: false };
    return {
      version: 1,
      mcpServers: Array.isArray(parsed.mcpServers) ? parsed.mcpServers : [],
      sessionStartHook: parsed.sessionStartHook === true,
    };
  } catch (error) {
    if (error.code === 'ENOENT') return { version: 1, mcpServers: [], sessionStartHook: false };
    throw error;
  }
}

function writeMergeJournal(root, journal) {
  if (!journal.mcpServers.length && !journal.sessionStartHook) {
    try { fs.unlinkSync(path.join(root, MERGE_JOURNAL_REL)); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    return;
  }
  writeJsonObject(path.join(root, MERGE_JOURNAL_REL), journal);
}

function installCursorNative({
  repoRoot,
  home = os.homedir(),
  force = false,
  dryRun = false,
  withMcpShrink = false,
  note = () => {},
  warn = () => {},
}) {
  const root = cursorConfigDir(home);
  const ruleBody = loadRuleBody(repoRoot);
  const ruleMdc = buildUserRuleMdc(ruleBody);
  const hookScript = buildSessionHookScript(ruleBody);

  if (dryRun) {
    note('  would install user rule, sessionStart hook, and MCP entries under ~/.cursor/');
    return { installed: false, dryRun: true };
  }

  const operations = [
    {
      relativePath: RULE_REL,
      write: (stage) => fs.writeFileSync(stage, ruleMdc, { mode: 0o600, flag: 'wx' }),
    },
    {
      relativePath: HOOK_SCRIPT_REL,
      write: (stage) => {
        fs.writeFileSync(stage, hookScript, { mode: 0o600, flag: 'wx' });
        try { fs.chmodSync(stage, 0o700); } catch (_) { /* Windows */ }
      },
    },
  ];
  OWNED.installOwned({ root, integration: INTEGRATION, operations, force, note });

  const mergeJournal = loadMergeJournal(root);
  try {
    mergeSessionStartHook(root);
    mergeJournal.sessionStartHook = true;
    note(`  wired sessionStart hook in ${path.join(root, HOOKS_JSON_REL)}`);
  } catch (error) {
    warn(`  Cursor sessionStart hook was not installed: ${error.message}`);
  }

  const mcpInstalled = [];
  const cavemanMcp = resolveMcpCommand(home);
  if (cavemanMcp) {
    if (mergeMcpServer(root, home, 'caveman', cavemanMcp, force, warn)) {
      mcpInstalled.push('caveman');
      note('  registered caveman MCP (caveman_retrieve)');
    }
  } else {
    note('  caveman-mcp not found — run `caveman setup --install` then re-run `--only cursor` for retrieve');
  }

  const cavememMcp = resolveCavememMcpCommand(home);
  if (cavememMcp) {
    if (mergeMcpServer(root, home, 'cavemem', cavememMcp, force, warn)) {
      mcpInstalled.push('cavemem');
      note('  registered cavemem MCP');
    }
  } else {
    note('  cavemem not found — memory MCP skipped (optional)');
  }

  if (withMcpShrink) {
    const shrink = resolveShrinkMcpCommand(withMcpShrink);
    if (shrink) {
      if (mergeMcpServer(root, home, 'caveman-shrink', shrink, force, warn)) {
        mcpInstalled.push('caveman-shrink');
        note(`  registered caveman-shrink MCP (wraps: ${withMcpShrink.join(' ')})`);
      }
    } else {
      warn('  npx not found — caveman-shrink MCP was not registered');
    }
  }

  mergeJournal.mcpServers = [...new Set([...mergeJournal.mcpServers, ...mcpInstalled])];
  writeMergeJournal(root, mergeJournal);
  return { installed: true, mcpInstalled };
}

function uninstallCursorNative({
  home = os.homedir(),
  dryRun = false,
  note = () => {},
  warn = () => {},
}) {
  const root = cursorConfigDir(home);
  const journal = loadMergeJournal(root);
  if (dryRun) {
    note('  would remove Cursor user rule, hook script, merged MCP/hook entries');
    return;
  }
  if (journal.sessionStartHook) {
    unmergeSessionStartHook(root);
    note('  removed caveman sessionStart hook entry');
  }
  const servers = new Set([...journal.mcpServers, 'caveman', 'cavemem', 'caveman-shrink']);
  for (const serverName of servers) {
    if (!readMcpMarker(home, serverName)) continue;
    unmergeMcpServer(root, home, serverName);
    note(`  removed mcpServers.${serverName} when Caveman-owned`);
  }
  writeMergeJournal(root, { version: 1, mcpServers: [], sessionStartHook: false });
}

const installCursorMcpJson = CURSOR_MCP.installCursorMcpJson;

function uninstallCursorMcpJson(serverName = 'caveman', { home = os.homedir() } = {}) {
  CURSOR_MCP.uninstallCursorMcpJson(serverName, { home });
  const journal = loadMergeJournal(cursorConfigDir(home));
  journal.mcpServers = journal.mcpServers.filter((name) => name !== serverName);
  writeMergeJournal(cursorConfigDir(home), journal);
  return true;
}

module.exports = {
  HOOK_COMMAND,
  buildSessionHookScript,
  buildUserRuleMdc,
  cursorConfigDir,
  installCursorMcpJson,
  installCursorNative,
  loadRuleBody,
  mergeMcpServer,
  mergeSessionStartHook,
  resolveMcpCommand,
  unmergeMcpServer,
  unmergeSessionStartHook,
  uninstallCursorMcpJson,
  uninstallCursorNative,
};
