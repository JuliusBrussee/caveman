// caveman — JSONC-tolerant settings.json read/write + defensive hook validation.
//
// Lifted in spirit from gsd-build/get-shit-done's stripJsonComments + readSettings.
// Reused by bin/install.js and (optionally) by hooks/caveman-activate.js so a
// commented settings.json no longer crashes the installer or the runtime hooks.
//
// Public API:
//   readSettings(path)             → object, {}, or null on hard parse failure
//   writeSettings(path, obj)       → atomic write with newline
//   stripJsonComments(src)         → string with // and /* */ stripped (string-aware)
//   validateHookFields(settings)   → mutates: drops malformed hook entries
//   hasCavemanHook(settings, ev)   → idempotency probe
//   addCommandHook(settings, ev, opts) → no-op if substring marker already present
//   removeCavemanHooks(settings)   → uninstall helper
//
// Pure stdlib, CommonJS, Node ≥14.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

// ── stripJsonComments ──────────────────────────────────────────────────────
// Hand-rolled state machine. Tracks string state + backslash escape so a
// comment-looking sequence inside a quoted string is left alone. Removes
// trailing commas in a final pass — JSONC tolerates those, JSON.parse does not.
function stripJsonComments(src) {
  if (typeof src !== 'string') return src;
  let out = '';
  let i = 0;
  const n = src.length;
  let inString = false;
  let stringChar = '';
  let inLine = false;
  let inBlock = false;
  while (i < n) {
    const c = src[i];
    const next = i + 1 < n ? src[i + 1] : '';
    if (inLine) {
      if (c === '\n') { inLine = false; out += c; }
      i++; continue;
    }
    if (inBlock) {
      if (c === '*' && next === '/') { inBlock = false; i += 2; continue; }
      i++; continue;
    }
    if (inString) {
      out += c;
      if (c === '\\') { if (i + 1 < n) { out += src[i + 1]; i += 2; continue; } }
      if (c === stringChar) { inString = false; }
      i++; continue;
    }
    if (c === '"' || c === "'") { inString = true; stringChar = c; out += c; i++; continue; }
    if (c === '/' && next === '/') { inLine = true; i += 2; continue; }
    if (c === '/' && next === '*') { inBlock = true; i += 2; continue; }
    out += c; i++;
  }
  // Trailing-comma sweep — only outside strings, but stripping happened above
  // so a regex over the comment-free output is safe.
  return out.replace(/,(\s*[}\]])/g, '$1');
}

// ── readSettings ───────────────────────────────────────────────────────────
// Try strict JSON first (fast path). On failure, strip comments and retry.
// On total failure return `null` and warn — never silently overwrite a
// malformed-but-recoverable file with `{}`.
function readSettings(p) {
  if (!fs.existsSync(p)) return {};
  let raw;
  try { raw = fs.readFileSync(p, 'utf8'); }
  catch (e) {
    process.stderr.write(`caveman: cannot read ${p}: ${e.message}\n`);
    return null;
  }
  if (!raw.trim()) return {};
  try { return JSON.parse(raw); } catch (_) { /* fall through to JSONC */ }
  try { return JSON.parse(stripJsonComments(raw)); }
  catch (e) {
    process.stderr.write(`caveman: warning — ${p} is not valid JSON or JSONC: ${e.message}\n`);
    return null;
  }
}

// ── writeSettings ──────────────────────────────────────────────────────────
// Atomic write: temp file + rename. mode 0600 (settings often contains tokens).
function writeSettings(p, obj) {
  const dir = path.dirname(p);
  fs.mkdirSync(dir, { recursive: true });
  const tmp = path.join(dir, `.${path.basename(p)}.${process.pid}.${crypto.randomBytes(4).toString('hex')}.tmp`);
  fs.writeFileSync(tmp, JSON.stringify(obj, null, 2) + '\n', { mode: 0o600 });
  fs.renameSync(tmp, p);
}

// ── validateHookFields ────────────────────────────────────────────────────
// Claude Code uses strict Zod on settings.json — a single malformed hook
// silently discards the entire file. Mutate-to-valid before write.
//
// Required shape (per Claude Code docs):
//   settings.hooks[event] = [{ hooks: [{ type:'command', command:'…', timeout?:n }, ...] }, ...]
//   settings.hooks[event] = [{ matcher?:'…', hooks: [...] }, ...]   // also valid
function validateHookFields(settings) {
  if (!settings || typeof settings !== 'object') return settings;
  if (!settings.hooks || typeof settings.hooks !== 'object') return settings;
  for (const ev of Object.keys(settings.hooks)) {
    const arr = settings.hooks[ev];
    if (!Array.isArray(arr)) { delete settings.hooks[ev]; continue; }
    settings.hooks[ev] = arr.filter(entry => {
      if (!entry || typeof entry !== 'object') return false;
      if (!Array.isArray(entry.hooks)) return false;
      entry.hooks = entry.hooks.filter(h => {
        if (!h || typeof h !== 'object') return false;
        if (h.type === 'command') return typeof h.command === 'string' && h.command.length > 0;
        if (h.type === 'agent')   return typeof h.prompt === 'string' && h.prompt.length > 0;
        return false;
      });
      return entry.hooks.length > 0;
    });
    if (settings.hooks[ev].length === 0) delete settings.hooks[ev];
  }
  if (Object.keys(settings.hooks).length === 0) delete settings.hooks;
  return settings;
}

