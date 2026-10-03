// caveman — installer transaction receipt (last-run verify / revert).
//
// Records every owned write, settings merge, marker fence, and external
// package command performed by bin/install.js. --verify-last checks the last
// receipt still matches disk. --revert-last walks that receipt in reverse.
// --uninstall remains the full wipe. command entries are listed and never
// spawned on revert (npx skills / claude plugin / gemini extensions / claude mcp).
//
// Independent of the CLI wrap journal. Stdlib only.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const VERSION = 1;
const RECEIPT_NAME = 'last-install.json';
const PREV_NAME = 'last-install.prev.json';
const REVERTED_NAME = 'last-install.reverted.json';
const DEFAULT_BEGIN = '<!-- caveman-begin -->';
const DEFAULT_END = '<!-- caveman-end -->';

function userConfigDir(env = process.env, platform = process.platform, homedir = os.homedir()) {
  // Copied from src/hooks/caveman-config.js getConfigDir(). Do not require the
  // hook file — curl|bash has not copied hooks yet.
  if (env.XDG_CONFIG_HOME) return path.join(env.XDG_CONFIG_HOME, 'caveman');
  if (platform === 'win32') {
    return path.join(env.APPDATA || path.join(homedir, 'AppData', 'Roaming'), 'caveman');
  }
  return path.join(homedir, '.config', 'caveman');
}

function receiptPath(env, platform, homedir) {
  return path.join(userConfigDir(env, platform, homedir), RECEIPT_NAME);
}

function prevReceiptPath(env, platform, homedir) {
  return path.join(userConfigDir(env, platform, homedir), PREV_NAME);
}

function revertedReceiptPath(env, platform, homedir) {
  return path.join(userConfigDir(env, platform, homedir), REVERTED_NAME);
}

function resolveSafePath(p) {
  if (typeof p !== 'string' || p.length === 0) throw new Error('receipt path missing');
  if (p.includes('\0')) throw new Error('receipt path contains NUL');
  return path.resolve(p);
}

function lstatOrNull(p) {
  try { return fs.lstatSync(p); } catch (e) {
    if (e && e.code === 'ENOENT') return null;
    throw e;
  }
}

function refuseSymlinkPath(target, label) {
  const abs = resolveSafePath(target);
  const parent = path.dirname(abs);
  const parentStat = lstatOrNull(parent);
  if (parentStat && parentStat.isSymbolicLink()) {
    throw new Error(`${label} parent is a symlink: ${parent}`);
  }
  const self = lstatOrNull(abs);
  if (self && self.isSymbolicLink()) {
    throw new Error(`${label} is a symlink: ${abs}`);
  }
  return abs;
}

function sha256Bytes(buf) {
  return crypto.createHash('sha256').update(buf).digest('hex');
}

function sha256File(absPath) {
  return sha256Bytes(fs.readFileSync(resolveSafePath(absPath)));
}

