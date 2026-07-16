'use strict';

const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const childProcess = require('child_process');

const PLUGIN = 'caveman';
const SKILL_DIRS = ['caveman', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];
const MANIFEST_VERSION = 1;
const LIFECYCLE_HELPER = path.resolve(__dirname, '..', '..', 'hermes_caveman', 'lifecycle_helper.py');

function assertHermesPlatform(platform = process.platform) {
  if (platform === 'win32') {
    throw new Error('native Hermes integration requires Linux, macOS, or WSL2; native Windows is not supported');
  }
}

function spawnHermes(args, env) {
  const result = childProcess.spawnSync('hermes', args, {
    encoding: 'utf8',
    env,
    windowsHide: true,
  });
  if (result.error || result.status !== 0) {
    const detail = result.error?.message || String(result.stderr || result.stdout || '').trim() || `exit ${result.status}`;
    throw new Error(`hermes ${args.join(' ')} failed: ${detail}`);
  }
  return result;
}

let cachedHermesHome = null;
function hermesHome() {
  if (cachedHermesHome) return cachedHermesHome;
  const result = spawnHermes(['config', 'path'], process.env);
  const lines = String(result.stdout || '').split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  const configPaths = lines.filter(line => path.isAbsolute(line) && path.basename(line) === 'config.yaml');
  if (configPaths.length !== 1) {
    throw new Error('Hermes config path was not reported');
  }
  const [configPath] = configPaths;
  cachedHermesHome = path.dirname(path.resolve(configPath));
  return cachedHermesHome;
}
function hermesEnv() { return { ...process.env, HERMES_HOME: hermesHome() }; }
function runHermes(args) { return spawnHermes(args, hermesEnv()); }
function pluginDir() { return path.join(hermesHome(), 'plugins', PLUGIN); }
function stateDir() { return path.join(hermesHome(), 'caveman'); }
function manifestPath() { return path.join(stateDir(), 'install-manifest.json'); }
function shaBytes(value) { return crypto.createHash('sha256').update(value).digest('hex'); }
function shaFile(file) { return shaBytes(fs.readFileSync(file)); }
function deepEqual(left, right) { return JSON.stringify(left) === JSON.stringify(right); }

let cachedHermesPython = null;
function hermesPython() {
  if (cachedHermesPython) return cachedHermesPython;
  const explicit = process.env.CAVEMAN_HERMES_PYTHON;
  if (explicit) {
    if (!path.isAbsolute(explicit) || !fs.statSync(explicit).isFile()) {
      throw new Error('CAVEMAN_HERMES_PYTHON must name an absolute regular file');
    }
    cachedHermesPython = explicit;
    return explicit;
  }
  const shown = runHermes(['config', 'show']);
  const match = String(shown.stdout || '').match(/^\s*Install:\s*(.+?)\s*$/m);
  if (!match || !path.isAbsolute(match[1])) throw new Error('Hermes install path was not reported');
  const install = match[1];
  const candidates = process.platform === 'win32'
    ? [path.join(install, 'venv', 'Scripts', 'python.exe'), path.join(install, '.venv', 'Scripts', 'python.exe')]
    : [
        path.join(install, 'venv', 'bin', 'python3'),
        path.join(install, 'venv', 'bin', 'python'),
        path.join(install, '.venv', 'bin', 'python3'),
        path.join(install, '.venv', 'bin', 'python'),
      ];
  const selected = candidates.find(candidate => {
    try { return fs.statSync(candidate).isFile(); } catch (_) { return false; }
  });
  if (!selected) throw new Error(`Hermes Python was not found under ${install}`);
  cachedHermesPython = selected;
  return selected;
}

function runLifecycleHelper(args) {
  const result = childProcess.spawnSync(hermesPython(), [LIFECYCLE_HELPER, ...args], {
    encoding: 'utf8',
    env: hermesEnv(),
    windowsHide: true,
  });
  if (result.error || result.status !== 0) {
    const detail = result.error?.message || String(result.stderr || result.stdout || '').trim() || `exit ${result.status}`;
    throw new Error(`Hermes lifecycle helper failed: ${detail}`);
  }
  return result;
}

function parseLastJson(text, label) {
  const lines = String(text || '').split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  for (let i = lines.length - 1; i >= 0; i--) {
    try { return JSON.parse(lines[i]); } catch (_) { /* continue */ }
  }
  throw new Error(`${label} did not return JSON`);
}

function safeRelative(home, target) {
  const rel = path.relative(home, target);
  if (!rel || rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) {
    throw new Error(`unsafe Hermes destination: ${target}`);
  }
  return rel.split(path.sep).join('/');
}

