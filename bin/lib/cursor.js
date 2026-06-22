// caveman — Cursor CLI/IDE hook installer.
//
// Wires SessionStart + UserPromptSubmit parity via ~/.cursor/hooks.json and
// optional statusLine in ~/.cursor/cli-config.json.
//
// Pure stdlib, CommonJS.

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');

const HOOK_SCRIPT_NAMES = [
  'caveman-config.js',
  'caveman-activate.js',
  'caveman-mode-tracker.js',
  'caveman-stats.js',
  'caveman-statusline.sh',
];

const ADAPTER_NAMES = [
  'caveman-session-start.js',
  'caveman-prompt-submit.js',
];

const CAVEMAN_MARKER = 'caveman-session-start.js';

function cursorConfigDir() {
  return path.join(os.homedir(), '.cursor');
}

function cursorHooksDir() {
  return path.join(cursorConfigDir(), 'hooks');
}

function cursorHooksJsonPath() {
  return path.join(cursorConfigDir(), 'hooks.json');
}

function cursorCliConfigPath() {
  return path.join(cursorConfigDir(), 'cli-config.json');
}

function repoHooksDir(repoRoot) {
  return path.join(repoRoot, 'src', 'hooks');
}

function repoCursorDir(repoRoot) {
  return path.join(repoRoot, 'src', 'hooks', 'cursor');
}

function readJsonFile(p) {
  if (!fs.existsSync(p)) return {};
  try {
    return JSON.parse(fs.readFileSync(p, 'utf8'));
  } catch (_) {
    return null;
  }
}

function writeJsonFile(p, obj) {
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, JSON.stringify(obj, null, 2) + '\n', { mode: 0o644 });
}

function mergeCursorHooks(existing) {
  const base = existing && typeof existing === 'object' ? existing : { version: 1, hooks: {} };
  if (!base.hooks || typeof base.hooks !== 'object') base.hooks = {};
  base.version = 1;
  base.hooks.sessionStart = [{ command: './hooks/caveman-session-start.js' }];
  base.hooks.beforeSubmitPrompt = [{ command: './hooks/caveman-prompt-submit.js' }];
  return base;
}

function hooksJsonHasCaveman(obj) {
  if (!obj || !obj.hooks) return false;
  const events = ['sessionStart', 'beforeSubmitPrompt'];
  return events.some((ev) => {
    const arr = obj.hooks[ev];
    if (!Array.isArray(arr)) return false;
    return arr.some((entry) => typeof entry.command === 'string' && entry.command.includes(CAVEMAN_MARKER));
  });
}

function installCursorHooks({ repoRoot, dryRun, log }) {
  const hooksSrc = repoHooksDir(repoRoot);
  const cursorSrc = repoCursorDir(repoRoot);
  const hooksDir = cursorHooksDir();
  const hooksJson = cursorHooksJsonPath();
  const cliConfig = cursorCliConfigPath();

  if (!fs.existsSync(hooksSrc) || !fs.existsSync(cursorSrc)) {
    return { kind: 'fail', why: 'repo hook sources missing (src/hooks or src/hooks/cursor)' };
  }

  if (dryRun) {
    log.note(`  would mkdir -p ${hooksDir}`);
    for (const f of [...HOOK_SCRIPT_NAMES, ...ADAPTER_NAMES]) log.note(`  would install ${path.join(hooksDir, f)}`);
    log.note(`  would merge caveman entries into ${hooksJson}`);
    log.note(`  would merge statusLine into ${cliConfig} (if absent)`);
    return { kind: 'ok' };
  }

  fs.mkdirSync(hooksDir, { recursive: true });

  for (const f of HOOK_SCRIPT_NAMES) {
    fs.copyFileSync(path.join(hooksSrc, f), path.join(hooksDir, f));
  }
  for (const f of ADAPTER_NAMES) {
    fs.copyFileSync(path.join(cursorSrc, f), path.join(hooksDir, f));
  }
  try { fs.chmodSync(path.join(hooksDir, 'caveman-statusline.sh'), 0o755); } catch (_) {}
  for (const f of ADAPTER_NAMES) {
    try { fs.chmodSync(path.join(hooksDir, f), 0o755); } catch (_) {}
  }

  const existingHooks = readJsonFile(hooksJson);
  if (existingHooks === null) {
    return { kind: 'fail', why: `${hooksJson} is not valid JSON` };
  }
  writeJsonFile(hooksJson, mergeCursorHooks(existingHooks));

  const cli = readJsonFile(cliConfig);
  if (cli === null) {
    return { kind: 'fail', why: `${cliConfig} is not valid JSON` };
  }
  if (!cli.statusLine) {
    cli.statusLine = {
      type: 'command',
      command: `bash ${path.join(hooksDir, 'caveman-statusline.sh')}`,
    };
    writeJsonFile(cliConfig, cli);
  }

  return { kind: 'ok' };
}

function uninstallCursorHooks({ dryRun, log }) {
  const hooksDir = cursorHooksDir();
  const hooksJson = cursorHooksJsonPath();

  if (dryRun) {
    log.note(`  would remove caveman hook scripts from ${hooksDir}`);
    log.note(`  would prune caveman entries from ${hooksJson}`);
    return { kind: 'ok' };
  }

  for (const f of [...HOOK_SCRIPT_NAMES, ...ADAPTER_NAMES]) {
    try { fs.unlinkSync(path.join(hooksDir, f)); } catch (_) {}
  }

  const existing = readJsonFile(hooksJson);
  if (existing && existing.hooks) {
    for (const ev of ['sessionStart', 'beforeSubmitPrompt']) {
      if (!Array.isArray(existing.hooks[ev])) continue;
      existing.hooks[ev] = existing.hooks[ev].filter(
        (entry) => !(typeof entry.command === 'string' && entry.command.includes('caveman'))
      );
      if (existing.hooks[ev].length === 0) delete existing.hooks[ev];
    }
    writeJsonFile(hooksJson, existing);
  }

  return { kind: 'ok' };
}

module.exports = {
  HOOK_SCRIPT_NAMES,
  ADAPTER_NAMES,
  CAVEMAN_MARKER,
  cursorConfigDir,
  cursorHooksDir,
  cursorHooksJsonPath,
  cursorCliConfigPath,
  mergeCursorHooks,
  hooksJsonHasCaveman,
  installCursorHooks,
  uninstallCursorHooks,
};
