import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const PLUGIN_MANIFEST = path.join(REPO_ROOT, '.claude-plugin', 'plugin.json');

function hookCommands(manifest, eventName) {
  return (manifest.hooks?.[eventName] || [])
    .flatMap(entry => (Array.isArray(entry?.hooks) ? entry.hooks : []))
    .map(hook => hook?.command || '')
    .filter(Boolean);
}

test('plugin manifest wires SessionEnd stats recorder', () => {
  const manifest = JSON.parse(fs.readFileSync(PLUGIN_MANIFEST, 'utf8'));
  const commands = hookCommands(manifest, 'SessionEnd');
  assert.equal(commands.length, 1);
  assert.match(commands[0], /caveman-stats\.js" --record$/);
});

test('marketplace source ships a native Codex manifest without duplicate hooks', () => {
  const marketplace = JSON.parse(fs.readFileSync(
    path.join(REPO_ROOT, '.claude-plugin', 'marketplace.json'), 'utf8'));
  const plugin = marketplace.plugins.find(entry => entry.name === 'caveman');
  assert.ok(plugin, 'marketplace must declare caveman');
  const pluginRoot = path.resolve(REPO_ROOT, plugin.source);
  // Codex reads both the marketplace checkout and the installed plugin root.
  // Without this file it falls back to Claude's incompatible inline hook map.
  const manifest = JSON.parse(fs.readFileSync(
    path.join(pluginRoot, '.codex-plugin', 'plugin.json'), 'utf8'));
  assert.equal(manifest.name, plugin.name);
  assert.equal(manifest.skills, './skills/');
  assert.ok(fs.statSync(path.join(pluginRoot, manifest.skills, 'caveman', 'SKILL.md')).isFile());
  assert.equal(manifest.hooks, undefined, 'existing user-level hooks own activation');
});

test('npm package includes the root Codex manifest', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'package.json'), 'utf8'));
  assert.ok(pkg.files.includes('.codex-plugin/'), 'npm must ship the native Codex manifest');
});
