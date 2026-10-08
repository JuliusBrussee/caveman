'use strict';

// Cursor native install, run after `npx skills add -a cursor` put the skills in
// ~/.cursor/skills:
//
//   ~/.cursor/agents/cavecrew-*.md  subagents (IDE, Agents Window and CLI all
//                                   read this directory on every OS)
//   ~/.cursor/caveman/...           the shared sessionStart hook payload
//   ~/.cursor/hooks.json            one sessionStart entry pointing at it
//
// Every file goes through the ownership journal. hooks.json is a shared user
// file: only the entry naming our hook script is ever added or removed.
// https://cursor.com/docs/agent/subagents  https://cursor.com/docs/agent/hooks

const fs = require('fs');
const os = require('os');
const path = require('path');

const OWNED = require('./owned-install');
const SETTINGS = require('./settings');
const HOST_HOOKS = require('./host-hooks');
const { transformOpencodeAgentFrontmatter } = require('./opencode-agent');

const INTEGRATION = 'cursor';
const CURSOR_AGENT_SPECS = [
  { file: 'cavecrew-investigator.md', readonly: true },
  { file: 'cavecrew-builder.md', readonly: false },
  { file: 'cavecrew-reviewer.md', readonly: true },
];

function cursorConfigDir(home) {
  return path.join(home, '.cursor');
}

// Same Claude-only fields opencode rejects: a provider-less `model: haiku` is
// not a Cursor model id (without it Cursor inherits the parent model). Cursor's
// `readonly` goes on the two agents whose prompts already refuse edits.
function transformCursorAgentFrontmatter(content, { readonly = false } = {}) {
  const out = transformOpencodeAgentFrontmatter(content);
  if (!readonly || !out.startsWith('---\n')) return out;
  return out.replace('\n---', '\nreadonly: true\n---');
}

function hooksJsonPath(root) {
  return path.join(root, 'hooks.json');
}

function readHooksJson(file) {
  const meta = {};
  const config = SETTINGS.readSettings(file, meta);
  if (!config || typeof config !== 'object' || Array.isArray(config)) {
    throw new Error(`${file} is not a JSON object; left untouched`);
  }
  // writeSettings emits plain JSON, which would silently drop the comments.
  if (meta.jsonc) {
    throw new Error(`${file} has comments; left untouched. Add or remove caveman's sessionStart entry by hand`);
  }
  if (config.version !== undefined && config.version !== 1) {
    throw new Error(`${file} has unsupported version ${JSON.stringify(config.version)}; left untouched`);
  }
  if (config.hooks !== undefined && (!config.hooks || typeof config.hooks !== 'object' || Array.isArray(config.hooks))) {
    throw new Error(`${file} hooks is not an object; left untouched`);
  }
  const list = config.hooks && config.hooks.sessionStart;
  if (list !== undefined && !Array.isArray(list)) {
    throw new Error(`${file} hooks.sessionStart is not an array; left untouched`);
  }
  return config;
}

function isOurs(entry, root) {
  return !!entry && typeof entry.command === 'string' && entry.command.includes(HOST_HOOKS.hookScriptPath(root));
}

function mergeSessionStartHook(root, command) {
  const file = hooksJsonPath(root);
  const config = readHooksJson(file);
  const list = (config.hooks && config.hooks.sessionStart) || [];
  if (list.some((entry) => isOurs(entry, root) && entry.command === command)) return;
  config.version = 1;
  config.hooks = { ...config.hooks, sessionStart: [...list.filter((entry) => !isOurs(entry, root)), { command, timeout: 10 }] };
  SETTINGS.writeSettings(file, config);
}

function unmergeSessionStartHook(root) {
  const file = hooksJsonPath(root);
  if (!fs.existsSync(file)) return;
  const config = readHooksJson(file);
  const list = (config.hooks && config.hooks.sessionStart) || [];
  if (!list.some((entry) => isOurs(entry, root))) return;
  const rest = list.filter((entry) => !isOurs(entry, root));
  if (rest.length) config.hooks.sessionStart = rest;
  else delete config.hooks.sessionStart;
  // A file that now says nothing was ours to begin with.
  if (Object.keys(config.hooks).length === 0 && Object.keys(config).every((key) => key === 'version' || key === 'hooks')) {
    fs.unlinkSync(file);
  } else {
    SETTINGS.writeSettings(file, config);
  }
}

function installCursorNative({
  repoRoot, home = os.homedir(), node = process.execPath, withHooks = true,
  force = false, dryRun = false, note = () => {},
}) {
  const root = cursorConfigDir(home);
  const operations = CURSOR_AGENT_SPECS.map(({ file, readonly }) => {
    const body = transformCursorAgentFrontmatter(fs.readFileSync(path.join(repoRoot, 'agents', file), 'utf8'), { readonly });
    return { relativePath: `agents/${file}`, write: (stage) => fs.writeFileSync(stage, body) };
  });
  if (withHooks) {
    const command = HOST_HOOKS.hookCommand(root, 'cursor', node);
    operations.push({ ...HOST_HOOKS.payloadOperation(repoRoot), register: () => mergeSessionStartHook(root, command) });
  }
  if (dryRun) {
    for (const operation of operations) note(`  would install ${path.join(root, operation.relativePath)}`);
    if (withHooks) note(`  would add a sessionStart hook to ${hooksJsonPath(root)}`);
    return;
  }
  OWNED.installOwned({ root, integration: INTEGRATION, operations, force, note });
  if (withHooks) note(`  sessionStart hook registered in ${hooksJsonPath(root)}`);
  note('  open a new Cursor chat to load the agents and the hook');
}

function uninstallCursorNative({ home = os.homedir(), dryRun = false, note = () => {}, warn = () => {} }) {
  const root = cursorConfigDir(home);
  const payload = path.join(root, HOST_HOOKS.PAYLOAD_DIR);
  return OWNED.uninstallOwned({
    root, integration: INTEGRATION, dryRun, note, warn,
    unregister: (target) => { if (target === payload) unmergeSessionStartHook(root); },
  });
}

module.exports = {
  CURSOR_AGENT_SPECS,
  cursorConfigDir,
  transformCursorAgentFrontmatter,
  installCursorNative,
  uninstallCursorNative,
};
