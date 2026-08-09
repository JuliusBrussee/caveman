// caveman --doctor — read-only health report for a Claude Code install. Never
// writes anything: broken installs get diagnosed here and fixed by
// re-running the installer or --uninstall, not by doctor itself.
//
// caveman installs one of two ways (see installClaude in ../install.js):
//   - plugin:     `claude plugin install caveman@caveman` — the plugin
//                 manifest wires SessionStart/UserPromptSubmit itself.
//   - standalone: hook files copied into <configDir>/hooks, wired by hand
//                 into settings.json.
// The installer's own 'auto' hook-wiring mode only falls back to standalone
// wiring when the plugin install did not succeed, precisely to avoid both
// paths firing on the same event. doctor mirrors that same either/or model:
// which checks apply depends on which mode is actually active.
//
// Exit-code contract (mapped by the caller): 0 problems -> healthy, >0 -> the
// install needs attention. Notes (legacy command shapes, statusline state)
// are informational and never count as problems.

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { spawnSync } = require('child_process');
const {
  readSettings, hasCavemanHook, tokenizeCommand, MANAGED_HOOK_BASENAMES,
} = require('./settings.js');

function sha256File(p) {
  return crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
}

// Parse a `sha256sum`-style manifest (`<64-hex>  <basename>` per line) into a
// Map. Returns null when the manifest is missing or unreadable — integrity
// checks are then skipped with a note rather than failing the whole report,
// same stance installHooks takes when the remote manifest is absent.
function loadManifest(manifestPath) {
  if (!manifestPath) return null;
  let raw;
  try { raw = fs.readFileSync(manifestPath, 'utf8'); }
  catch (_) { return null; }
  const map = new Map();
  for (const line of raw.split('\n')) {
    const m = /^([0-9a-f]{64})\s+(\S+)\s*$/.exec(line.trim());
    if (m) map.set(m[2], m[1]);
  }
  return map.size ? map : null;
}

// Same probe installClaude uses to decide whether the plugin install already
// succeeded (`claude plugin list` mentioning caveman). Any failure to run
// `claude` at all — not on PATH, non-zero exit, spawn error — is read as "no
// plugin install", the same way the installer treats it.
function probeClaudePlugin() {
  let r;
  try { r = spawnSync('claude', ['plugin', 'list'], { encoding: 'utf8' }); }
  catch (_) { return false; }
  if (!r || r.error || r.status !== 0) return false;
  return /caveman/i.test(r.stdout || '');
}

// Extract the managed-script target of a hook command, resolved against
// configDir when relative. Returns null when the command doesn't reference a
// managed script at all. Same exact-basename rule as the settings helpers so
// a user script that merely contains a managed name is never ours.
function managedTarget(command, configDir) {
  try {
    for (const tok of tokenizeCommand(command)) {
      if (!tok || typeof tok !== 'string') continue;
      if (!MANAGED_HOOK_BASENAMES.has(path.win32.basename(tok))) continue;
      return path.isAbsolute(tok) ? tok : path.join(configDir, tok);
    }
  } catch (_) { /* malformed command — not ours */ }
  return null;
}

// Walk every hook command in settings (plus statusLine, which lives outside
// settings.hooks) and hand each one to visit(event, command). Tolerates the
// malformed shapes readSettings lets through — never assumes arrays.
function eachHookCommand(settings, visit) {
  if (settings.hooks && typeof settings.hooks === 'object') {
    for (const ev of Object.keys(settings.hooks)) {
      if (!Array.isArray(settings.hooks[ev])) continue;
      for (const entry of settings.hooks[ev]) {
        if (!entry || !Array.isArray(entry.hooks)) continue;
        for (const h of entry.hooks) {
          if (h && typeof h.command === 'string') visit(ev, h.command);
        }
      }
    }
  }
  if (settings.statusLine && typeof settings.statusLine.command === 'string') {
    visit('statusLine', settings.statusLine.command);
  }
}

// ── runDoctor ─────────────────────────────────────────────────────────────
// opts: { configDir, hookFiles, manifestPath, c }  (c = chalk from makeChalk)
// Prints the report, returns the number of problems found.
function runDoctor({ configDir, hookFiles, manifestPath, c }) {
  const out = (s) => process.stdout.write(s + '\n');
  const problems = [];
  const notes = [];

  out(c.orange('🪨 caveman doctor'));
  out(c.dim(`  config dir: ${configDir}`));

  const pluginInstalled = probeClaudePlugin();
  out(c.dim(`  install mode: ${pluginInstalled ? 'Claude Code plugin' : 'standalone hooks (or plugin not detected)'}`));
  out('');

  const hooksDir = path.join(configDir, 'hooks');
  const settingsPath = path.join(configDir, 'settings.json');
  const settings = readSettings(settingsPath);

  if (settings === null) {
    problems.push(`settings.json is not valid JSON or JSONC: ${settingsPath}`);
  } else {
    const sessionWired = hasCavemanHook(settings, 'SessionStart', 'caveman-activate');
    const promptWired = hasCavemanHook(settings, 'UserPromptSubmit', 'caveman-mode-tracker');

    if (pluginInstalled) {
      // The plugin manifest wires these events itself. Standalone wiring on
      // top of that fires each event twice — the exact double-firing the
      // installer's 'auto' mode exists to avoid at install time.
      if (sessionWired || promptWired) {
        problems.push(
          'the Claude Code plugin is installed and standalone hooks are also wired in settings.json — ' +
          'SessionStart/UserPromptSubmit will fire twice per event; remove the standalone wiring (or reinstall without --with-hooks)'
        );
      }
    } else {
      // No plugin detected — a healthy install must be standalone, which
      // means the hook files need to exist AND be wired.
      if (!sessionWired) problems.push('SessionStart hook not wired in settings.json');
      if (!promptWired) problems.push('UserPromptSubmit hook not wired in settings.json');

      if (!fs.existsSync(hooksDir)) {
        problems.push(`hooks directory missing: ${hooksDir} — run the installer to set up hooks`);
      } else {
        const manifest = loadManifest(manifestPath);
        for (const f of hookFiles) {
          const p = path.join(hooksDir, f);
          if (!fs.existsSync(p)) {
            problems.push(`missing hook file: ${f}`);
            continue;
          }
          if (manifest && manifest.has(f) && manifest.get(f) !== sha256File(p)) {
            problems.push(`modified hook file: ${f} differs from the shipped version — re-run the installer to restore it`);
          }
        }
        if (!manifest) notes.push('no integrity manifest available — file contents not verified');
      }
    }

    // Orphans and legacy command shapes matter regardless of which mode is
    // currently active — both are leftovers from a previous install that
    // migrating to the plugin (or back) can strand (#471).
    eachHookCommand(settings, (ev, command) => {
      const target = managedTarget(command, configDir);
      if (target && !fs.existsSync(target)) {
        problems.push(`orphaned ${ev} hook points at missing script: ${target}`);
      }
    });
    eachHookCommand(settings, (ev, command) => {
      const toks = tokenizeCommand(command);
      if (toks[0] === 'node' && managedTarget(command, configDir)) {
        notes.push(`${ev} hook uses a bare \`node\` command — re-running the installer pins the absolute path`);
      }
    });

    if (!settings.statusLine) notes.push('statusline badge not configured');
  }

  for (const p of problems) out(c.red(`  problem  ${p}`));
  for (const n of notes) out(c.dim(`  note     ${n}`));
  out('');
  if (problems.length) {
    out(c.red(`${problems.length} problem(s) found`));
  } else {
    out(c.green('no problems found'));
  }
  return problems.length;
}

module.exports = { runDoctor };