function utcCompact(d = new Date()) {
  return d.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

function beginReceipt({ argv = [], pinnedRef = '', configDir = '' } = {}) {
  return {
    version: VERSION,
    id: `${utcCompact()}-${crypto.randomBytes(4).toString('hex')}`,
    createdAt: new Date().toISOString(),
    argv: Array.isArray(argv) ? argv.slice() : [],
    pinnedRef: pinnedRef == null ? '' : String(pinnedRef),
    configDir: configDir ? resolveSafePath(String(configDir)) : '',
    entries: [],
  };
}

function pushEntry(receipt, entry) {
  if (!receipt || !Array.isArray(receipt.entries)) return;
  receipt.entries.push(entry);
}

function recordWrite(receipt, { path: filePath, created, previousSha256, previousBytes } = {}) {
  if (!receipt) return;
  let abs;
  try { abs = resolveSafePath(filePath); } catch (e) {
    process.stderr.write(`caveman: receipt skipped write: ${e.message}\n`);
    return;
  }
  let digest;
  try { digest = sha256File(abs); } catch (e) {
    process.stderr.write(`caveman: receipt skipped write ${abs}: ${e.message}\n`);
    return;
  }
  const entry = {
    op: 'write',
    path: abs,
    sha256: digest,
    created: !!created,
  };
  if (!created && typeof previousSha256 === 'string' && previousSha256.length) {
    entry.previousSha256 = previousSha256;
    const bytes = Buffer.isBuffer(previousBytes)
      ? previousBytes
      : (typeof previousBytes === 'string' ? Buffer.from(previousBytes) : null);
    if (bytes) entry.previousBase64 = bytes.toString('base64');
  }
  pushEntry(receipt, entry);
}

function recordMerge(receipt, { path: filePath, marker = 'caveman', keys = [] } = {}) {
  if (!receipt) return;
  let abs;
  try { abs = resolveSafePath(filePath); } catch (e) {
    process.stderr.write(`caveman: receipt skipped merge: ${e.message}\n`);
    return;
  }
  pushEntry(receipt, {
    op: 'merge',
    path: abs,
    marker: marker || 'caveman',
    keys: Array.isArray(keys) ? keys.slice() : [],
  });
}

function recordFence(receipt, { path: filePath, begin = DEFAULT_BEGIN, end = DEFAULT_END } = {}) {
  if (!receipt) return;
  let abs;
  try { abs = resolveSafePath(filePath); } catch (e) {
    process.stderr.write(`caveman: receipt skipped fence: ${e.message}\n`);
    return;
  }
  pushEntry(receipt, {
    op: 'fence',
    path: abs,
    begin: begin || DEFAULT_BEGIN,
    end: end || DEFAULT_END,
  });
}

function recordCommand(receipt, { argv, revert = 'manual' } = {}) {
  if (!receipt) return;
  if (!Array.isArray(argv) || argv.length === 0) return;
  pushEntry(receipt, {
    op: 'command',
    argv: argv.map(String),
    revert: 'manual',
  });
  void revert;
}

function serializeReceipt(receipt) {
  return JSON.stringify({
    version: receipt.version,
    id: receipt.id,
    createdAt: receipt.createdAt,
    argv: receipt.argv,
    pinnedRef: receipt.pinnedRef,
    configDir: receipt.configDir,
    entries: receipt.entries,
  }, null, 2) + '\n';
}

function atomicWriteFile(dest, content, mode) {
  const dir = path.dirname(dest);
  const tmp = path.join(dir, `.${path.basename(dest)}.${process.pid}.${crypto.randomBytes(4).toString('hex')}.tmp`);
  try {
    const fd = fs.openSync(tmp, 'wx', mode);
    try {
      fs.writeFileSync(fd, content);
      try { fs.fchmodSync(fd, mode); } catch (_) { /* windows */ }
    } finally {
      fs.closeSync(fd);
    }
    fs.renameSync(tmp, dest);
  } catch (error) {
    try { fs.unlinkSync(tmp); } catch (_) {}
    throw error;
  }
}

function writeReceipt(receipt, env, platform, homedir) {
  if (!receipt || !Array.isArray(receipt.entries)) {
    throw new Error('invalid receipt');
  }
  const dest = refuseSymlinkPath(receiptPath(env, platform, homedir), 'receipt');
  const dir = path.dirname(dest);
  const parentStat = lstatOrNull(dir);
  if (parentStat && parentStat.isSymbolicLink()) {
    throw new Error(`receipt parent is a symlink: ${dir}`);
  }
  if (!parentStat) {
    const grand = path.dirname(dir);
    const grandStat = lstatOrNull(grand);
    if (grandStat && grandStat.isSymbolicLink()) {
      throw new Error(`receipt parent is a symlink: ${grand}`);
    }
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  }
  try { fs.chmodSync(dir, 0o700); } catch (_) { /* windows / not ours */ }

  const prev = path.join(dir, PREV_NAME);
  const existing = lstatOrNull(dest);
  if (existing && existing.isFile() && !existing.isSymbolicLink()) {
    try {
      refuseSymlinkPath(prev, 'previous receipt');
      fs.copyFileSync(dest, prev);
      try { fs.chmodSync(prev, 0o600); } catch (_) {}
    } catch (_) { /* last-writer-wins; a failed prev copy must not block */ }
  }

  atomicWriteFile(dest, serializeReceipt(receipt), 0o600);
  return dest;
}

function readReceipt(env, platform, homedir) {
  const dest = receiptPath(env, platform, homedir);
  let st;
  try { st = fs.lstatSync(dest); } catch (e) {
    if (e && e.code === 'ENOENT') return null;
    throw e;
  }
  if (st.isSymbolicLink() || !st.isFile()) {
    throw new Error(`unreadable receipt: ${dest}`);
  }
  let parsed;
  try {
    parsed = JSON.parse(fs.readFileSync(dest, 'utf8'));
  } catch (e) {
    throw new Error(`unreadable receipt: ${e.message}`);
  }
  if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.entries)) {
    throw new Error('unreadable receipt: invalid schema');
  }
  return parsed;
}