function assertNoSymlinkComponents(home, target) {
  const rel = safeRelative(home, target);
  let cursor = home;
  for (const part of rel.split('/')) {
    cursor = path.join(cursor, part);
    let info;
    try { info = fs.lstatSync(cursor); }
    catch (error) { if (error.code === 'ENOENT') return; throw error; }
    if (info.isSymbolicLink()) throw new Error(`symlink destination refused: ${cursor}`);
  }
}

function walkRegularFiles(root) {
  const output = [];
  function visit(current) {
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      if (entry.name === '__pycache__' || entry.name === 'node_modules' || entry.name === '.DS_Store') continue;
      const full = path.join(current, entry.name);
      const info = fs.lstatSync(full);
      if (info.isSymbolicLink()) throw new Error(`symlink source refused: ${full}`);
      if (info.isDirectory()) visit(full);
      else if (info.isFile() && !entry.name.endsWith('.pyc')) output.push(full);
    }
  }
  visit(root);
  return output;
}

function addTree(entries, sourceRoot, destinationRoot, home) {
  if (!fs.existsSync(sourceRoot)) throw new Error(`Hermes payload missing: ${sourceRoot}`);
  for (const source of walkRegularFiles(sourceRoot)) {
    const destination = path.join(destinationRoot, path.relative(sourceRoot, source));
    entries.push({ source, destination, path: safeRelative(home, destination) });
  }
}

function payload(repoRoot) {
  const home = hermesHome();
  const plugin = pluginDir();
  const entries = [];
  for (const name of ['plugin.yaml', '__init__.py']) {
    const source = path.join(repoRoot, name);
    if (!fs.statSync(source).isFile()) throw new Error(`Hermes payload missing: ${source}`);
    const destination = path.join(plugin, name);
    entries.push({ source, destination, path: safeRelative(home, destination) });
  }
  addTree(entries, path.join(repoRoot, 'hermes_caveman'), path.join(plugin, 'hermes_caveman'), home);
  addTree(entries, path.join(repoRoot, 'src', 'tools'), path.join(plugin, 'src', 'tools'), home);
  addTree(entries, path.join(repoRoot, 'src', 'rules'), path.join(plugin, 'src', 'rules'), home);
  addTree(entries, path.join(repoRoot, 'src', 'mcp-servers', 'caveman-shrink'), path.join(plugin, 'src', 'mcp-servers', 'caveman-shrink'), home);
  for (const skill of SKILL_DIRS) {
    const source = path.join(repoRoot, 'skills', skill);
    addTree(entries, source, path.join(plugin, 'skills', skill), home);
    addTree(entries, source, path.join(home, 'skills', 'productivity', skill), home);
  }
  entries.sort((a, b) => a.path.localeCompare(b.path));
  const seen = new Set();
  for (const entry of entries) {
    if (seen.has(entry.path)) throw new Error(`duplicate Hermes destination: ${entry.path}`);
    seen.add(entry.path);
  }
  return entries;
}