// ── Idempotency probe ──────────────────────────────────────────────────────
function hasCavemanHook(settings, event, marker = 'caveman') {
  const arr = settings && settings.hooks && settings.hooks[event];
  if (!Array.isArray(arr)) return false;
  return arr.some(e =>
    e && Array.isArray(e.hooks) &&
    e.hooks.some(h => h && typeof h.command === 'string' && h.command.includes(marker))
  );
}

// ── addCommandHook ────────────────────────────────────────────────────────
// Idempotent push. `marker` defaults to opts.command — pass an explicit
// shorter substring (e.g. the script basename) when the full command path
// might rotate across reinstalls.
function addCommandHook(settings, event, opts) {
  if (!settings.hooks) settings.hooks = {};
  if (!Array.isArray(settings.hooks[event])) settings.hooks[event] = [];
  const marker = opts.marker || opts.command;
  if (hasCavemanHook(settings, event, marker)) return false;
  const hook = { type: 'command', command: opts.command };
  if (typeof opts.timeout === 'number') hook.timeout = opts.timeout;
  if (typeof opts.statusMessage === 'string') hook.statusMessage = opts.statusMessage;
  settings.hooks[event].push({ hooks: [hook] });
  return true;
}

// ── removeCavemanHooks ────────────────────────────────────────────────────
// Strip every entry whose any hook command mentions `marker`. Empties events.
function removeCavemanHooks(settings, marker = 'caveman') {
  if (!settings || !settings.hooks) return 0;
  let removed = 0;
  for (const ev of Object.keys(settings.hooks)) {
    const before = settings.hooks[ev].length;
    settings.hooks[ev] = settings.hooks[ev].filter(entry => {
      if (!entry || !Array.isArray(entry.hooks)) return true;
      return !entry.hooks.some(h => h && typeof h.command === 'string' && h.command.includes(marker));
    });
    removed += before - settings.hooks[ev].length;
    if (settings.hooks[ev].length === 0) delete settings.hooks[ev];
  }
  if (Object.keys(settings.hooks).length === 0) delete settings.hooks;
  return removed;
}

// ── rewriteLegacyManagedHookCommands ──────────────────────────────────────
// Walk every hook command. If it's a bare `node /path/to/<managed>.js` (no
// absolute node path) and the basename is one of ours, rewrite to use
// `absoluteNode` so GUI launchers with minimal PATH still find Node. Only
// touches commands matching the exact bare-node shape — won't false-positive
// on user-authored hooks that just happen to mention "caveman".
const MANAGED_HOOK_BASENAMES = new Set([
  'caveman-activate.js',
  'caveman-mode-tracker.js',
  'caveman-stats.js',
  'caveman-statusline.sh',
]);
function rewriteLegacyManagedHookCommands(settings, absoluteNode) {
  if (!settings || !settings.hooks || !absoluteNode) return 0;
  let rewritten = 0;
  const reBare = /^node\s+("([^"]+)"|'([^']+)'|(\S+))\s*$/;
  for (const ev of Object.keys(settings.hooks)) {
    for (const entry of settings.hooks[ev]) {
      if (!entry || !Array.isArray(entry.hooks)) continue;
      for (const h of entry.hooks) {
        if (!h || typeof h.command !== 'string') continue;
        const m = reBare.exec(h.command);
        if (!m) continue;
        const scriptPath = m[2] || m[3] || m[4];
        const basename = path.basename(scriptPath);
        if (!MANAGED_HOOK_BASENAMES.has(basename)) continue;
        h.command = `"${absoluteNode}" "${scriptPath}"`;
        rewritten++;
      }
    }
  }
  return rewritten;
}

// ── claudeConfigDir ───────────────────────────────────────────────────────
function claudeConfigDir() {
  if (process.env.CLAUDE_CONFIG_DIR) return process.env.CLAUDE_CONFIG_DIR;
  return path.join(os.homedir(), '.claude');
}

module.exports = {
  stripJsonComments,
  readSettings,
  writeSettings,
  validateHookFields,
  hasCavemanHook,
  addCommandHook,
  removeCavemanHooks,
  rewriteLegacyManagedHookCommands,
  claudeConfigDir,
  MANAGED_HOOK_BASENAMES,
};
