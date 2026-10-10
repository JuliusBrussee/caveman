// opencode native install — fresh install, idempotency, uninstall, plugin smoke.
//
// Detection of opencode is gated behind `command -v opencode`, so to run on a
// CI box without opencode installed we prepend a tmpdir with a no-op `opencode`
// shim to PATH. The installer's per-provider dispatch only checks PATH; it
// never invokes the binary itself.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';
import { isolatedEnv } from './_isolated-env.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'installer', 'install.js');
const requireCjs = createRequire(import.meta.url);
const SETTINGS = requireCjs(path.join(REPO_ROOT, 'installer', 'lib', 'settings.js'));
const MODE_LOG_BASENAME = '.caveman-mode-log.jsonl';

const IS_WIN = process.platform === 'win32';

function freshTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-opencode-'));
}

// Make a throwaway `opencode` binary on PATH so detectMatch('command:opencode')
// returns true. The shim never executes — installer only checks PATH presence.
function shimOpencode() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-shim-'));
  if (IS_WIN) {
    fs.writeFileSync(path.join(dir, 'opencode.cmd'), '@echo off\r\n');
  } else {
    const f = path.join(dir, 'opencode');
    fs.writeFileSync(f, '#!/bin/sh\nexit 0\n');
    fs.chmodSync(f, 0o755);
  }
  return dir;
}

function runInstaller(args, env) {
  const configDir = path.join(env.XDG_CONFIG_HOME, 'claude-test');
  return spawnSync(process.execPath, [INSTALLER, ...args, '--config-dir', configDir, '--non-interactive', '--no-mcp-shrink'], {
    env, encoding: 'utf8',
  });
}

// The temp XDG dir doubles as HOME: uninstall walks every agent's home and
// runs every caveman/claude/gemini it finds on PATH, so neither may be real.
function installEnv(xdg, shimDir) {
  return { ...isolatedEnv(xdg, [shimDir]), XDG_CONFIG_HOME: xdg };
}

// ── 1. Fresh install populates expected files ────────────────────────────
test('opencode fresh install drops plugin, commands, agents, skills, AGENTS.md, opencode.jsonc', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const r = runInstaller(['--only', 'opencode'], installEnv(xdg, shimDir));
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const ocDir = path.join(xdg, 'opencode');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'plugin.js')), 'plugin.js missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'package.json')), 'plugin package.json missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'caveman-config.cjs')), 'caveman-config.cjs sibling missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'caveman-parse.cjs')), 'caveman-parse.cjs sibling missing');

    for (const f of ['caveman.md', 'ultracave.md', 'megacave.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md', 'caveman-help.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'commands', f)), `command ${f} missing`);
    }
    for (const f of ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'agents', f)), `agent ${f} missing`);
      // Subagent-only: keeps cavecrew out of opencode's Tab cycle (#725).
      assert.match(fs.readFileSync(path.join(ocDir, 'agents', f), 'utf8'), /^mode: subagent$/m, `agent ${f} must be mode: subagent`);
    }
    for (const name of ['caveman', 'ultracave', 'megacave', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'skills', name, 'SKILL.md')), `skill ${name}/SKILL.md missing`);
    }
    assert.ok(fs.existsSync(path.join(ocDir, 'AGENTS.md')), 'AGENTS.md missing');
    const agentsBody = fs.readFileSync(path.join(ocDir, 'AGENTS.md'), 'utf8');
    assert.match(agentsBody, /Respond terse like smart caveman/);
    // Block must be wrapped in begin/end markers so uninstall can isolate it
    // from user-authored content above and below.
    assert.match(agentsBody, /<!-- caveman-begin -->/);
    assert.match(agentsBody, /<!-- caveman-end -->/);

    // Fresh installs (neither config present) create opencode.jsonc — the
    // installer prefers it so it never shadows an existing .jsonc (#861).
    const cfgPath = path.join(ocDir, 'opencode.jsonc');
    assert.ok(fs.existsSync(cfgPath), 'opencode.jsonc missing');
    const cfg = JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
    assert.ok(Array.isArray(cfg.plugin), 'opencode.jsonc missing plugin array');
    assert.deepEqual(cfg.plugin, ['./plugins/caveman'], 'plugin entry must name the directory');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 2. Idempotency: install twice, plugin array stays length 1 ───────────
test('opencode idempotent install does not duplicate plugin entries', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);
    const r2 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r2.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(xdg, 'opencode', 'opencode.jsonc'), 'utf8'));
    const matches = cfg.plugin.filter(p => p === './plugins/caveman');
    assert.equal(matches.length, 1, `expected 1 plugin entry, got ${matches.length}`);

    // AGENTS.md should not have the ruleset duplicated either.
    const agentsMd = fs.readFileSync(path.join(xdg, 'opencode', 'AGENTS.md'), 'utf8');
    const sentinelCount = (agentsMd.match(/Respond terse like smart caveman/g) || []).length;
    assert.equal(sentinelCount, 1, `expected 1 sentinel, got ${sentinelCount}`);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 2b. Plugin payload not overwritten on re-install (without --force) ────
test('opencode re-install preserves user edits to plugin.js without --force', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const tweak = '\n// USER-TWEAK-DO-NOT-OVERWRITE\n';
    fs.appendFileSync(pluginPath, tweak);
    const beforeBytes = fs.readFileSync(pluginPath, 'utf8');

    const r2 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r2.status, 2);

    const afterBytes = fs.readFileSync(pluginPath, 'utf8');
    assert.equal(afterBytes, beforeBytes, 'second install should not overwrite plugin.js without --force');
    assert.match(afterBytes, /USER-TWEAK-DO-NOT-OVERWRITE/);

    // With --force, the file should be replaced (no tweak afterward).
    const r3 = runInstaller(['--only', 'opencode', '--force'], env);
    assert.notEqual(r3.status, 2);
    const forced = fs.readFileSync(pluginPath, 'utf8');
    assert.doesNotMatch(forced, /USER-TWEAK-DO-NOT-OVERWRITE/, '--force should overwrite plugin.js');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