function loadManifest() {
  const file = manifestPath();
  assertNoSymlinkComponents(hermesHome(), file);
  let info;
  try { info = fs.lstatSync(file); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe Hermes manifest: ${file}`);
  const value = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (value.version !== MANIFEST_VERSION || value.plugin !== PLUGIN || !Array.isArray(value.files)) {
    throw new Error(`unsupported Hermes ownership manifest: ${file}`);
  }
  const seen = new Set();
  for (const item of value.files) {
    if (
      !item ||
      typeof item.path !== 'string' ||
      typeof item.sha256 !== 'string' ||
      !allowedOwnedPath(item.path) ||
      !/^[0-9a-f]{64}$/.test(item.sha256) ||
      seen.has(item.path)
    ) {
      throw new Error(`invalid owned path or digest in Hermes manifest: ${item?.path || '<missing>'}`);
    }
    seen.add(item.path);
  }
  if (!Object.hasOwn(value, 'mcp') || !validMcpOwnership(value.mcp)) {
    throw new Error('invalid MCP ownership in Hermes manifest');
  }
  if (!Object.hasOwn(value, 'pluginEntry') || !validPluginEntryOwnership(value.pluginEntry)) {
    throw new Error('invalid plugin config ownership in Hermes manifest');
  }
  if (Object.hasOwn(value, 'configCleaned') && typeof value.configCleaned !== 'boolean') {
    throw new Error('invalid lifecycle phase in Hermes manifest');
  }
  return value;
}

function plainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function validMcpOwnership(value) {
  if (value === null) return true;
  if (!plainObject(value) || !/^[A-Za-z0-9_-]{1,128}$/.test(value.name || '')) return false;
  if (!plainObject(value.entry) || value.entry.command !== 'node' || !Array.isArray(value.entry.args)) return false;
  const proxy = path.join(pluginDir(), 'src', 'mcp-servers', 'caveman-shrink', 'index.js');
  if (value.entry.args.length < 2 || value.entry.args[0] !== proxy || !value.entry.args.every(item => typeof item === 'string')) {
    return false;
  }
  if (typeof value.previousPresent !== 'boolean') return false;
  return value.previousPresent ? plainObject(value.previous) : value.previous === null;
}

function validPluginEntryOwnership(value) {
  if (value === null) return true;
  if (!plainObject(value) || value.entry !== false || typeof value.previousPresent !== 'boolean') return false;
  return value.previousPresent ? typeof value.previous === 'boolean' : value.previous === null;
}

function existingRegularFiles(root) {
  if (!fs.existsSync(root)) return [];
  const info = fs.lstatSync(root);
  if (info.isSymbolicLink()) throw new Error(`symlink collision refused: ${root}`);
  if (!info.isDirectory()) throw new Error(`non-directory collision refused: ${root}`);
  return walkRegularFiles(root);
}

function collisionRoots(home) {
  return [
    path.join(home, 'plugins', PLUGIN),
    ...SKILL_DIRS.map(name => path.join(home, 'skills', 'productivity', name)),
  ];
}

function buildPlan(entries, manifest, force) {
  const home = hermesHome();
  const prior = new Map((manifest?.files || []).map(item => [item.path, item]));
  const currentPaths = new Set(entries.map(item => item.path));
  const backups = new Map();
  const actions = [];
  const stale = [];

  if (!manifest) {
    for (const root of collisionRoots(home)) {
      assertNoSymlinkComponents(home, root);
      if (!fs.existsSync(root)) continue;
      if (!force) throw new Error(`unowned Hermes collision: ${root} (use --force to back up and overwrite)`);
      for (const file of existingRegularFiles(root)) backups.set(file, safeRelative(home, file));
    }
  }

  for (const entry of entries) {
    assertNoSymlinkComponents(home, entry.destination);
    let info = null;
    try { info = fs.lstatSync(entry.destination); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    const old = prior.get(entry.path);
    if (!info) {
      actions.push({ ...entry, previousDigest: old?.sha256 || null, existed: false, write: true });
      continue;
    }
    if (info.isSymbolicLink()) throw new Error(`symlink collision refused: ${entry.destination}`);
    if (!info.isFile()) throw new Error(`non-file collision refused: ${entry.destination}`);
    const digest = shaFile(entry.destination);
    if (old && digest === old.sha256) {
      actions.push({ ...entry, previousDigest: digest, existed: true, write: true });
    } else if (force) {
      backups.set(entry.destination, entry.path);
      actions.push({ ...entry, previousDigest: digest, existed: true, write: true });
    } else if (old) {
      actions.push({ ...entry, previousDigest: old.sha256, existed: true, write: false, modified: true });
    } else {
      throw new Error(`unowned Hermes collision: ${entry.destination} (use --force to back up and overwrite)`);
    }
  }

  for (const old of manifest?.files || []) {
    if (currentPaths.has(old.path)) continue;
    const destination = path.join(home, ...old.path.split('/'));
    assertNoSymlinkComponents(home, destination);
    let info;
    try { info = fs.lstatSync(destination); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    if (info.isSymbolicLink() || !info.isFile()) {
      throw new Error(`non-file obsolete Hermes payload refused: ${destination}`);
    }
    stale.push({
      destination,
      path: old.path,
      sha256: old.sha256,
      modified: shaFile(destination) !== old.sha256,
    });
  }
  return { actions, backups: [...backups.entries()], stale };
}

function removeStaleFiles(stale, note) {
  const home = hermesHome();
  const stops = [home, path.join(home, 'plugins'), path.join(home, 'skills'), path.join(home, 'skills', 'productivity')];
  for (const item of stale) {
    if (item.modified) {
      note(`  preserved modified obsolete ${item.destination}`);
      continue;
    }
    assertNoSymlinkComponents(home, item.destination);
    let info;
    try { info = fs.lstatSync(item.destination); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    if (info.isSymbolicLink() || !info.isFile() || shaFile(item.destination) !== item.sha256) {
      note(`  preserved changed obsolete ${item.destination}`);
      continue;
    }
    fs.unlinkSync(item.destination);
    removeEmptyParents(item.destination, stops);
  }
}

function backupCollisions(backups) {
  if (!backups.length) return [];
  const home = hermesHome();
  const bucket = path.join(stateDir(), 'collision-backups', `${Date.now()}-${crypto.randomBytes(6).toString('hex')}`);
  const written = [];
  for (const [source, relative] of backups) {
    const info = fs.lstatSync(source);
    if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe collision backup source: ${source}`);
    const destination = path.join(bucket, ...relative.split('/'));
    assertNoSymlinkComponents(home, destination);
    fs.mkdirSync(path.dirname(destination), { recursive: true, mode: 0o700 });
    fs.copyFileSync(source, destination, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(destination, 0o600);
    if (shaFile(source) !== shaFile(destination)) throw new Error(`collision backup verification failed: ${source}`);
    written.push(safeRelative(home, destination));
  }
  return written;
}

