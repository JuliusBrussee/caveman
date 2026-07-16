#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const args = process.argv.slice(2);
const defaultHome = process.env.HERMES_HOME || path.join(os.homedir(), '.hermes');
let home = defaultHome;
if (path.basename(path.dirname(defaultHome)) !== 'profiles') {
  try {
    const active = fs.readFileSync(path.join(defaultHome, 'active_profile'), 'utf8').trim();
    if (active && active !== 'default') home = path.join(defaultHome, 'profiles', active);
  } catch {}
}
const configPath = path.join(home, 'config.yaml');
const logPath = process.env.FAKE_HERMES_LOG;
if (logPath) fs.appendFileSync(logPath, JSON.stringify(args) + '\n');

function readConfig() {
  try { return JSON.parse(fs.readFileSync(configPath, 'utf8')); }
  catch { return {}; }
}
function writeConfig(config) {
  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  const tmp = `${configPath}.${process.pid}.${Date.now()}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(config, null, 2) + '\n', { mode: 0o600 });
  fs.renameSync(tmp, configPath);
  try { fs.chmodSync(configPath, 0o600); } catch {}
}
function atPath(config, dotted, create = false) {
  const parts = dotted.split('.').filter(Boolean);
  const leaf = parts.pop();
  let cursor = config;
  for (const part of parts) {
    if (!cursor[part] || typeof cursor[part] !== 'object' || Array.isArray(cursor[part])) {
      if (!create) return [null, leaf];
      cursor[part] = {};
    }
    cursor = cursor[part];
  }
  return [cursor, leaf];
}

const [group, action, ...rest] = args;
const config = readConfig();
if (group === 'plugins' && (action === 'enable' || action === 'disable')) {
  const name = rest[0];
  if (!name) process.exit(2);
  config.plugins ??= {};
  const enabled = new Set(Array.isArray(config.plugins.enabled) ? config.plugins.enabled : []);
  const disabled = new Set(Array.isArray(config.plugins.disabled) ? config.plugins.disabled : []);
  if (action === 'enable') {
    enabled.add(name);
    const partialMarker = path.join(home, '.fake-partial-enable-fired');
    if (process.env.FAKE_HERMES_PARTIAL_ENABLE_ONCE === '1' && !fs.existsSync(partialMarker)) {
      config.plugins.enabled = [...enabled].sort();
      config.plugins.disabled = [...disabled].sort();
      writeConfig(config);
      fs.writeFileSync(partialMarker, '1\n');
      process.exit(9);
    }
    disabled.delete(name);
    config.plugins.entries ??= {};
    config.plugins.entries[name] ??= {};
    config.plugins.entries[name].allow_tool_override = false;
  }
  else { enabled.delete(name); disabled.add(name); }
  config.plugins.enabled = [...enabled].sort();
  config.plugins.disabled = [...disabled].sort();
  writeConfig(config);
  process.exit(0);
}
if (group === 'config' && action === 'set') {
  const [key, raw] = rest;
  if (!key || raw === undefined) process.exit(2);
  const [parent, leaf] = atPath(config, key, true);
  try { parent[leaf] = JSON.parse(raw); }
  catch { parent[leaf] = raw; }
  writeConfig(config);
  process.exit(0);
}
if (group === 'config' && action === 'unset') {
  const [key] = rest;
  if (!key) process.exit(2);
  const [parent, leaf] = atPath(config, key, false);
  if (parent) delete parent[leaf];
  writeConfig(config);
  process.exit(0);
}
if (group === 'caveman' && action === '_lifecycle') {
  const operation = rest.shift();
  const valueFor = flag => {
    const index = rest.indexOf(flag);
    return index === -1 ? null : rest[index + 1];
  };
  if (operation === 'get-mcp') {
    const name = valueFor('--name');
    process.stdout.write(JSON.stringify(config.mcp_servers?.[name] ?? null) + '\n');
    process.exit(0);
  }
  if (operation === 'inspect-plugin-override') {
    const snapshotHome = valueFor('--snapshot-home');
    let snapshot = {};
    try { snapshot = JSON.parse(fs.readFileSync(path.join(snapshotHome, 'config.yaml'), 'utf8')); } catch {}
    const entry = snapshot.plugins?.entries?.caveman;
    const present = !!entry && Object.hasOwn(entry, 'allow_tool_override');
    process.stdout.write(JSON.stringify({ present, value: present ? entry.allow_tool_override : null }) + '\n');
    process.exit(0);
  }
  if (operation === 'set-plugin-override') {
    const present = rest.includes('--present');
    const raw = valueFor('--value');
    config.plugins ??= {};
    config.plugins.entries ??= {};
    config.plugins.entries.caveman ??= {};
    if (present) config.plugins.entries.caveman.allow_tool_override = JSON.parse(raw);
    else delete config.plugins.entries.caveman.allow_tool_override;
    if (Object.keys(config.plugins.entries.caveman).length === 0) delete config.plugins.entries.caveman;
    if (Object.keys(config.plugins.entries).length === 0) delete config.plugins.entries;
    writeConfig(config);
    process.exit(0);
  }
  if (operation === 'set-mcp') {
    if (process.env.FAKE_HERMES_FAIL_SET_MCP === '1') process.exit(9);
    const name = valueFor('--name');
    const raw = valueFor('--entry');
    if (!name || !raw) process.exit(2);
    let entry;
    try { entry = JSON.parse(raw); } catch { process.exit(2); }
    config.mcp_servers ??= {};
    config.mcp_servers[name] = entry;
    writeConfig(config);
    process.exit(0);
  }
  if (operation === 'remove') {
    if (process.env.FAKE_HERMES_FAIL_REMOVE === '1') process.exit(9);
    const plugin = valueFor('--plugin');
    const mcpName = valueFor('--mcp-name');
    const expectedRaw = valueFor('--expected-mcp');
    const restoreRaw = valueFor('--restore-mcp');
    const restorePresent = rest.includes('--restore-present');
    const expectedOverrideRaw = valueFor('--expected-plugin-override');
    const restoreOverrideRaw = valueFor('--restore-plugin-override');
    const restoreOverridePresent = rest.includes('--restore-plugin-override-present');
    let expected;
    if (mcpName && expectedRaw && config.mcp_servers && Object.hasOwn(config.mcp_servers, mcpName)) {
      try { expected = JSON.parse(expectedRaw); } catch { process.exit(2); }
      if (JSON.stringify(config.mcp_servers[mcpName]) !== JSON.stringify(expected)) {
        process.stderr.write('managed MCP entry was modified\n');
        process.exit(1);
      }
    }
    if (plugin && config.plugins) {
      for (const key of ['enabled', 'disabled']) {
        if (Array.isArray(config.plugins[key])) config.plugins[key] = config.plugins[key].filter(value => value !== plugin);
      }
    }
    let changed = false;
    if (mcpName && expectedRaw && config.mcp_servers && Object.hasOwn(config.mcp_servers, mcpName)) {
      if (restorePresent) {
        try { config.mcp_servers[mcpName] = JSON.parse(restoreRaw); }
        catch { process.exit(2); }
      } else {
        delete config.mcp_servers[mcpName];
      }
      changed = true;
      if (Object.keys(config.mcp_servers).length === 0) delete config.mcp_servers;
    }
    if (expectedOverrideRaw !== null) {
      const entry = config.plugins?.entries?.caveman;
      if (entry && Object.hasOwn(entry, 'allow_tool_override')
          && JSON.stringify(entry.allow_tool_override) === JSON.stringify(JSON.parse(expectedOverrideRaw))) {
        if (restoreOverridePresent) entry.allow_tool_override = JSON.parse(restoreOverrideRaw);
        else {
          delete entry.allow_tool_override;
          if (Object.keys(entry).length === 0) delete config.plugins.entries.caveman;
          if (Object.keys(config.plugins.entries).length === 0) delete config.plugins.entries;
        }
      }
    }
    writeConfig(config);
    process.stdout.write(JSON.stringify({ mcp_changed: changed }) + '\n');
    process.exit(0);
  }
  process.exit(2);
}
if (group === 'plugins' && action === 'list') {
  if (rest.includes('--json')) {
    const enabled = Array.isArray(config.plugins?.enabled) && config.plugins.enabled.includes('caveman');
    const disabled = Array.isArray(config.plugins?.disabled) && config.plugins.disabled.includes('caveman');
    process.stdout.write(JSON.stringify([{
      name: 'caveman',
      status: enabled ? 'enabled' : disabled ? 'disabled' : 'not enabled',
      source: 'user',
    }]) + '\n');
    process.exit(0);
  }
  process.stdout.write(JSON.stringify(config.plugins || {}) + '\n');
  process.exit(0);
}
if (group === 'config' && action === 'path') {
  process.stdout.write(configPath + '\n');
  process.exit(0);
}
process.stderr.write(`fake-hermes: unsupported ${args.join(' ')}\n`);
process.exit(2);
