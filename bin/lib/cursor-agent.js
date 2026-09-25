'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const OWNED = require('./owned-install');
const CURSOR_NATIVE = require('./cursor-native');

// Cursor custom subagents (editor, Agents Window, CLI, Cloud Agents) are
// markdown files in <home>/.cursor/agents/. Cursor documents that directory
// for every desktop OS. os.homedir() is %USERPROFILE% on Windows and $HOME
// on macOS and Linux.
// https://cursor.com/docs/agent/subagents

const CURSOR_AGENT_SPECS = [
  { file: 'cavecrew-investigator.md', readonly: true },
  { file: 'cavecrew-builder.md', readonly: false },
  { file: 'cavecrew-reviewer.md', readonly: true },
];

const TOOLS_FIELD_RE = /^tools[ \t]*:/;
const MODEL_FIELD_RE = /^model[ \t]*:[ \t]*(.*)$/;
const READONLY_FIELD_RE = /^readonly[ \t]*:/;
const CONTINUATION_RE = /^[ \t]/;
const FRONTMATTER_FENCE = '---\n';

function cursorConfigDir(home, pathImpl = path) {
  return pathImpl.join(home, '.cursor');
}

function modelValue(rawValue) {
  let value = rawValue.replace(/\s+#.*$/, '').trim();
  const quoted = /^(['"])([\s\S]*)\1$/.exec(value);
  if (quoted) value = quoted[2].trim();
  return value;
}

// Cursor model ids are `inherit` or a product model id. Claude aliases such
// as `haiku` are not in that set. Rewrite those to `inherit` so the IDE and
// the Agents Window use the parent model instead of a missing id.
function transformCursorAgentFrontmatter(content, options = {}) {
  const readonly = options.readonly === true;
  if (typeof content !== 'string' || !content.startsWith(FRONTMATTER_FENCE)) return content;
  const fmEnd = content.indexOf('\n---', FRONTMATTER_FENCE.length);
  if (fmEnd < 0) return content;

  const fm = content.slice(FRONTMATTER_FENCE.length, fmEnd);
  const rest = content.slice(fmEnd);
  const out = [];
  let dropping = false;
  let sawModel = false;
  let sawReadonly = false;

  for (const line of fm.split('\n')) {
    if (dropping) {
      if (CONTINUATION_RE.test(line)) continue;
      dropping = false;
    }
    if (TOOLS_FIELD_RE.test(line)) { dropping = true; continue; }
    if (READONLY_FIELD_RE.test(line)) sawReadonly = true;
    const model = MODEL_FIELD_RE.exec(line);
    if (model) {
      sawModel = true;
      const value = modelValue(model[1]);
      if (value !== '' && value !== 'inherit' && !value.includes('/')) {
        out.push('model: inherit');
        continue;
      }
    }
    out.push(line);
  }

  if (!sawModel) out.push('model: inherit');
  if (readonly && !sawReadonly) out.push('readonly: true');
  return FRONTMATTER_FENCE + out.join('\n') + rest;
}

function installCursorAgents({
  repoRoot,
  home = os.homedir(),
  force = false,
  dryRun = false,
  withMcpShrink = false,
  note = () => {},
  warn = () => {},
}) {
  const root = cursorConfigDir(home);
  const agentsDir = path.join(root, 'agents');
  if (!repoRoot) {
    note('  Skills were installed. Cavecrew agents were not copied because this installer package has no agents/ directory.');
    return { copied: false };
  }
  if (dryRun) {
    note(`  would copy ${CURSOR_AGENT_SPECS.length} cavecrew agents into ${agentsDir}`);
    note('  Cursor IDE, Agents Window, and CLI read this same user directory.');
    CURSOR_NATIVE.installCursorNative({ repoRoot, home, force, dryRun: true, withMcpShrink, note, warn });
    return { copied: false, dryRun: true };
  }

  const agentSrcDir = path.join(repoRoot, 'agents');
  const operations = [];
  for (const spec of CURSOR_AGENT_SPECS) {
    const src = path.join(agentSrcDir, spec.file);
    if (!fs.existsSync(src)) continue;
    const body = transformCursorAgentFrontmatter(fs.readFileSync(src, 'utf8'), { readonly: spec.readonly });
    operations.push({
      relativePath: `agents/${spec.file}`,
      write: (stage) => fs.writeFileSync(stage, body, { mode: 0o600, flag: 'wx' }),
    });
  }
  OWNED.installOwned({
    root,
    integration: 'cursor',
    operations,
    force,
    note,
  });
  note('  Cursor IDE, Agents Window, and CLI read this same user directory. Open a new chat to load the agents.');
  CURSOR_NATIVE.installCursorNative({
    repoRoot,
    home,
    force,
    dryRun: false,
    withMcpShrink,
    note,
    warn,
  });
  return { copied: true, count: operations.length };
}

function uninstallCursorAgents({
  home = os.homedir(),
  dryRun = false,
  note = () => {},
  warn = () => {},
}) {
  CURSOR_NATIVE.uninstallCursorNative({ home, dryRun, note, warn });
  return OWNED.uninstallOwned({
    root: cursorConfigDir(home),
    integration: 'cursor',
    dryRun,
    note,
    warn,
  });
}

module.exports = {
  CURSOR_AGENT_SPECS,
  cursorConfigDir,
  transformCursorAgentFrontmatter,
  installCursorAgents,
  uninstallCursorAgents,
};