function writeAtomicFromSource(source, destination) {
  const home = hermesHome();
  assertNoSymlinkComponents(home, destination);
  fs.mkdirSync(path.dirname(destination), { recursive: true, mode: 0o700 });
  assertNoSymlinkComponents(home, destination);
  const payload = fs.readFileSync(source);
  const mode = fs.statSync(source).mode & 0o777;
  const temporary = path.join(path.dirname(destination), `.${path.basename(destination)}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`);
  const descriptor = fs.openSync(temporary, 'wx', mode || 0o644);
  try { fs.writeFileSync(descriptor, payload); fs.fsyncSync(descriptor); }
  finally { fs.closeSync(descriptor); }
  fs.chmodSync(temporary, mode || 0o644);
  fs.renameSync(temporary, destination);
}

function writeManifest(value) {
  const file = manifestPath();
  const home = hermesHome();
  assertNoSymlinkComponents(home, file);
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const temporary = `${file}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(value, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
  fs.chmodSync(temporary, 0o600);
  fs.renameSync(temporary, file);
}

function removeEmptyParents(file, stops) {
  let cursor = path.dirname(file);
  const stopSet = new Set(stops.map(item => path.resolve(item)));
  while (cursor.startsWith(hermesHome()) && !stopSet.has(path.resolve(cursor))) {
    try { fs.rmdirSync(cursor); }
    catch (_) { break; }
    cursor = path.dirname(cursor);
  }
}

function removeGeneratedPythonCaches(root, stops) {
  let rootInfo;
  try { rootInfo = fs.lstatSync(root); }
  catch (error) { if (error.code === 'ENOENT') return; throw error; }
  if (rootInfo.isSymbolicLink() || !rootInfo.isDirectory()) return;

  function cleanCache(cache) {
    for (const entry of fs.readdirSync(cache, { withFileTypes: true })) {
      const target = path.join(cache, entry.name);
      const info = fs.lstatSync(target);
      if (info.isSymbolicLink()) continue;
      if (info.isFile() && /\.py[co]$/.test(entry.name)) fs.unlinkSync(target);
    }
    try {
      fs.rmdirSync(cache);
      removeEmptyParents(path.join(path.dirname(cache), '.caveman-cache-prune'), stops);
    } catch (error) {
      if (!['ENOTEMPTY', 'EEXIST'].includes(error.code)) throw error;
    }
  }

  function visit(current) {
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const target = path.join(current, entry.name);
      const info = fs.lstatSync(target);
      if (info.isSymbolicLink() || !info.isDirectory()) continue;
      if (entry.name === '__pycache__') cleanCache(target);
      else visit(target);
    }
  }

  visit(root);
}

function captureConfigSnapshot(root) {
  const result = runHermes(['config', 'path']);
  const lines = String(result.stdout || '').split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  const config = lines.at(-1);
  if (!config || !path.isAbsolute(config)) throw new Error('Hermes config path was not absolute');
  const snapshotHome = path.join(root, 'config-before');
  fs.mkdirSync(snapshotHome, { recursive: false, mode: 0o700 });
  let info;
  try { info = fs.lstatSync(config); }
  catch (error) { if (error.code === 'ENOENT') return snapshotHome; throw error; }
  if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe Hermes config source: ${config}`);
  const destination = path.join(snapshotHome, 'config.yaml');
  fs.copyFileSync(config, destination, fs.constants.COPYFILE_EXCL);
  fs.chmodSync(destination, 0o600);
  return snapshotHome;
}

function createLifecycleSnapshot() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-config-'));
  fs.chmodSync(root, 0o700);
  try { return { root, configHome: captureConfigSnapshot(root) }; }
  catch (error) { fs.rmSync(root, { recursive: true, force: true }); throw error; }
}

function createRollbackSnapshot(plan) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-hermes-rollback-'));
  fs.chmodSync(root, 0o700);
  const records = [];
  const seen = new Set();

  function capture(target, existed) {
    if (seen.has(target)) return;
    seen.add(target);
    let source = null;
    if (existed) {
      const info = fs.lstatSync(target);
      if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe rollback source: ${target}`);
      source = path.join(root, String(records.length));
      fs.copyFileSync(target, source, fs.constants.COPYFILE_EXCL);
      fs.chmodSync(source, info.mode & 0o777);
    }
    records.push({ target, existed, source });
  }

  try {
    for (const action of plan.actions) {
      if (action.write) capture(action.destination, action.existed);
    }
    for (const item of plan.stale) {
      if (!item.modified) capture(item.destination, true);
    }
    capture(manifestPath(), fs.existsSync(manifestPath()));
    return { root, records, configHome: captureConfigSnapshot(root) };
  } catch (error) {
    fs.rmSync(root, { recursive: true, force: true });
    throw error;
  }
}

function restoreFiles(snapshot) {
  for (const record of snapshot.records.slice().reverse()) {
    if (record.existed) {
      writeAtomicFromSource(record.source, record.target);
      continue;
    }
    let info;
    try { info = fs.lstatSync(record.target); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe rollback target: ${record.target}`);
    fs.unlinkSync(record.target);
    removeEmptyParents(record.target, [hermesHome()]);
  }
}