function defaultLog() {
  return {
    write: (s) => process.stdout.write(s),
    warn: (s) => process.stderr.write(s + (s.endsWith('\n') ? '' : '\n')),
  };
}

function inspectPath(abs) {
  const resolved = path.resolve(abs);
  let st;
  try { st = fs.lstatSync(resolved); } catch (e) {
    if (e && e.code === 'ENOENT') return { missing: true, path: resolved };
    throw e;
  }
  if (st.isSymbolicLink()) return { symlink: true, path: resolved };
  if (!st.isFile()) return { notFile: true, path: resolved };
  return { path: resolved, sha256: sha256File(resolved), stat: st };
}

function getByDottedKey(obj, key) {
  if (!obj || typeof obj !== 'object' || typeof key !== 'string') return undefined;
  return key.split('.').reduce((acc, part) => {
    if (acc == null || typeof acc !== 'object') return undefined;
    return acc[part];
  }, obj);
}

function statusLineCommand(settings) {
  if (!settings || settings.statusLine == null) return '';
  if (typeof settings.statusLine === 'string') return settings.statusLine;
  return settings.statusLine.command || '';
}

function verifyMerge(entry, settingsApi) {
  const inspected = inspectPath(entry.path);
  if (inspected.missing) return 'missing';
  if (inspected.symlink) return 'symlink';
  const parsed = settingsApi.readSettings(entry.path);
  if (!parsed) return 'missing';
  const marker = entry.marker || 'caveman';
  for (const key of entry.keys || []) {
    if (key === 'statusLine' || key === 'statusLine.command') {
      if (!statusLineCommand(parsed).includes('caveman-statusline')) return 'missing';
      continue;
    }
    if (key.startsWith('hooks.')) {
      const ev = key.slice('hooks.'.length);
      if (!settingsApi.hasCavemanHook(parsed, ev, marker)) return 'missing';
      continue;
    }
    const val = getByDottedKey(parsed, key);
    if (val == null) return 'missing';
    const blob = typeof val === 'string' ? val : JSON.stringify(val);
    if (!blob.includes(marker)) return 'missing';
  }
  return 'ok';
}

function verifyFence(entry) {
  const inspected = inspectPath(entry.path);
  if (inspected.missing) return 'missing';
  if (inspected.symlink) return 'symlink';
  const text = fs.readFileSync(entry.path, 'utf8');
  const begin = entry.begin || DEFAULT_BEGIN;
  const end = entry.end || DEFAULT_END;
  const b = text.indexOf(begin);
  const e = text.indexOf(end);
  if (b === -1 || e === -1 || b >= e) return 'missing';
  return 'ok';
}

function verifyWrite(entry) {
  const inspected = inspectPath(entry.path);
  if (inspected.missing) return 'missing';
  if (inspected.symlink) return 'symlink';
  if (inspected.notFile) return 'missing';
  if (inspected.sha256 !== entry.sha256) return 'hash-mismatch';
  return 'ok';
}

function verifyReceipt(receipt, { settings, log } = {}) {
  const out = log || defaultLog();
  const settingsApi = settings || require('./settings');
  let ok = true;
  const results = [];
  if (!receipt || !Array.isArray(receipt.entries)) {
    return { ok: false, results };
  }
  for (const entry of receipt.entries) {
    const op = entry && entry.op;
    let status;
    let label;
    if (op === 'write') {
      status = verifyWrite(entry);
      label = entry.path;
      if (status !== 'ok') ok = false;
    } else if (op === 'merge') {
      status = verifyMerge(entry, settingsApi);
      label = entry.path;
      if (status !== 'ok') ok = false;
    } else if (op === 'fence') {
      status = verifyFence(entry);
      label = entry.path;
      if (status !== 'ok') ok = false;
    } else if (op === 'command') {
      status = 'manual';
      label = (entry.argv || []).join(' ');
    } else {
      out.warn(`caveman: skipping unknown receipt op ${JSON.stringify(op)}`);
      continue;
    }
    out.write(`${status}  ${label}\n`);
    results.push({ op, status, label });
  }
  return { ok, results };
}

