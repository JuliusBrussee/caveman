import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { readFileSync, mkdtempSync, rmSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
const root = fileURLToPath(new URL('../../', import.meta.url));
const require = createRequire(import.meta.url);
const { parseModeChange } = require('../../src/hooks/caveman-parse.js');
const options = { getDefaultMode: () => 'caveman' };
for (const [prompt, expected] of [
  ['/sweet', { action: 'set', mode: 'caveman' }],
  ['/sweet lite', { action: 'set', mode: 'caveman' }],
  ['/sweet full', { action: 'set', mode: 'caveman' }],
  ['/sweet ultra', { action: 'set', mode: 'ultracave' }],
  ['/sweet:sweet off', { action: 'clear' }],
  ['/sweet:sweet-review', { action: 'set', mode: 'review' }],
  ['/sweet status', { action: 'status' }],
  ['짧게 말해', { action: 'set', mode: 'caveman' }],
  ['스윗 끄기', { action: 'clear' }],
  ['그만', { action: 'clear' }],
  ['버튼 문구를 짧게 말해로 바꿔줘', null],
  ['API 응답값을 반말로 해', null],
  ['"스윗 끄기" 문구를 화면에 추가해', null],
  ['일반 모드 버튼을 만들어줘', null],
  ['/another sweet off', null],
]) {
  test(`Sweet parser: ${prompt}`, () => assert.deepEqual(parseModeChange(prompt, options), expected));
}

test('Sweet expanded OpenCode command preserves its argument', () => {
  assert.deepEqual(parseModeChange('Activate sweet mode: ultra\n\nIf no argument given, use caveman.', { ...options, expandedTpl: true }), { action: 'set', mode: 'ultracave' });
});

test('Claude command envelope activates the Sweet alias with Korean context', t => {
  const home = mkdtempSync(join(tmpdir(), 'sweet-envelope-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const env = { ...process.env, HOME: home, USERPROFILE: home, CLAUDE_CONFIG_DIR: home, XDG_CONFIG_HOME: home, CAVEMAN_DEFAULT_MODE: 'caveman', CLAUDE_PLUGIN_ROOT: root };
  const run = prompt => spawnSync(process.execPath, [join(root, 'src/hooks/caveman-mode-tracker.js')], { input: JSON.stringify({ prompt, cwd: home, session_id: 'sweet-test' }), encoding: 'utf8', env, cwd: home, timeout: 10000 });
  const on = run('<command-name>/sweet</command-name><command-args>ultra</command-args>');
  assert.equal(on.status, 0, on.stderr);
  assert.match(on.stdout, /한국어 반말/);
  const status = run('<command-name>/sweet</command-name><command-args>status</command-args>');
  assert.equal(status.status, 0, status.stderr);
  assert.match(status.stdout, /Caveman mode: ultracave/);
  const off = run('<command-name>/sweet</command-name><command-args>off</command-args>');
  assert.equal(off.status, 0, off.stderr);
  const after = run('/sweet status');
  assert.match(after.stdout, /Caveman mode: off/);
});

test('all prose modes and degraded rules retain the Korean persona and artifact boundary', () => {
  const config = require('../../src/hooks/caveman-config.js');
  for (const mode of ['caveman', 'ultracave', 'megacave']) {
    const body = config.loadRuleset(mode, join(root, 'src/hooks'));
    assert.match(body, /한국어/);
    assert.match(body, /오빠/);
    assert.match(body, /프로젝트의 언어 관례/);
    assert.doesNotMatch(body, /以文言答|Prose in 文言|Caveman in Classical Chinese/);
    assert.match(config.fallbackRuleset(mode), /한국어 반말/);
  }
});

test('Sweet compression scripts match maintained canonical scripts', () => {
  for (const name of ['compress.py', 'detect.py', 'validate.py', 'cli.py']) {
    assert.equal(readFileSync(join(root, 'skills/sweet-compress/scripts', name), 'utf8'), readFileSync(join(root, 'skills/caveman-compress/scripts', name), 'utf8'));
  }
});