function cleanupRollbackSnapshot(snapshot) {
  fs.rmSync(snapshot.root, { recursive: true, force: true });
}

function lifecycleRemove(manifest, plugin = PLUGIN) {
  const args = ['caveman', '_lifecycle', 'remove', '--plugin', plugin];
  if (manifest?.mcp?.name && manifest.mcp.entry) {
    args.push('--mcp-name', manifest.mcp.name, '--expected-mcp', JSON.stringify(manifest.mcp.entry));
    if (manifest.mcp.previousPresent) {
      args.push('--restore-present', '--restore-mcp', JSON.stringify(manifest.mcp.previous));
    }
  }
  if (manifest?.pluginEntry) {
    args.push('--expected-plugin-override', JSON.stringify(manifest.pluginEntry.entry));
    if (manifest.pluginEntry.previousPresent) {
      args.push(
        '--restore-plugin-override-present',
        '--restore-plugin-override',
        JSON.stringify(manifest.pluginEntry.previous),
      );
    }
  }
  return runHermes(args);
}

function getMcp(name) {
  const result = runHermes(['caveman', '_lifecycle', 'get-mcp', '--name', name]);
  return parseLastJson(result.stdout, 'Hermes MCP lookup');
}

function setMcp(name, entry) {
  runHermes(['caveman', '_lifecycle', 'set-mcp', '--name', name, '--entry', JSON.stringify(entry)]);
}

function inspectPluginOverride(snapshotHome) {
  const result = runLifecycleHelper([
    'inspect-plugin-override', '--snapshot-home', snapshotHome,
  ]);
  const state = parseLastJson(result.stdout, 'Hermes plugin policy snapshot');
  if (!plainObject(state) || typeof state.present !== 'boolean' || (state.present && !Object.hasOwn(state, 'value'))) {
    throw new Error('Hermes plugin policy snapshot was invalid');
  }
  return { present: state.present, value: state.present ? state.value : null };
}

function setPluginOverride(state) {
  const args = ['caveman', '_lifecycle', 'set-plugin-override'];
  if (state.present) args.push('--present', '--value', JSON.stringify(state.value));
  runHermes(args);
}

function enablePlugin() {
  runHermes(['plugins', 'enable', PLUGIN, '--no-allow-tool-override']);
}

function getPluginStatus() {
  const result = runHermes(['plugins', 'list', '--json']);
  let rows;
  try { rows = JSON.parse(result.stdout); }
  catch (error) { throw new Error(`Hermes plugin status did not return JSON: ${error.message}`); }
  if (!Array.isArray(rows)) throw new Error('Hermes plugin status did not return a list');
  const row = rows.find(item => item && item.name === PLUGIN);
  const status = row?.status || 'not enabled';
  if (!['enabled', 'disabled', 'not enabled'].includes(status)) {
    throw new Error(`unknown Hermes plugin status: ${status}`);
  }
  return status;
}

function snapshotMcp(names) {
  return [...new Set(names.filter(Boolean))].map(name => {
    const entry = getMcp(name);
    return { name, present: entry !== null, entry };
  });
}

function restoreMcp(snapshot) {
  for (const item of snapshot) {
    const current = getMcp(item.name);
    if (item.present) {
      if (!deepEqual(current, item.entry)) setMcp(item.name, item.entry);
      continue;
    }
    if (current !== null) {
      lifecycleRemove({ mcp: { name: item.name, entry: current, previousPresent: false } }, '');
    }
  }
}

function restorePluginState(expected, override) {
  const current = getPluginStatus();
  if (current !== 'enabled') enablePlugin();
  setPluginOverride(override);
  if (expected === 'disabled') {
    runHermes(['plugins', 'disable', PLUGIN]);
  } else if (expected === 'not enabled') {
    lifecycleRemove(null);
  }
}