function isHooksPackageJson(abs) {
  return path.basename(abs) === 'package.json' && path.basename(path.dirname(abs)) === 'hooks';
}

function stripExactFence(text, begin, end) {
  const b = text.indexOf(begin);
  const e = text.indexOf(end);
  if (b === -1 && e === -1) return { skip: true };
  if (b === -1 || e === -1 || e < b) return { unpaired: true };
  const afterEnd = e + end.length;
  const before = text.slice(0, b).replace(/\n+$/, '\n');
  const after = text.slice(afterEnd).replace(/^\n+/, '\n');
  let next = (before + after).trimEnd();
  next = next ? next + '\n' : '';
  return { next };
}

function previousBytesFromEntry(entry) {
  if (typeof entry.previousBase64 === 'string') {
    return Buffer.from(entry.previousBase64, 'base64');
  }
  return null;
}

function revertWrite(entry, { dryRun, warn, note, hooksManifestIsOurs }) {
  const abs = path.resolve(entry.path);
  const inspected = inspectPath(abs);
  if (inspected.missing) {
    note(`  skip missing ${abs}`);
    return { ok: true };
  }
  if (inspected.symlink) {
    warn(`  left symlink ${abs}`);
    return { ok: true, warned: true };
  }
  if (inspected.notFile) {
    warn(`  left non-file ${abs}`);
    return { ok: true, warned: true };
  }
  if (inspected.sha256 !== entry.sha256) {
    warn(`  left ${abs} (hash mismatch — not deleting or restoring)`);
    return { ok: true, warned: true };
  }
  if (isHooksPackageJson(abs)) {
    const ours = typeof hooksManifestIsOurs === 'function' && hooksManifestIsOurs(abs);
    if (!ours) {
      warn(`  left ${abs} (hooks/package.json is not ours)`);
      return { ok: true, warned: true };
    }
  }
  if (entry.created) {
    if (dryRun) {
      note(`  would delete ${abs}`);
      return { ok: true };
    }
    fs.unlinkSync(abs);
    note(`  deleted ${abs}`);
    return { ok: true };
  }
  if (!entry.previousSha256) {
    warn(`  restore unavailable for ${abs} (no previousSha256)`);
    return { ok: true, warned: true };
  }
  const prev = previousBytesFromEntry(entry);
  if (!prev) {
    warn(`  restore unavailable for ${abs} (previous bytes not stored)`);
    return { ok: true, warned: true };
  }
  if (sha256Bytes(prev) !== entry.previousSha256) {
    warn(`  restore unavailable for ${abs} (previous bytes do not match previousSha256)`);
    return { ok: true, warned: true };
  }
  if (dryRun) {
    note(`  would restore ${abs}`);
    return { ok: true };
  }
  atomicWriteFile(abs, prev, 0o644);
  note(`  restored ${abs}`);
  return { ok: true };
}

function revertMerge(entry, { dryRun, warn, note, settings }) {
  const abs = path.resolve(entry.path);
  const settingsApi = settings || require('./settings');
  const parsed = settingsApi.readSettings(abs);
  if (!parsed) {
    warn(`  could not parse ${abs} — leaving the hook files in place.`);
    warn('  Remove the caveman entries from it by hand, then re-run --revert-last.');
    return { ok: false, hardStop: true };
  }
  if (dryRun) {
    note(`  would revert merge ${abs}`);
    return { ok: true };
  }
  settingsApi.removeCavemanHooks(parsed);
  if (parsed.statusLine) {
    const cmd = statusLineCommand(parsed);
    if (cmd.includes('caveman-statusline')) delete parsed.statusLine;
  }
  const marker = entry.marker || 'caveman';
  for (const key of entry.keys || []) {
    if (key === 'plugin' && Array.isArray(parsed.plugin)) {
      parsed.plugin = parsed.plugin.filter(p => !String(p).includes(marker));
      if (parsed.plugin.length === 0) delete parsed.plugin;
    }
    if ((key === 'mcp' || key === 'mcp.caveman-shrink') && parsed.mcp && typeof parsed.mcp === 'object') {
      if (parsed.mcp['caveman-shrink']) delete parsed.mcp['caveman-shrink'];
      if (Object.keys(parsed.mcp).length === 0) delete parsed.mcp;
    }
  }
  settingsApi.validateHookFields(parsed);
  try {
    settingsApi.writeSettings(abs, parsed);
  } catch (e) {
    warn(`  could not update ${abs}: ${e && e.message || e}`);
    warn('  leaving the hook files in place so the entries it still holds keep resolving.');
    return { ok: false, hardStop: true };
  }
  note(`  reverted merge ${abs}`);
  return { ok: true };
}