test('opencode refuses an unowned plugin directory before writing other payloads', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const ocDir = path.join(xdg, 'opencode');
    const userPlugin = path.join(ocDir, 'plugins', 'caveman');
    fs.mkdirSync(userPlugin, { recursive: true });
    fs.writeFileSync(path.join(userPlugin, 'user.js'), 'export default "mine";\n');
    const env = installEnv(xdg, shimDir);

    const result = runInstaller(['--only', 'opencode'], env);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /ownership conflict/);
    assert.equal(fs.readFileSync(path.join(userPlugin, 'user.js'), 'utf8'), 'export default "mine";\n');
    assert.equal(fs.existsSync(path.join(ocDir, 'commands', 'caveman.md')), false);
    assert.equal(fs.existsSync(path.join(ocDir, '.caveman-opencode-ownership.json')), false);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

test('opencode uninstall never deletes unjournaled same-named user content', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const userPlugin = path.join(xdg, 'opencode', 'plugins', 'caveman');
    fs.mkdirSync(userPlugin, { recursive: true });
    fs.writeFileSync(path.join(userPlugin, 'user.js'), 'export default "mine";\n');
    const env = installEnv(xdg, shimDir);
    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.readFileSync(path.join(userPlugin, 'user.js'), 'utf8'), 'export default "mine";\n');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

test('opencode --force backs up conflicts and uninstall restores original directory', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const ocDir = path.join(xdg, 'opencode');
    const userPlugin = path.join(ocDir, 'plugins', 'caveman');
    fs.mkdirSync(userPlugin, { recursive: true });
    fs.writeFileSync(path.join(userPlugin, 'user.js'), 'export default "mine";\n');
    const env = installEnv(xdg, shimDir);

    const installed = runInstaller(['--only', 'opencode', '--force'], env);
    assert.equal(installed.status, 0, installed.stderr);
    assert.equal(fs.existsSync(path.join(userPlugin, 'user.js')), false);
    assert.ok(fs.existsSync(path.join(userPlugin, 'plugin.js')));

    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.readFileSync(path.join(userPlugin, 'user.js'), 'utf8'), 'export default "mine";\n');
    assert.equal(fs.existsSync(path.join(userPlugin, 'plugin.js')), false);
    assert.equal(fs.existsSync(path.join(ocDir, 'commands', 'caveman.md')), false);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

