'use strict';

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const VERSION = 1;

function pathExists(target) {
  try {
    fs.lstatSync(target);
    return true;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw error;
  }
}

function safeRelative(value) {
  if (typeof value !== 'string' || value === '' || path.isAbsolute(value)) {
    throw new Error(`invalid owned path: ${JSON.stringify(value)}`);
  }
  const normalized = value.replaceAll('\\', '/');
  if (normalized === '..' || normalized.startsWith('../') || normalized.includes('/../')) {
    throw new Error(`owned path escapes integration root: ${value}`);
  }
  return normalized;
}

function destination(root, relativePath) {
  const relative = safeRelative(relativePath);
  const target = path.resolve(root, ...relative.split('/'));
  const check = path.relative(path.resolve(root), target);
  if (check === '..' || check.startsWith(`..${path.sep}`) || path.isAbsolute(check)) {
    throw new Error(`owned path escapes integration root: ${relativePath}`);
  }
  let current = path.resolve(root);
  for (const segment of relative.split('/').slice(0, -1)) {
    current = path.join(current, segment);
    if (!pathExists(current)) break;
    if (fs.lstatSync(current).isSymbolicLink()) {
      throw new Error(`symbolic-link ownership ancestor refused: ${current}`);
    }
  }
  return target;
}

function updateDigest(hash, root, target) {
  const stat = fs.lstatSync(target);
  const relative = path.relative(root, target).split(path.sep).join('/');
  if (stat.isSymbolicLink()) throw new Error(`symbolic-link ownership refused: ${target}`);
  if (stat.isDirectory()) {
    hash.update(`dir\0${relative}\0`);
    for (const name of fs.readdirSync(target).sort()) updateDigest(hash, root, path.join(target, name));
    return;
  }
  if (!stat.isFile()) throw new Error(`unsupported owned path type: ${target}`);
  hash.update(`file\0${relative}\0`);
  hash.update(fs.readFileSync(target));
  hash.update('\0');
}

function digestPath(target) {
  const hash = crypto.createHash('sha256');
  // Root the digest at the payload itself so a staged path and its final path
  // compare byte-for-byte despite having different temporary basenames.
  updateDigest(hash, target, target);
  return hash.digest('hex');
}

function copyPath(source, target) {
  const stat = fs.lstatSync(source);
  if (stat.isSymbolicLink()) throw new Error(`symbolic-link copy refused: ${source}`);
  if (stat.isDirectory()) {
    fs.mkdirSync(target, { recursive: true, mode: 0o700 });
    for (const name of fs.readdirSync(source)) copyPath(path.join(source, name), path.join(target, name));
    return;
  }
  if (!stat.isFile()) throw new Error(`unsupported copy path type: ${source}`);
  fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
  fs.copyFileSync(source, target);
}

function removePath(target) {
  fs.rmSync(target, { recursive: true, force: true });
}