function revertFence(entry, { dryRun, warn, note }) {
  const abs = path.resolve(entry.path);
  const inspected = inspectPath(abs);
  if (inspected.missing) {
    note(`  skip missing fence ${abs}`);
    return { ok: true };
  }
  if (inspected.symlink) {
    warn(`  left symlink fence ${abs}`);
    return { ok: true, warned: true };
  }
  const begin = entry.begin || DEFAULT_BEGIN;
  const end = entry.end || DEFAULT_END;
  const text = fs.readFileSync(abs, 'utf8');
  const stripped = stripExactFence(text, begin, end);
  if (stripped.skip) {
    note(`  skip fence ${abs} (markers absent)`);
    return { ok: true };
  }
  if (stripped.unpaired) {
    warn(`  left ${abs} (begin marker without matching end — will not splice)`);
    return { ok: true, warned: true };
  }
  if (dryRun) {
    note(`  would strip fence ${abs}`);
    return { ok: true };
  }
  if (stripped.next === '') {
    fs.unlinkSync(abs);
    note(`  removed ${abs}`);
  } else {
    atomicWriteFile(abs, stripped.next, 0o644);
    note(`  stripped fence ${abs}`);
  }
  return { ok: true };
}

function revertCommand(entry, { note }) {
  const argv = Array.isArray(entry.argv) ? entry.argv : [];
  note(`  manual  ${argv.join(' ')}  revert: manual`);
  return { ok: true };
}

function revertReceipt(receipt, opts = {}) {
  const log = opts.log || defaultLog();
  const note = opts.note || ((s) => log.write(s + (s.endsWith('\n') ? '' : '\n')));
  const warn = opts.warn || ((s) => log.warn(s));
  const dryRun = !!opts.dryRun;
  const ctx = {
    dryRun,
    warn,
    note,
    hooksManifestIsOurs: opts.hooksManifestIsOurs,
    settings: opts.settings,
    openclaw: opts.openclaw,
  };
  if (!receipt || !Array.isArray(receipt.entries)) {
    return { ok: false, hardStop: false };
  }
  const reversed = receipt.entries.slice().reverse();
  for (const entry of reversed) {
    const op = entry && entry.op;
    let result;
    if (op === 'write') result = revertWrite(entry, ctx);
    else if (op === 'merge') result = revertMerge(entry, ctx);
    else if (op === 'fence') result = revertFence(entry, ctx);
    else if (op === 'command') result = revertCommand(entry, ctx);
    else {
      warn(`caveman: skipping unknown receipt op ${JSON.stringify(op)}`);
      continue;
    }
    if (result && result.hardStop) return { ok: false, hardStop: true };
  }
  return { ok: true, hardStop: false };
}

function markReceiptReverted(env, platform, homedir) {
  const dest = refuseSymlinkPath(receiptPath(env, platform, homedir), 'receipt');
  const reverted = path.join(path.dirname(dest), REVERTED_NAME);
  refuseSymlinkPath(reverted, 'reverted receipt');
  const st = lstatOrNull(dest);
  if (!st) throw new Error('no last-install receipt');
  fs.renameSync(dest, reverted);
  try { fs.chmodSync(reverted, 0o600); } catch (_) {}
  return reverted;
}

module.exports = {
  VERSION,
  RECEIPT_NAME,
  DEFAULT_BEGIN,
  DEFAULT_END,
  userConfigDir,
  receiptPath,
  prevReceiptPath,
  revertedReceiptPath,
  readReceipt,
  writeReceipt,
  sha256File,
  sha256Bytes,
  beginReceipt,
  recordWrite,
  recordMerge,
  recordFence,
  recordCommand,
  verifyReceipt,
  revertReceipt,
  markReceiptReverted,
  resolveSafePath,
};
