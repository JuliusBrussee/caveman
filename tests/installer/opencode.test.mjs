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

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const INSTALLER = path.join(REPO_ROOT, 'bin', 'install.js');
const requireCjs = createRequire(import.meta.url);
const SETTINGS = requireCjs(path.join(REPO_ROOT, 'bin', 'lib', 'settings.js'));

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

function pathWith(prependDir) {
  const sep = IS_WIN ? ';' : ':';
  return prependDir + sep + (process.env.PATH || '');
}

// ── 1. Fresh install populates expected files ────────────────────────────
test('opencode fresh install drops plugin, commands, agents, skills, AGENTS.md, opencode.jsonc', () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  try {
    const r = runInstaller(['--only', 'opencode'], {
      ...process.env,
      XDG_CONFIG_HOME: xdg,
      PATH: pathWith(shimDir),
      NO_COLOR: '1',
    });
    assert.notEqual(r.status, 2, `argv error: ${r.stderr}`);

    const ocDir = path.join(xdg, 'opencode');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'plugin.js')), 'plugin.js missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'package.json')), 'plugin package.json missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'caveman-config.cjs')), 'caveman-config.cjs sibling missing');
    assert.ok(fs.existsSync(path.join(ocDir, 'plugins', 'caveman', 'caveman-parse.cjs')), 'caveman-parse.cjs sibling missing');

    for (const f of ['caveman.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md', 'caveman-help.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'commands', f)), `command ${f} missing`);
    }
    for (const f of ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md']) {
      assert.ok(fs.existsSync(path.join(ocDir, 'agents', f)), `agent ${f} missing`);
    }
    for (const name of ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew']) {
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
    assert.ok(cfg.plugin.includes('./plugins/caveman/plugin.js'), 'plugin entry missing');
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r1 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r1.status, 2);
    const r2 = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r2.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(xdg, 'opencode', 'opencode.jsonc'), 'utf8'));
    const matches = cfg.plugin.filter(p => p === './plugins/caveman/plugin.js');
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };

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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };

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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const installed = runInstaller(['--only', 'opencode'], env);
    assert.equal(installed.status, 0, installed.stderr);
    const ocDir = path.join(xdg, 'opencode');
    const command = path.join(ocDir, 'commands', 'caveman.md');
    fs.appendFileSync(command, '\nUSER EDIT\n');

    const removed = runInstaller(['--uninstall'], env);
    assert.equal(removed.status, 0, removed.stderr);
    assert.match(removed.stderr, /left modified/);
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
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

    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const cfg = JSON.parse(fs.readFileSync(path.join(ocDir, 'opencode.json'), 'utf8'));
    assert.equal(cfg.model, 'anthropic/claude-sonnet-4-5', 'user model setting wiped');
    assert.equal(cfg.theme, 'dark', 'user theme setting wiped');
    assert.ok(cfg.plugin.includes('./plugins/caveman/plugin.js'), 'plugin entry missing');
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
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
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
      const stillHasPlugin = Array.isArray(cfg.plugin) && cfg.plugin.includes('./plugins/caveman/plugin.js');
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
test('opencode plugin handles /caveman ultra, stop caveman, and session init via real hooks', async () => {
  const xdg = freshTmpDir();
  const shimDir = shimOpencode();
  const origDefault = process.env.CAVEMAN_DEFAULT_MODE;
  try {
    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
    const r = runInstaller(['--only', 'opencode'], env);
    assert.notEqual(r.status, 2);

    const pluginPath = path.join(xdg, 'opencode', 'plugins', 'caveman', 'plugin.js');
    const flagPath = path.join(xdg, 'opencode', '.caveman-active');

    // Set XDG_CONFIG_HOME so the plugin's flagPath resolves to our temp dir,
    // and pin the default mode so session-init is deterministic regardless of
    // any ambient user/repo-local caveman config.
    process.env.XDG_CONFIG_HOME = xdg;
    process.env.CAVEMAN_DEFAULT_MODE = 'full';

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

    // Slash command in a chat.message text part activates ultra.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultra');

    // opencode expands "/caveman <level>" into the command template before
    // chat.message fires — the level must be recovered from the expanded text.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: wenyan-lite\n\nIf no level given, use full. If "off", deactivate.' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'wenyan-lite');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: off\n\nIf no level given, use full. If "off", deactivate.' }] });
    assert.equal(fs.existsSync(flagPath), false, 'expanded template with off should delete the flag');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text:
      'Activate caveman mode: \n\nIf no level given, use full. If "off", deactivate.' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'full', 'expanded template without level uses default');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultra');

    // opencode's non-interactive `run` path wraps the message in literal
    // quotes ("/caveman lite"\n) — the parser must unwrap them.
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '"/caveman lite"\n' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'lite');
    await handlers['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'ultra');

    // system.transform injects the reinforcement line while active.
    const sys1 = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys1);
    assert.equal(sys1.system.length, 1, 'expected one reinforcement line');
    assert.match(sys1.system[0], /CAVEMAN MODE ACTIVE \(ultra\)/);

    // Existing system prompts must remain a single entry. Some vLLM chat
    // templates reject a second system message even when both precede user
    // content, so append the reinforcement to the existing entry.
    const sysWithExisting = { system: ['existing system prompt'] };
    await handlers['experimental.chat.system.transform']({}, sysWithExisting);
    assert.equal(sysWithExisting.system.length, 1, 'must not add a second system message');
    assert.match(sysWithExisting.system[0], /^existing system prompt\n\nCAVEMAN MODE ACTIVE \(ultra\)/);

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

    // No reinforcement injected when inactive.
    const sys2 = { system: [] };
    await handlers['experimental.chat.system.transform']({}, sys2);
    assert.equal(sys2.system.length, 0, 'no reinforcement when flag absent');

    // The `event` dispatcher writes the default mode on session.created, and
    // ignores unrelated event types.
    await handlers.event({ event: { type: 'session.idle' } });
    assert.equal(fs.existsSync(flagPath), false, 'non-session.created event must not write the flag');
    await handlers.event({ event: { type: 'session.created' } });
    assert.equal(fs.readFileSync(flagPath, 'utf8'), 'full');
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

    const env = { ...process.env, XDG_CONFIG_HOME: xdg, PATH: pathWith(shimDir), NO_COLOR: '1' };
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