function atomicJSON(target, value) {
  fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
  const temporary = `${target}.tmp-${process.pid}-${crypto.randomUUID()}`;
  try {
    fs.writeFileSync(temporary, JSON.stringify(value, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    fs.renameSync(temporary, target);
  } finally {
    try { fs.unlinkSync(temporary); } catch (_) {}
  }
}

function emptyJournal(integration) {
  return { version: VERSION, integration, entries: {} };
}

function loadJournal(journalPath, integration) {
  if (!pathExists(journalPath)) return emptyJournal(integration);
  const journalStat = fs.lstatSync(journalPath);
  if (journalStat.isSymbolicLink() || !journalStat.isFile()) {
    throw new Error(`invalid ${integration} ownership journal path`);
  }
  let parsed;
  try {
    parsed = JSON.parse(fs.readFileSync(journalPath, 'utf8'));
  } catch (error) {
    throw new Error(`invalid ${integration} ownership journal: ${error.message}`);
  }
  if (parsed?.version !== VERSION || parsed?.integration !== integration ||
      parsed.entries === null || typeof parsed.entries !== 'object' || Array.isArray(parsed.entries)) {
    throw new Error(`invalid ${integration} ownership journal schema`);
  }
  for (const [relative, entry] of Object.entries(parsed.entries)) {
    safeRelative(relative);
    if (!entry || typeof entry !== 'object' || !/^[a-f0-9]{64}$/.test(entry.installedDigest || '')) {
      throw new Error(`invalid ${integration} ownership journal entry: ${relative}`);
    }
    if (entry.restoreBackup !== undefined) safeRelative(entry.restoreBackup);
    for (const backup of entry.preservedBackups || []) safeRelative(backup);
  }
  return parsed;
}

function backupCurrent(root, backupRoot, relative, entry) {
  const source = destination(root, relative);
  const backupName = `${crypto.createHash('sha256').update(relative).digest('hex').slice(0, 16)}-${Date.now()}-${crypto.randomUUID()}`;
  const target = destination(backupRoot, backupName);
  const temporary = `${target}.tmp`;
  if (pathExists(backupRoot)) {
    const stat = fs.lstatSync(backupRoot);
    if (stat.isSymbolicLink() || !stat.isDirectory()) throw new Error(`invalid backup root: ${backupRoot}`);
  }
  fs.mkdirSync(backupRoot, { recursive: true, mode: 0o700 });
  try {
    copyPath(source, temporary);
    fs.renameSync(temporary, target);
  } finally {
    removePath(temporary);
  }
  if (entry?.restoreBackup) {
    const preservedBackups = Array.isArray(entry.preservedBackups) ? [...entry.preservedBackups] : [];
    preservedBackups.push(backupName);
    return { ...entry, preservedBackups };
  }
  return { ...(entry || {}), restoreBackup: backupName };
}

function journalPaths(root, integration) {
  return {
    journalPath: path.join(root, `.caveman-${integration}-ownership.json`),
    backupRoot: path.join(root, `.caveman-${integration}-backups`),
  };
}

function writeJournal(journalPath, journal) {
  if (Object.keys(journal.entries).length === 0) {
    try { fs.unlinkSync(journalPath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    return;
  }
  atomicJSON(journalPath, journal);
}

function preflightOwnedInstall({ root, integration, operations, force = false }) {
  const { journalPath } = journalPaths(root, integration);
  const journal = loadJournal(journalPath, integration);
  const conflicts = [];
  const seen = new Set();
  for (const operation of operations) {
    const relative = safeRelative(operation.relativePath);
    if (seen.has(relative)) throw new Error(`duplicate owned path: ${relative}`);
    seen.add(relative);
    const target = destination(root, relative);
    if (!pathExists(target)) continue;
    const entry = journal.entries[relative];
    let currentDigest;
    try { currentDigest = digestPath(target); }
    catch (error) { conflicts.push(`${relative} (${error.message})`); continue; }
    if (!entry || currentDigest !== entry.installedDigest) conflicts.push(relative);
  }
  if (conflicts.length && !force) {
    throw new Error(
      `${integration} ownership conflict: ${conflicts.join(', ')}; refusing to overwrite user content (re-run with --force to back it up)`,
    );
  }
  if (conflicts.some((value) => value.includes('symbolic-link'))) {
    throw new Error(`${integration} ownership conflict: symbolic links are never overwritten`);
  }
  return journal;
}

function installOwned({ root, integration, operations, force = false, note = () => {} }) {
  const { journalPath, backupRoot } = journalPaths(root, integration);
  const journal = preflightOwnedInstall({ root, integration, operations, force });
  fs.mkdirSync(root, { recursive: true, mode: 0o700 });
  for (const operation of operations) {
    const relative = safeRelative(operation.relativePath);
    const target = destination(root, relative);
    const existingEntry = journal.entries[relative];
    const exists = pathExists(target);
    const currentDigest = exists ? digestPath(target) : null;
    const unchangedOwned = exists && existingEntry && currentDigest === existingEntry.installedDigest;

    const stage = `${target}.caveman-stage-${process.pid}-${crypto.randomUUID()}`;
    const displaced = `${target}.caveman-old-${process.pid}-${crypto.randomUUID()}`;
    fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
    let entry = existingEntry ? { ...existingEntry } : {};
    let swapped = false;
    let committed = false;
    let backupNamesBefore = new Set([
      entry.restoreBackup,
      ...(Array.isArray(entry.preservedBackups) ? entry.preservedBackups : []),
    ].filter(Boolean));
    try {
      operation.write(stage);
      if (!pathExists(stage)) throw new Error(`owned installer did not materialize ${relative}`);
      const stagedDigest = digestPath(stage);
      if (unchangedOwned && !force && stagedDigest === currentDigest) {
        note(`  kept owned ${target}`);
        continue;
      }
      if (exists && !unchangedOwned) {
        entry = backupCurrent(root, backupRoot, relative, entry);
        note(`  backed up user content at ${target}`);
      }
      if (exists) fs.renameSync(target, displaced);
      try {
        fs.renameSync(stage, target);
        swapped = true;
      } catch (error) {
        if (pathExists(displaced) && !pathExists(target)) fs.renameSync(displaced, target);
        throw error;
      }
      entry.installedDigest = digestPath(target);
      journal.entries[relative] = entry;
      writeJournal(journalPath, journal);
      committed = true;
      removePath(displaced);
      note(`  installed owned ${target}`);
    } finally {
      removePath(stage);
      if (swapped && !committed && pathExists(target)) removePath(target);
      if (pathExists(displaced) && !pathExists(target)) fs.renameSync(displaced, target);
      else removePath(displaced);
      if (!committed) {
        for (const backupName of [entry.restoreBackup, ...(entry.preservedBackups || [])]) {
          if (backupName && !backupNamesBefore.has(backupName)) {
            removePath(destination(backupRoot, backupName));
          }
        }
      }
    }
  }
  return { journalPath, journal };
}

function uninstallOwned({ root, integration, dryRun = false, note = () => {}, warn = () => {} }) {
  const { journalPath, backupRoot } = journalPaths(root, integration);
  if (!pathExists(journalPath)) return { hadJournal: false, changed: [], preserved: [] };
  const journal = loadJournal(journalPath, integration);
  const changed = [];
  const preserved = [];
  for (const relative of Object.keys(journal.entries).sort()) {
    const entry = journal.entries[relative];
    const target = destination(root, relative);
    if (pathExists(target)) {
      let digest;
      try { digest = digestPath(target); }
      catch (error) {
        warn(`  left ${target}: ${error.message}`);
        changed.push(relative);
        continue;
      }
      if (digest !== entry.installedDigest) {
        warn(`  left modified ${target}; ownership digest no longer matches`);
        changed.push(relative);
        continue;
      }
    }
    if (dryRun) {
      note(entry.restoreBackup ? `  would restore backup over ${target}` : `  would remove owned ${target}`);
      continue;
    }
    const restoreBackup = entry.restoreBackup ? destination(backupRoot, entry.restoreBackup) : null;
    if (restoreBackup && !pathExists(restoreBackup)) {
      warn(`  backup missing for ${target}; ownership journal retained`);
      changed.push(relative);
      continue;
    }
    removePath(target);
    if (restoreBackup) {
      fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
      fs.renameSync(restoreBackup, target);
      note(`  restored user backup to ${target}`);
    } else {
      note(`  removed owned ${target}`);
    }
    for (const backupName of entry.preservedBackups || []) {
      const backup = destination(backupRoot, backupName);
      if (pathExists(backup)) preserved.push(backup);
    }
    delete journal.entries[relative];
    writeJournal(journalPath, journal);
  }
  if (!dryRun && Object.keys(journal.entries).length === 0 && pathExists(backupRoot)) {
    const remaining = fs.readdirSync(backupRoot);
    if (remaining.length === 0) removePath(backupRoot);
    else warn(`  preserved additional user backups at ${backupRoot}`);
  }
  return { hadJournal: true, changed, preserved };
}

module.exports = {
  copyPath,
  digestPath,
  installOwned,
  journalPaths,
  preflightOwnedInstall,
  uninstallOwned,
};