function installHermes(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ Hermes Agent detected');
  if (!repoRoot) {
    results.failed.push(['hermes', 'native install requires packaged plugin payload']);
    return;
  }
  try {
    assertHermesPlatform();
    const entries = payload(repoRoot);
    const oldManifest = loadManifest();
    const plan = buildPlan(entries, oldManifest, opts.force);
    if (opts.dryRun) {
      note(`  would install native plugin at ${pluginDir()}`);
      note(`  would copy ${entries.length} owned files and enable plugin ${PLUGIN}`);
      for (const action of plan.actions.filter(item => item.modified)) note(`  would preserve modified ${action.destination}`);
      for (const item of plan.stale) {
        note(`  would ${item.modified ? 'preserve modified' : 'remove unchanged'} obsolete ${item.destination}`);
      }
      results.installed.push('hermes');
      return;
    }

    let rollback = null;
    let priorPluginStatus = null;
    let priorPluginOverride = null;
    let mcpSnapshot = null;
    try {
      rollback = createRollbackSnapshot(plan);
      const collisionBackups = backupCollisions(plan.backups);
      for (const action of plan.actions) {
        if (action.modified) {
          note(`  preserved modified ${action.destination}`);
          continue;
        }
        writeAtomicFromSource(action.source, action.destination);
      }

      priorPluginStatus = getPluginStatus();
      priorPluginOverride = inspectPluginOverride(rollback.configHome);
      enablePlugin();
      const oldPluginEntry = oldManifest?.pluginEntry;
      const stillOwnsPluginEntry = !!(
        oldPluginEntry
        && priorPluginOverride.present
        && deepEqual(priorPluginOverride.value, oldPluginEntry.entry)
      );
      let pluginEntry = null;
      if (stillOwnsPluginEntry) {
        pluginEntry = oldPluginEntry;
      } else if (oldManifest || priorPluginOverride.present) {
        setPluginOverride(priorPluginOverride);
      } else {
        pluginEntry = { entry: false, previousPresent: false, previous: null };
      }
      const oldMcp = oldManifest?.mcp;
      mcpSnapshot = snapshotMcp([oldMcp?.name, opts.withMcpShrink ? opts.mcpShrinkName : null]);
      let mcp = null;
      if (opts.withMcpShrink) {
        const name = opts.mcpShrinkName;
        const proxy = path.join(pluginDir(), 'src', 'mcp-servers', 'caveman-shrink', 'index.js');
        const desired = { command: 'node', args: [proxy, ...opts.withMcpShrink] };
        const current = getMcp(name);
        let previous = null;
        let previousPresent = false;
        const ownsCurrent = !!(oldMcp && oldMcp.name === name && deepEqual(current, oldMcp.entry));
        if (ownsCurrent) {
          previous = oldMcp.previous;
          previousPresent = !!oldMcp.previousPresent;
        } else if (current !== null && !deepEqual(current, desired)) {
          if (!opts.force) throw new Error(`Hermes MCP '${name}' already exists; use --force or --mcp-shrink-name`);
          previous = current;
          previousPresent = true;
        }
        if (oldMcp && oldMcp.name !== name) {
          lifecycleRemove({ mcp: oldMcp }, '');
        }
        if (ownsCurrent || current === null || !deepEqual(current, desired)) {
          setMcp(name, desired);
          mcp = { name, entry: desired, previous, previousPresent };
        }
      } else if (opts.withMcpShrink === null && oldMcp) {
        const current = getMcp(oldMcp.name);
        if (!deepEqual(current, oldMcp.entry)) {
          throw new Error(`managed Hermes MCP '${oldMcp.name}' changed; refusing reinstall`);
        }
        mcp = oldMcp;
      } else if (oldManifest?.mcp) {
        lifecycleRemove({ mcp: oldManifest.mcp }, '');
      }

      removeStaleFiles(plan.stale, note);
      const files = plan.actions.map(action => ({
        path: action.path,
        sha256: action.modified ? action.previousDigest : shaFile(action.destination),
      }));
      writeManifest({
        version: MANIFEST_VERSION,
        plugin: PLUGIN,
        files,
        mcp,
        pluginEntry,
        configCleaned: false,
        collisionBackups,
      });
      cleanupRollbackSnapshot(rollback);
      rollback = null;
      results.installed.push('hermes');
    } catch (error) {
      const recoveryErrors = [];
      if (priorPluginStatus !== null && priorPluginOverride === null && rollback !== null) {
        try { priorPluginOverride = inspectPluginOverride(rollback.configHome); }
        catch (rollbackError) { recoveryErrors.push(`plugin snapshot: ${rollbackError.message}`); }
      }
      if (mcpSnapshot !== null) {
        try { restoreMcp(mcpSnapshot); }
        catch (rollbackError) { recoveryErrors.push(`MCP: ${rollbackError.message}`); }
      }
      if (priorPluginStatus !== null && priorPluginOverride !== null) {
        try { restorePluginState(priorPluginStatus, priorPluginOverride); }
        catch (rollbackError) { recoveryErrors.push(`plugin: ${rollbackError.message}`); }
      }
      if (rollback !== null) {
        try { restoreFiles(rollback); }
        catch (rollbackError) { recoveryErrors.push(`files: ${rollbackError.message}`); }
      }
      if (rollback !== null && recoveryErrors.length === 0) cleanupRollbackSnapshot(rollback);
      if (recoveryErrors.length > 0) {
        const recovery = rollback ? `; recovery snapshot preserved at ${rollback.root}` : '';
        throw new Error(`${error.message}; rollback incomplete (${recoveryErrors.join('; ')})${recovery}`);
      }
      throw error;
    }
  } catch (error) {
    warn(`  Hermes install failed: ${error.message}`);
    results.failed.push(['hermes', error.message]);
  }
}