test('opencode uninstall leaves modified owned files and keeps journal evidence', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const installed = runInstaller(['--only', 'opencode'], env);
    assert.equal(installed.status, 0, installed.stderr);
    const ocDir = path.join(xdg, 'opencode');
    const command = path.join(ocDir, 'commands', 'caveman.md');
    fs.appendFileSync(command, '\nUSER EDIT\n');

    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 1, removed.stderr);
    assert.match(removed.stderr, /left modified/);
    assert.match(removed.stderr, /uninstall incomplete/);
    assert.match(fs.readFileSync(command, 'utf8'), /USER EDIT/);
    assert.ok(fs.existsSync(path.join(ocDir, '.caveman-opencode-ownership.json')));
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 2c. AGENTS.md fence preserves user content above and below ───────────
test('opencode uninstall strips fenced AGENTS.md block, preserving user prefix and suffix', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);

    const agentsMd = path.join(xdg, 'opencode', 'AGENTS.md');
    const installed = fs.readFileSync(agentsMd, 'utf8');
    // Sandwich the caveman block between user prefix and suffix.
    const userPrefix = '# my project\n\nuse 2-space indent.\n\n';
    const userSuffix = '\n## extra\n\nkeep PRs small.\n';
    fs.writeFileSync(agentsMd, userPrefix + installed.trimEnd() + '\n' + userSuffix);

    const r2 = runInstaller(['--uninstall'], env);
    assert.notEqual(r2.status, 2);

    const after = fs.readFileSync(agentsMd, 'utf8');
    assert.doesNotMatch(after, /<!-- caveman-begin -->/, 'caveman block should be stripped');
    assert.doesNotMatch(after, /<!-- caveman-end -->/, 'caveman end marker should be stripped');
    assert.doesNotMatch(after, /Respond terse like smart caveman/, 'caveman body should be stripped');
    assert.match(after, /# my project/, 'user prefix should survive');
    assert.match(after, /use 2-space indent/, 'user prefix body should survive');
    assert.match(after, /## extra/, 'user suffix should survive');
    assert.match(after, /keep PRs small/, 'user suffix body should survive');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 2d. A CRLF AGENTS.md gets a CRLF block and comes back byte for byte ──
test('opencode AGENTS.md with CRLF line endings keeps them through install and uninstall', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const agentsMd = path.join(xdg, 'opencode', 'AGENTS.md');
    const user = '# my project\r\n\r\nuse 2-space indent.\r\n';
    fs.mkdirSync(path.dirname(agentsMd), { recursive: true });
    fs.writeFileSync(agentsMd, user);

    assert.notEqual(runInstaller(['--only', 'opencode'], env).status, 2);
    const installed = fs.readFileSync(agentsMd, 'utf8');
    assert.doesNotMatch(installed, /(^|[^\r])\n/, 'LF line ending in a CRLF file');
    assert.ok(installed.startsWith(`${user}\r\n<!-- caveman-begin -->\r\n`), JSON.stringify(installed.slice(0, 80)));
    assert.notEqual(runInstaller(['--only', 'opencode'], env).status, 2);
    assert.equal(fs.readFileSync(agentsMd, 'utf8'), installed, 'a second install rewrote the CRLF block');

    assert.notEqual(runInstaller(['--uninstall'], env).status, 2);
    assert.equal(fs.readFileSync(agentsMd, 'utf8'), user);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 3. Tolerates JSONC opencode.json (#249-class regression guard) ───────
test('opencode install tolerates JSONC opencode.json (comments + trailing commas)', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const ocDir = path.join(xdg, 'opencode');
    fs.mkdirSync(ocDir, { recursive: true });
    fs.writeFileSync(path.join(ocDir, 'opencode.json'),
      `// hand-written
{
  /* user prefs */
  "model": "anthropic/claude-sonnet-4-5",
  "theme": "dark",
}
`);

    const env = installEnv(xdg, shimDir);
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(ocDir, 'opencode.json'), 'utf8'));
    assert.equal(cfg.model, 'anthropic/claude-sonnet-4-5', 'user model setting wiped');
    assert.equal(cfg.theme, 'dark', 'user theme setting wiped');
    assert.ok(cfg.plugin.includes('./plugins/caveman'), 'plugin entry missing');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 4. Uninstall removes opencode artifacts and prunes config ────────────
test('opencode uninstall removes plugin dir, command/agent/skill files, prunes opencode.json', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);

    const r2 = runInstaller(['--uninstall'], env);
    assert.notEqual(r2.status, 2);

    const ocDir = path.join(xdg, 'opencode');
    assert.equal(fs.existsSync(path.join(ocDir, 'plugins', 'caveman')), false, 'plugin dir survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'commands', 'caveman.md')), false, 'caveman.md command survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'agents', 'cavecrew-builder.md')), false, 'cavecrew agent survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'skills', 'caveman')), false, 'caveman skill dir survived');
    assert.equal(fs.existsSync(path.join(ocDir, 'AGENTS.md')), false, 'AGENTS.md (we wrote it) survived');

    for (const name of ['opencode.jsonc', 'opencode.json']) {
      const cfgPath = path.join(ocDir, name);
      if (!fs.existsSync(cfgPath)) continue;
      const cfg = JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
      const stillHasPlugin = Array.isArray(cfg.plugin) && cfg.plugin.includes('./plugins/caveman');
      assert.equal(stillHasPlugin, false, `plugin entry survived in ${name}`);
    }
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 5. Plugin smoke: load installed plugin.js, fire the real opencode hooks ──
// opencode (>= 1.15) has no `tui.prompt.append` or top-level `session.created`
// plugin-hook keys (#418/#421). The plugin now uses `chat.message` for mode
// parsing, `experimental.chat.system.transform` for reinforcement, and the
// `event` dispatcher (filtering event.type === 'session.created') for session
// init. This test drives those real hooks.
test('opencode plugin handles /caveman ultra, /megacave, stop caveman, and session init via real hooks', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    const env = installEnv(xdg, shimDir);
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const flagPath = path.join(xdg, 'opencode', '.caveman-active');
    const modeLogPath = path.join(xdg, 'opencode', MODE_LOG_BASENAME);

    // Set XDG_CONFIG_HOME so the plugin's flagPath resolves to our temp dir,
    // and pin the default mode so session-init is deterministic regardless of
    // any ambient user/repo-local caveman config.
    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';

    const mod = await import(pathToFileURL(pluginPath).href);
    const factory = mod.default || mod.CavemanPlugin;
    const handlers = await factory({});

    // The dead direct-key hooks must NOT be registered.
    assert.equal(handlers['tui.prompt.append'], undefined, 'tui.prompt.append should not exist');
    assert.equal(handlers['session.created'], undefined, 'session.created direct key should not exist');
    assert.equal(typeof handlers.event, 'function', 'event dispatcher should be a function');
    assert.equal(typeof handlers['chat.message'], 'function', 'chat.message should be a function');
    assert.equal(typeof handlers['experimental.chat.system.transform'], 'function',
      'system.transform should be a function');

    // Slash command in a chat.message text part activates ultracave (the
    // legacy `ultra` argument is an alias).
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultracave');
    assert.ok(fs.existsSync(modeLogPath), 'mode log missing after slash activation');
    const activationRows = fs.readFileSync(modeLogPath, 'utf8').trim().split('\n').map(JSON.parse);
    assert.equal(activationRows.at(-1).mode, 'ultracave');

    // Status must replace the activation template and report the actual flag,
    // without touching mode history or refreshing its mtime.
    const beforeStatus = [fs.readFileSync(modeLogPath, 'utf8'), fs.statSync(flagPath).mtimeMs];
    for (const text of ['/caveman status', '/caveman:caveman status',
      'Activate caveman mode: status\n\nIf no argument given, use caveman.']) {
      const output = { parts: [{ type: 'text', text }] };
      await handlers['chat.message']({}, output);
      assert.equal(output.parts[0].text, 'Report this status verbatim without changing mode: Caveman mode: ultracave');
      assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultracave');
      assert.deepEqual([fs.readFileSync(modeLogPath, 'utf8'), fs.statSync(flagPath).mtimeMs], beforeStatus);
    }

    // opencode expands "/caveman <arg>" into the command template before
    // chat.message fires — the argument must be recovered from the expanded text.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: wenyan-lite\n\nIf no argument given, use caveman. If "off", deactivate.' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'megacave');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: off\n\nIf no argument given, use caveman. If "off", deactivate.' }] });
    assert.equal(fs.existsSync(flagPath), false, 'expanded template with off should delete the flag');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: \n\nIf no argument given, use caveman. If "off", deactivate.' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman', 'expanded template without argument uses default');
    // /ultracave and /megacave expand to their own command templates.
    for (const [file, mode] of [['ultracave.md', 'ultracave'], ['megacave.md', 'megacave']]) {
      const tpl = fs.readFileSync(path.join(REPO_ROOT, 'src', 'plugins', 'opencode', 'commands', file), 'utf8')
        .replace(/^---[\s\S]*?---\s*/, '');
      await handlers['chat.message']({}, { parts: [{ type: 'text', text: tpl }] });
      assert.equal(fs.readFileSync(flagPath, 'utf8'), mode, file);
    }

    // opencode's non-interactive `run` path wraps the message in literal
    // quotes ("/caveman lite"\n) — the parser must unwrap them.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '"/caveman lite"\n' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/ultracave' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultracave');

    // system.transform injects the reinforcement line while active.
    const sys1 = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys1);
    assert.equal(sys1.system.length, 1, 'expected one reinforcement line');
    assert.match(sys1.system[0], /CAVEMAN MODE ACTIVE \(ultracave\)/);

    // Existing system prompts must remain a single entry. Some vLLM chat
    // templates reject a second system message even when both precede user
    // content, so append the reinforcement to the existing entry.
    const sysWithExisting = { system: ['existing system prompt'] };
    await handlers['experimental.chat.system.transform']({}, sysWithExisting);
    assert.equal(sysWithExisting.system.length, 1, 'must not add a second system message');
    assert.match(sysWithExisting.system[0], /^existing system prompt\n\nCAVEMAN MODE ACTIVE \(ultracave\)/);

    // Idempotent across repeated transforms on the SAME array: if opencode
    // ever reuses output.system between turns, an unguarded append would grow
    // the system prompt without bound and silently eat the context window.
    await handlers['experimental.chat.system.transform']({}, sysWithExisting);
    await handlers['experimental.chat.system.transform']({}, sysWithExisting);
    assert.equal(sysWithExisting.system.length, 1, 'must not add entries on re-transform');
    assert.equal(
      sysWithExisting.system[0].match(/CAVEMAN MODE ACTIVE/g).length,
      1,
      'reinforcement line must not accumulate across transforms',
    );

    // Natural-language deactivation removes the flag.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: 'stop caveman please' }] });
    assert.equal(fs.existsSync(flagPath), false, 'flag should be deleted after deactivation');
    const deactivationRows = fs.readFileSync(modeLogPath, 'utf8').trim().split('\n').map(JSON.parse);
    assert.equal(deactivationRows.at(-1).mode, null);

    const offStatus = { parts: [{ type: 'text', text: '/caveman status' }] };
    await handlers['chat.message']({}, offStatus);
    assert.equal(offStatus.parts[0].text, 'Report this status verbatim without changing mode: Caveman mode: off');
    assert.equal(fs.existsSync(flagPath), false, 'status must not activate the default');

    process.env.CAVEMAN_DEFAULT_MODE = 'manual';
    await handlers.event({ event: { type: 'session.created' } });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman', 'Claude-only manual policy must not pretend OpenCode static rules are disabled');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman', 'manual policy permits explicit activation');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: 'stop caveman' }] });
    assert.equal(fs.existsSync(flagPath), false);
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';

    // No reinforcement injected when inactive.
    const sys2 = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys2);
    assert.equal(sys2.system.length, 0, 'no reinforcement when flag absent');

    // The `event` dispatcher writes the default mode on session.created, and
    // ignores unrelated event types.
    await handlers.event({ event: { type: 'session.idle' } });
    assert.equal(fs.existsSync(flagPath), false, 'non-session.created event must not write the flag');
    await handlers.event({ event: { type: 'session.created' } });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman');
    const sessionInitRows = fs.readFileSync(modeLogPath, 'utf8').trim().split('\n').map(JSON.parse);
    assert.deepEqual(
      { mode: sessionInitRows.at(-1).mode, prev: sessionInitRows.at(-1).prev },
      { mode: 'caveman', prev: null },
    );

    // A session starting with the default resolved to "off" turns caveman off,
    // and that transition is a mode change like any other — stats must be able
    // to attribute the messages that follow to caveman being inactive.
    process.env.CAVEMAN_DEFAULT_MODE = 'off';
    await handlers.event({ event: { type: 'session.created' } });
    assert.equal(fs.existsSync(flagPath), false, 'session init with default off should delete the flag');
    const sessionOffRows = fs.readFileSync(modeLogPath, 'utf8').trim().split('\n').map(JSON.parse);
    assert.deepEqual(
      { mode: sessionOffRows.at(-1).mode, prev: sessionOffRows.at(-1).prev },
      { mode: null, prev: 'caveman' },
    );
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── opencode 2.x: the same payload loads through server.js ────────────────
// opencode 2 skips the opencode.json file entry for plugin.js ("configured
// plugin path must be a directory"), discovers plugins/caveman/ itself, loads
// <dir>/server.js and requires `export default { id, setup(ctx) }`. Without
// that file caveman's mode tracking silently never ran on 2.x. This drives
// the V2 surface the way the host does: event stream, prompt and context hooks.
test('opencode 2.x loads server.js: session init, prompt mode changes and context reinforcement', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  const origXdg = process.env.XDG_CONFIG_HOME;
  try {
    const r = runInstaller(['--only', 'opencode'], installEnv(xdg, shimDir));
    assert.equal(r.status, 0, r.stderr);
    const pluginDir = path.join(xdg, 'opencode', 'plugins', 'caveman');
    const flagPath = path.join(xdg, 'opencode', '.caveman-active');
    // 1.x loads only top-level plugins/*.{js,ts} plus the config entry, so a
    // server.js inside the plugin directory never reaches the 1.x loader.
    const cfg = SETTINGS.readSettings(path.join(xdg, 'opencode', 'opencode.jsonc'));
    assert.deepEqual(cfg.plugin, ['./plugins/caveman']);

    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';
    const mod = await import(pathToFileURL(path.join(pluginDir, 'server.js')).href);
    assert.equal(mod.default.id, 'caveman');
    assert.equal(typeof mod.default.setup, 'function');

    const hooks = {};
    let delivered;
    const eventsDone = new Promise((resolve) => { delivered = resolve; });
    const ctx = {
      event: {
        subscribe: async function* () {
          yield { type: 'session.created', data: { sessionID: 'ses_1' } };
          delivered();
        },
      },
      session: { hook: async (name, callback) => { hooks[name] = callback; return { dispose: async () => {} }; } },
    };
    const cleanup = await mod.default.setup(ctx);
    assert.equal(typeof cleanup, 'function');
    await eventsDone;
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'caveman', 'session init did not write the flag');

    const prompt = (text) => {
      const event = { sessionID: 'ses_1', prompt: { text } };
      return hooks.prompt(event).then(() => event.prompt.text);
    };
    await prompt('/ultracave');
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultracave');
    assert.equal(await prompt('/caveman status'), 'Report this status verbatim without changing mode: Caveman mode: ultracave');

    const context = { sessionID: 'ses_1', system: [{ type: 'text', text: 'host prompt' }] };
    await hooks.context(context);
    assert.equal(context.system.length, 2);
    assert.equal(context.system[1].type, 'text');
    assert.match(context.system[1].text, /^CAVEMAN MODE ACTIVE \(ultracave\)/);

    await prompt('stop caveman');
    assert.equal(fs.existsSync(flagPath), false, 'stop caveman did not clear the flag');
    const quiet = { sessionID: 'ses_1', system: [] };
    await hooks.context(quiet);
    assert.deepEqual(quiet.system, []);
    cleanup();
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    if (origXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = origXdg;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── system.transform must inject the ACTIVE MODE's skill ─────────────────
// Checks injected content differs per mode and a mid-session switch replaces
// the whole block rather than stacking behind the prior one (#792).
test('opencode system.transform injects the active mode\'s SKILL.md body or thesis, not just the banner', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    const env = installEnv(xdg, shimDir);
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';

    const mod = await import(pathToFileURL(pluginPath).href);
    const factory = mod.default || mod.CavemanPlugin;
    const handlers = await factory({});

    // The caveman skill is installed beside the plugin, so its whole body
    // travels.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman' }] });
    const sys = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys);
    assert.match(sys.system[0], /CAVEMAN MODE ACTIVE \(caveman\)/);
    assert.match(sys.system[0], /Caveman is a voice, not broken grammar\./,
      'caveman should carry its SKILL.md body, not just the banner');

    // Switching mode mid-session must swap in the NEW mode's rules, not leave
    // caveman's stacked behind it (the idempotent-rewrite path). Whether the
    // ultracave skill dir is installed or not, its thesis line is present.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/ultracave' }] });
    await handlers['experimental.chat.system.transform']({}, sys); // reuse same array, as opencode may
    await handlers['experimental.chat.system.transform']({}, sys);
    assert.match(sys.system[0], /CAVEMAN MODE ACTIVE \(ultracave\)/);
    assert.match(sys.system[0], /Only fluff die\. Then cut again\./, 'ultracave must carry its own thesis');
    assert.doesNotMatch(sys.system[0], /Caveman is a voice, not broken grammar\./,
      'switching to ultracave must drop caveman\'s body, not accumulate it');
    assert.equal((sys.system[0].match(/CAVEMAN MODE ACTIVE/g) || []).length, 1,
      'banner must not duplicate across repeated transforms');

    // The legacy wenyan argument lands on megacave.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman wenyan' }] });
    const sysMega = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sysMega);
    assert.match(sysMega.system[0], /CAVEMAN MODE ACTIVE \(megacave\)/);
    assert.match(sysMega.system[0], /以文言答。技術之實皆存，唯贅言去之。/);
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── a stale caveman-config.cjs must degrade, not throw ───────────────────
// plugin.js reads the skill loader out of caveman-config.js instead of
// keeping its own copy, and the installed caveman-config.cjs is a COPY: an
// opencode plugin dir left over from before the shared loader existed has no
// loadRuleset/thesisLine export. That is plugin-cache drift, the same case the
// hooks guard against, and it lands inside a system-prompt hook — throwing
// there costs the user the banner too, not just the ruleset.
test('opencode system.transform degrades to the banner when caveman-config.cjs predates the shared ruleset loader', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    const env = installEnv(xdg, shimDir);
    assert.notEqual(runInstaller(['--only', 'opencode'], env).status, 2);

    // Roll the installed copy back to a pre-shared-loader shape by dropping
    // the export line, exactly as tests/test_mode_tracker_ruleset.js does for
    // the hook side. The rest of the module — the flag helpers plugin.js
    // destructures at load — stays intact, which is what makes this drift
    // rather than a corrupt file.
    const cfgPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'caveman-config.cjs');
    const body = fs.readFileSync(cfgPath, 'utf8');
    const stripped = body.replace(/^\s*skillPathCandidates, loadRuleset, thesisLine, rulesetBanner,\n/m, '');
    assert.notEqual(stripped, body, 'export line to strip not found — test is stale');
    fs.writeFileSync(cfgPath, stripped);

    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';
    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const mod = await import(pathToFileURL(pluginPath).href + '?stale');
    const handlers = await (mod.default || mod.CavemanPlugin)({});

    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman' }] });
    const sys = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys);

    assert.match(sys.system[0], /CAVEMAN MODE ACTIVE \(caveman\)/,
      'a stale config must still leave the user the banner');
    assert.doesNotMatch(sys.system[0], /Respond terse like smart caveman/,
      'a config with no loader cannot have produced a ruleset or thesis');
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── a caveman-config.cjs with no recordModeChange must not break the plugin ─
// The mode-history log is the NEWEST thing plugin.js pulls out of
// caveman-config, and handleSessionCreated() runs at factory time, outside any
// try. So an installed plugin dir that predates the export would throw during
// plugin construction rather than in a handler: the user loses activation
// entirely, not just the history line. The log is best-effort by its own
// design (recordModeChange silent-fails internally), so the correct
// degradation is a no-op, and the mode flag must still be written.
test('opencode session init still activates when caveman-config.cjs predates recordModeChange', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    const env = installEnv(xdg, shimDir);
    assert.notEqual(runInstaller(['--only', 'opencode'], env).status, 2);

    const pluginDir = path.join(xdg, 'opencode', 'plugins', 'caveman');
    const cfgPath = path.join(pluginDir, 'caveman-config.cjs');
    const body = fs.readFileSync(cfgPath, 'utf8');
    const stripped = body.replace(/^\s*recordModeChange, MODE_LOG_BASENAME,\n/m, '  MODE_LOG_BASENAME,\n');
    assert.notEqual(stripped, body, 'export line to strip not found — test is stale');
    fs.writeFileSync(cfgPath, stripped);

    process.env.XDG_CONFIG_HOME = xdg;
    // A legacy level name in the env still resolves (to caveman).
    process.env.CAVEMAN_DEFAULT_MODE = 'full';
    const pluginPath = path.join(pluginDir, 'plugin.js');
    // Factory construction is where the unguarded call would throw.
    const mod = await import(pathToFileURL(pluginPath).href + '?norecord');
    const handlers = await (mod.default || mod.CavemanPlugin)({});

    assert.equal(fs.readFileSync(path.join(xdg, 'opencode', '.caveman-active'), 'utf8').trim(), 'caveman',
      'session init must still write the mode flag with no recordModeChange export');

    // And a later mode change must still take effect rather than throwing.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
    assert.equal(fs.readFileSync(path.join(xdg, 'opencode', '.caveman-active'), 'utf8').trim(), 'ultracave',
      'a mode change must still apply with no recordModeChange export');
    assert.ok(!fs.existsSync(path.join(xdg, 'opencode', MODE_LOG_BASENAME)),
      'a stripped config cannot have written a history log');
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── AGENTS.md marker damage must not splice the file ─────────────────────
// Both markers present is not enough: they must be one matched pair, in order.
// An END above a BEGIN made `existing.indexOf(END, begin)` return -1, and the
// slice arithmetic then re-appended the whole file from byte 19, compounding
// on every re-run.
test('opencode leaves an AGENTS.md with unmatched caveman markers untouched', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const ocDir = path.join(xdg, 'opencode');
    fs.mkdirSync(ocDir, { recursive: true });
    const agentsMd = path.join(ocDir, 'AGENTS.md');
    const original = [
      '## My team notes',
      'Never force-push.',
      '<!-- caveman-end -->',
      'more user text',
      '<!-- caveman-begin -->',
      'stale rules',
      '',
    ].join('\n');
    fs.writeFileSync(agentsMd, original);

    const env = installEnv(xdg, shimDir);
    for (let i = 0; i < 2; i++) {
      const r = runInstaller(['--only', 'opencode'], env);
      assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);
      assert.match(r.stdout, /unmatched caveman markers/);
    }
    assert.equal(fs.readFileSync(agentsMd, 'utf8'), original, 'damaged-marker AGENTS.md must be byte-identical');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 9. One-shot independent modes restore the displaced prose mode ──────────
// #599 parity. On Claude Code, caveman-mode-tracker.js remembers the prose mode
// a one-shot (/caveman-commit, /caveman-review, /caveman-compress) displaces and
// restores it on the next ordinary prompt. The opencode plugin wrote the
// one-shot mode and never came back: `experimental.chat.system.transform` skips
// INDEPENDENT_MODES, so a single /caveman-commit silently killed per-turn
// reinforcement for the rest of the session — it only self-healed at the next
// session.created. Same shared helpers (writeSessionPrev/readSessionPrev/
// clearSessionPrev), same restore rule, so the two hosts cannot drift again.
test('opencode plugin restores the displaced prose mode after a one-shot mode', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  const origXdg = process.env.XDG_CONFIG_HOME;
  try {
    const env = installEnv(xdg, shimDir);
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const flagPath = path.join(xdg, 'opencode', '.caveman-active');
    const prevPath = path.join(xdg, 'opencode', '.caveman-active.prev');

    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';

    const mod = await import(pathToFileURL(pluginPath).href);
    const handlers = await (mod.default || mod.CavemanPlugin)({});

    const send = (text) => handlers['chat.message']({}, { parts: [{ type: 'text', text }] });
    const reinforced = async () => {
      const out = { system: ['base'] };
      await handlers['experimental.chat.system.transform']({}, out);
      return /CAVEMAN MODE ACTIVE/.test(out.system[0]);
    };
    const flag = () => (fs.existsSync(flagPath) ? fs.readFileSync(flagPath, 'utf8') : null);

    // A one-shot displaces ultracave and remembers it.
    await send('/ultracave');
    assert.equal(flag(), 'ultracave');
    await send('/caveman-commit');
    assert.equal(flag(), 'commit', 'one-shot must take effect for its own turn');
    assert.equal(await reinforced(), false, 'independent modes carry their own skill, not caveman reinforcement');
    assert.equal(fs.readFileSync(prevPath, 'utf8'), 'ultracave', 'displaced prose mode must be remembered');

    // The next ordinary prompt restores it — this is what regressed.
    await send('now fix the parser bug');
    assert.equal(flag(), 'ultracave', 'next ordinary prompt must restore the displaced prose mode');
    assert.equal(await reinforced(), true, 'reinforcement must resume after the one-shot');
    assert.equal(fs.existsSync(prevPath), false, 'prev must be cleared once consumed');

    // A second one-shot chained onto the first must still restore the ORIGINAL
    // prose mode, not the intervening one-shot.
    await send('/caveman-commit');
    await send('/caveman-review');
    assert.equal(flag(), 'review');
    assert.equal(fs.readFileSync(prevPath, 'utf8'), 'ultracave', 'chained one-shots keep the first return target');
    await send('ship it');
    assert.equal(flag(), 'ultracave', 'chained one-shots restore the original prose mode');

    // A one-shot entered while caveman is OFF must restore off, never a stale
    // return target left behind by an earlier one-shot.
    await send('stop caveman');
    assert.equal(flag(), null);
    await send('/caveman-compress');
    assert.equal(flag(), 'compress');
    await send('summarize that');
    assert.equal(flag(), null, 'a one-shot entered from off must return to off');
    assert.equal(await reinforced(), false);
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    if (origXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = origXdg;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 10. Uninstall removes the opencode mode state, both files ──────────────
// The Claude-side cleanup sweeps a `stateFiles` list that already names
// `.caveman-active.prev`; the opencode branch only unlinked `.caveman-active`,
// which was correct while the plugin never wrote a prev file. It writes one now
// (one-shot restore, test 9), so uninstall has to take both or it leaves state
// behind — and a stale prev is not inert: a reinstall's first one-shot would
// read it as that session's return target.
test('opencode uninstall removes both the mode flag and the one-shot prev file', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const installed = runInstaller(['--only', 'opencode'], env);
    assert.equal(installed.status, 0, installed.stderr);

    const ocDir = path.join(xdg, 'opencode');
    const flag = path.join(ocDir, '.caveman-active');
    const prev = path.join(ocDir, '.caveman-active.prev');
    fs.writeFileSync(flag, 'commit');
    fs.writeFileSync(prev, 'ultracave');

    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.existsSync(flag), false, 'mode flag must be removed');
    assert.equal(fs.existsSync(prev), false, 'one-shot prev file must be removed');
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 10b. Uninstall puts a commented opencode.jsonc back and leaves nothing ──
// Install rewrites the config as plain JSON (comments gone) and keeps the
// original in opencode.jsonc.bak; the plugin also writes a mode log. Uninstall
// used to leave the comment-less file, the .bak and the log behind.
test('opencode uninstall restores a commented opencode.jsonc and removes its backup and the mode log', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const ocDir = path.join(xdg, 'opencode');
    const cfgPath = path.join(ocDir, 'opencode.jsonc');
    const original = '{\n  // my opencode config\n  "theme": "tokyonight",\n  "plugin": ["my-plugin"], // trailing comma ok\n}\n';
    fs.mkdirSync(ocDir, { recursive: true });
    fs.writeFileSync(cfgPath, original);

    const installed = runInstaller(['--only', 'opencode'], env);
    assert.equal(installed.status, 0, installed.stderr);
    assert.deepEqual(SETTINGS.readSettings(cfgPath).plugin, ['my-plugin', './plugins/caveman']);
    fs.writeFileSync(path.join(ocDir, MODE_LOG_BASENAME), '{"mode":"caveman"}\n');

    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 0, removed.stderr);
    assert.equal(fs.readFileSync(cfgPath, 'utf8'), original, 'the original config, comments included, must come back');
    assert.equal(fs.existsSync(cfgPath + '.bak'), false, 'the install backup must go');
    assert.equal(fs.existsSync(path.join(ocDir, MODE_LOG_BASENAME)), false, 'the mode log must go');

    // A config the user changed after install keeps its backup: nothing is lost.
    assert.equal(runInstaller(['--only', 'opencode'], env).status, 0);
    const changed = SETTINGS.readSettings(cfgPath);
    SETTINGS.writeSettings(cfgPath, { ...changed, model: 'anthropic/claude-sonnet-4-5' });
    assert.equal(runInstaller(['--uninstall'], env).status, 0);
    assert.equal(fs.readFileSync(cfgPath + '.bak', 'utf8'), original, 'a changed config must keep the pre-install backup');
    assert.deepEqual(SETTINGS.readSettings(cfgPath), { theme: 'tokyonight', plugin: ['my-plugin'], model: 'anthropic/claude-sonnet-4-5' });
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 10b-2. A restore that fails leaves uninstall incomplete ─────────────────
// A failed copy back used to be a warning only, with uninstall reported done.
// A backup that cannot be deleted after a good copy is still a restore.
test('opencode uninstall reports a failed config restore, but not a backup it could not delete', () => {
  const shimDir = shimOpencode();
  try {
    for (const [broken, status] of [['copyFileSync', 1], ['unlinkSync', 0]]) {
      const xdg = freshTmpDir();
      try {
        const env = installEnv(xdg, shimDir);
        const cfgPath = path.join(xdg, 'opencode', 'opencode.jsonc');
        const original = '{\n  // mine\n  "theme": "tokyonight"\n}\n';
        fs.mkdirSync(path.dirname(cfgPath), { recursive: true });
        fs.writeFileSync(cfgPath, original);
        assert.equal(runInstaller(['--only', 'opencode'], env).status, 0);

        // Preloaded into the uninstall: the one fs call on the config backup fails.
        const preload = path.join(xdg, 'break-fs.cjs');
        fs.writeFileSync(preload, "const fs = require('fs');\n"
          + `const name = ${JSON.stringify(broken)};\nconst orig = fs[name];\n`
          + "fs[name] = function (p, ...rest) {\n"
          + "  if (String(p).endsWith('opencode.jsonc.bak')) throw Object.assign(new Error(`EACCES: ${name}`), { code: 'EACCES' });\n"
          + '  return orig.call(this, p, ...rest);\n};\n');
        const removed = runInstaller(['--uninstall'], { ...env, NODE_OPTIONS: `--require "${preload}"` });
        assert.equal(removed.status, status, `${broken}: ${removed.stdout}${removed.stderr}`);
        if (broken === 'copyFileSync') {
          assert.match(removed.stderr, /could not restore/);
          assert.match(removed.stderr, /uninstall incomplete/);
        } else {
          assert.equal(fs.readFileSync(cfgPath, 'utf8'), original, 'the original config must come back');
          assert.doesNotMatch(removed.stderr, /could not restore/);
        }
      } finally {
        fs.rmSync(xdg, { recursive: true, force: true });
      }
    }
  } finally {
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 10c. The plugin entry names the directory, so opencode 2 loads it quietly ─
// A plugin.js entry worked on 1.x only; 2.x warned "configured plugin path
// must be a directory" on every start. An older install's entry is replaced.
test('opencode install replaces an older plugin.js entry with the directory entry', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const env = installEnv(xdg, shimDir);
    const cfgPath = path.join(xdg, 'opencode', 'opencode.json');
    fs.mkdirSync(path.dirname(cfgPath), { recursive: true });
    fs.writeFileSync(cfgPath, JSON.stringify({ plugin: ['./plugins/caveman/plugin.js', 'my-plugin'] }) + '\n');

    const installed = runInstaller(['--only', 'opencode'], env);
    assert.equal(installed.status, 0, installed.stderr);
    assert.deepEqual(SETTINGS.readSettings(cfgPath).plugin, ['my-plugin', './plugins/caveman']);
  } finally {
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});

// ── 11. An older caveman-config.cjs copy must not turn caveman OFF ─────────
// The installed plugin loads a COPY of caveman-config (#848 plugin-cache
// drift), which can predate writeSessionPrev/readSessionPrev/clearSessionPrev.
// With no-op stubs the restore path reads a null return target and falls to its
// deactivate branch, so a single /caveman-commit deleted the flag on the next
// prompt — strictly worse than both the old behavior (one-shot sticks until the
// next session) and the new one. Degrade to the old behavior instead.
test('opencode plugin does not deactivate caveman when the config copy predates the prev helpers', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  const origXdg = process.env.XDG_CONFIG_HOME;
  try {
    const env = installEnv(xdg, shimDir);
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const pluginDir = path.join(xdg, 'opencode', 'plugins', 'caveman');
    const configCjs = path.join(pluginDir, 'caveman-config.cjs');
    // Simulate the older copy: everything else intact, the three prev exports
    // absent — exactly what a pre-#599 caveman-config.cjs looks like.
    fs.appendFileSync(configCjs,
      '\ndelete module.exports.writeSessionPrev;'
      + '\ndelete module.exports.readSessionPrev;'
      + '\ndelete module.exports.clearSessionPrev;\n');

    const flagPath = path.join(xdg, 'opencode', '.caveman-active');
    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'caveman';

    // Cache-bust: earlier tests in this file already imported plugin.js.
    const mod = await import(`${pathToFileURL(path.join(pluginDir, 'plugin.js')).href}?legacy-config`);
    const handlers = await (mod.default || mod.CavemanPlugin)({});
    const send = (text) => handlers['chat.message']({}, { parts: [{ type: 'text', text }] });
    const flag = () => (fs.existsSync(flagPath) ? fs.readFileSync(flagPath, 'utf8') : null);

    await send('/ultracave');
    assert.equal(flag(), 'ultracave');
    await send('/caveman-commit');
    assert.equal(flag(), 'commit');

    // The regression: this deleted the flag. Without the prev helpers the
    // one-shot simply sticks, which is what this plugin did before #599 landed
    // here — never a silent deactivation.
    await send('now fix the parser bug');
    assert.notEqual(flag(), null, 'a one-shot must never deactivate caveman on an older config copy');
    assert.equal(flag(), 'commit', 'without prev helpers the one-shot sticks, as it did before');
  } finally {
    if (origDefault === undefined) delete process.env.CAVEMAN_DEFAULT_MODE;
    else process.env.CAVEMAN_DEFAULT_MODE = origDefault;
    if (origXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = origXdg;
    fs.rmSync(xdg, { recursive: true, force: true });
    fs.rmSync(shimDir, { recursive: true, force: true });
  }
});