function disableHermes(ctx) {
  const { note, opts } = ctx;
  if (opts.dryRun) { note(`  would disable Hermes plugin ${PLUGIN}`); return 0; }
  try {
    assertHermesPlatform();
    runHermes(['plugins', 'disable', PLUGIN]);
    note(`  disabled Hermes plugin ${PLUGIN}; files and state preserved`);
    return 0;
  } catch (error) {
    ctx.warn(`  Hermes disable failed: ${error.message}`);
    return 1;
  }
}

function allowedOwnedPath(relative) {
  if (typeof relative !== 'string' || relative.includes('\\')) return false;
  const parts = relative.split('/');
  if (parts.some(part => !part || part === '.' || part === '..')) return false;
  if (parts.length > 2 && parts[0] === 'plugins' && parts[1] === PLUGIN) return true;
  return (
    parts.length > 3 &&
    parts[0] === 'skills' &&
    parts[1] === 'productivity' &&
    SKILL_DIRS.includes(parts[2])
  );
}

function scanNoSymlinks(root) {
  const info = fs.lstatSync(root);
  if (info.isSymbolicLink()) throw new Error(`symlink state path refused: ${root}`);
  if (!info.isDirectory()) throw new Error(`non-directory state path refused: ${root}`);
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const full = path.join(root, entry.name);
    const child = fs.lstatSync(full);
    if (child.isSymbolicLink()) throw new Error(`symlink state child refused: ${full}`);
    if (child.isDirectory()) scanNoSymlinks(full);
    else if (!child.isFile()) throw new Error(`special state file refused: ${full}`);
  }
}

function removeRegular(file) {
  let info;
  try { info = fs.lstatSync(file); }
  catch (error) { if (error.code === 'ENOENT') return; throw error; }
  if (info.isSymbolicLink() || !info.isFile()) throw new Error(`non-regular state path refused: ${file}`);
  fs.unlinkSync(file);
}

function removeEphemeral(opts, note) {
  const args = ['cleanup-state', '--hermes-home', hermesHome()];
  if (opts.purgeHistory) args.push('--purge-history');
  const outcome = parseLastJson(runLifecycleHelper(args).stdout, 'Hermes state cleanup');
  const history = path.join(stateDir(), 'history.jsonl');
  if (outcome.history === 'purged') note(`  purged lifetime history ${history}`);
  else if (outcome.history === 'preserved') note(`  preserved lifetime history ${history}`);
}

function preflightCacheCleanup(root) {
  let info;
  try { info = fs.lstatSync(root); }
  catch (error) { if (error.code === 'ENOENT') return; throw error; }
  if (info.isSymbolicLink() || !info.isDirectory()) return;
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const target = path.join(root, entry.name);
    const child = fs.lstatSync(target);
    if (child.isDirectory() && !child.isSymbolicLink()) preflightCacheCleanup(target);
  }
}

function preflightUninstall(manifest, opts) {
  const home = hermesHome();
  assertNoSymlinkComponents(home, manifestPath());
  for (const item of manifest.files) {
    const target = path.join(home, ...item.path.split('/'));
    assertNoSymlinkComponents(home, target);
    let info;
    try { info = fs.lstatSync(target); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    if (info.isSymbolicLink() || !info.isFile()) throw new Error(`unsafe owned path: ${target}`);
    shaFile(target);
  }
  for (const root of collisionRoots(home)) preflightCacheCleanup(root);
  for (const name of ['mode.json', '.history.lock']) {
    const target = path.join(stateDir(), name);
    let info;
    try { info = fs.lstatSync(target); }
    catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    if (info.isSymbolicLink() || !info.isFile()) throw new Error(`non-regular state path refused: ${target}`);
  }
  for (const name of ['sessions', 'revisions', 'locks']) {
    const target = path.join(stateDir(), name);
    if (fs.existsSync(target)) scanNoSymlinks(target);
  }
  const history = path.join(stateDir(), 'history.jsonl');
  let historyInfo;
  try { historyInfo = fs.lstatSync(history); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  if (historyInfo && (historyInfo.isSymbolicLink() || !historyInfo.isFile())) {
    throw new Error(`non-regular history path refused: ${history}`);
  }
  if (opts.purgeHistory && historyInfo) fs.accessSync(history, fs.constants.W_OK);
}

function uninstallHermes(ctx) {
  const { note, warn, opts } = ctx;
  let manifest;
  try { manifest = loadManifest(); }
  catch (error) { warn(`  Hermes uninstall refused: ${error.message}`); return 1; }
  if (!manifest) {
    note('  Hermes ownership manifest not found — no plugin files or config changed');
    return 0;
  }
  if (opts.dryRun) {
    note(`  would remove unchanged owned Hermes files from ${hermesHome()}`);
    note(`  would preserve lifetime history${opts.purgeHistory ? ' (purge requested)' : ''}`);
    return 0;
  }

  try {
    assertHermesPlatform();
    preflightUninstall(manifest, opts);
  }
  catch (error) {
    warn(`  Hermes uninstall preflight refused: ${error.message}`);
    warn('  Hermes config, files, and manifest preserved for recovery');
    return 1;
  }

  if (!manifest.configCleaned) {
    let snapshot = null;
    let priorStatus = null;
    let priorOverride = null;
    let priorMcp = null;
    try {
      snapshot = createLifecycleSnapshot();
      priorStatus = getPluginStatus();
      priorOverride = inspectPluginOverride(snapshot.configHome);
      if (priorStatus !== 'enabled') enablePlugin();
      priorMcp = manifest.mcp ? snapshotMcp([manifest.mcp.name]) : [];
      setPluginOverride(priorOverride);
      const result = lifecycleRemove(manifest);
      const outcome = parseLastJson(result.stdout, 'Hermes lifecycle removal');
      if (manifest.mcp && !outcome.mcp_changed) note(`  managed MCP entry already absent: ${manifest.mcp.name}`);
      const marked = { ...manifest, configCleaned: true };
      try {
        writeManifest(marked);
      } catch (markerError) {
        if (getPluginStatus() !== 'enabled') enablePlugin();
        restoreMcp(priorMcp);
        restorePluginState(priorStatus, priorOverride);
        throw new Error(`could not persist cleanup phase: ${markerError.message}`);
      }
      manifest = marked;
    } catch (error) {
      if (priorStatus !== null && priorOverride !== null) {
        try { restorePluginState(priorStatus, priorOverride); }
        catch (rollbackError) { warn(`  plugin config rollback failed: ${rollbackError.message}`); }
      }
      warn(`  exact Hermes config cleanup failed: ${error.message}`);
      warn('  Hermes files and manifest preserved for recovery');
      if (snapshot) cleanupRollbackSnapshot(snapshot);
      return 1;
    }
    cleanupRollbackSnapshot(snapshot);
  }

  let failed = false;
  const home = hermesHome();
  const pruneStops = [home, path.join(home, 'plugins'), path.join(home, 'skills'), path.join(home, 'skills', 'productivity')];
  for (const item of manifest.files.slice().reverse()) {
    if (!item || typeof item.path !== 'string' || typeof item.sha256 !== 'string' || !allowedOwnedPath(item.path)) {
      warn(`  invalid owned path preserved: ${item?.path || '<missing>'}`);
      failed = true;
      continue;
    }
    const target = path.join(home, ...item.path.split('/'));
    try {
      assertNoSymlinkComponents(home, target);
      const info = fs.lstatSync(target);
      if (!info.isFile() || shaFile(target) !== item.sha256) {
        note(`  preserved modified ${target}`);
        continue;
      }
      fs.unlinkSync(target);
      removeEmptyParents(target, pruneStops);
    } catch (error) {
      if (error.code !== 'ENOENT') {
        warn(`  preserved unsafe/unreadable ${target}: ${error.message}`);
        failed = true;
      }
    }
  }

  for (const root of collisionRoots(home)) {
    try { removeGeneratedPythonCaches(root, pruneStops); }
    catch (error) {
      warn(`  generated Python cache cleanup refused at ${root}: ${error.message}`);
      failed = true;
    }
  }

  try { removeEphemeral(opts, note); }
  catch (error) { warn(`  Hermes state cleanup refused: ${error.message}`); failed = true; }
  if (failed) {
    warn('  Hermes ownership manifest preserved for recovery');
    return 1;
  }
  try { removeRegular(manifestPath()); }
  catch (error) { warn(`  manifest cleanup failed: ${error.message}`); return 1; }
  return 0;
}

module.exports = {
  SKILL_DIRS,
  assertHermesPlatform,
  disableHermes,
  hermesHome,
  installHermes,
  manifestPath,
  pluginDir,
  stateDir,
  uninstallHermes,
};
