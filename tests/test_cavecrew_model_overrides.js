#!/usr/bin/env node
// Tests for src/hooks/cavecrew-model-overrides.js
// Run: node tests/test_cavecrew_model_overrides.js

'use strict';

const fs   = require('fs');
const path = require('path');
const os   = require('os');
const assert = require('assert');

const { patchFrontmatterModel, resolvePluginRoot, applyOverrides, AGENT_ENV_MAP } =
  require('../src/hooks/cavecrew-model-overrides');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log('  ✓ ' + name);
  } catch (e) {
    failed++;
    console.error('  ✗ ' + name);
    console.error('    ' + e.message);
  }
}

// ── patchFrontmatterModel ──────────────────────────────────────────────────

console.log('\npatchFrontmatterModel\n');

const REVIEWER_FM = [
  '---',
  'name: cavecrew-reviewer',
  'description: >',
  '  Reviewer subagent.',
  'tools: [Read, Grep, Bash]',
  'model: haiku',
  '---',
  '',
  'Body text.',
].join('\n');

test('replaces existing model: haiku with sonnet in reviewer', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, 'sonnet');
  assert.ok(out.includes('model: sonnet'), 'new model line missing');
  assert.ok(!out.includes('model: haiku'), 'old model line still present');
  assert.ok(out.includes('Body text.'), 'body missing');
});

test('preserves all other frontmatter lines', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, 'opus');
  assert.ok(out.includes('name: cavecrew-reviewer'), 'name line lost');
  assert.ok(out.includes('tools: [Read, Grep, Bash]'), 'tools line lost');
  assert.ok(out.includes('description: >'), 'description block lost');
});

const INVESTIGATOR_FM = [
  '---',
  'name: cavecrew-investigator',
  'tools: [Read, Grep, Glob, Bash]',
  'model: haiku',
  '---',
  '',
  'Investigator body.',
].join('\n');

test('replaces existing model: haiku with opus in investigator', () => {
  const out = patchFrontmatterModel(INVESTIGATOR_FM, 'opus');
  assert.ok(out.includes('model: opus'), 'new model missing');
  assert.ok(!out.includes('model: haiku'), 'old model still present');
  assert.ok(out.includes('Investigator body.'), 'body lost');
});

const BUILDER_FM = [
  '---',
  'name: cavecrew-builder',
  'description: >',
  '  Builder subagent.',
  'tools: [Read, Edit, Write, Grep, Glob]',
  '---',
  '',
  'Builder body.',
].join('\n');

test('inserts model: after tools: when no model line exists (builder)', () => {
  const out = patchFrontmatterModel(BUILDER_FM, 'sonnet');
  assert.ok(out.includes('model: sonnet'), 'model line not inserted');
  // Must be inside frontmatter (before body)
  const fmClose = out.indexOf('\n---', 3);
  const modelPos = out.indexOf('model: sonnet');
  assert.ok(modelPos < fmClose, 'model line is outside frontmatter');
  // Inserted right after tools: line
  const toolsPos = out.indexOf('tools:');
  const toolsEnd = out.indexOf('\n', toolsPos);
  assert.strictEqual(out.slice(toolsEnd + 1, toolsEnd + 1 + 'model: sonnet'.length), 'model: sonnet',
    'model not inserted immediately after tools: line');
  assert.ok(out.includes('Builder body.'), 'body lost');
});

test('no-op when content has no frontmatter', () => {
  const plain = 'Just some text\nno frontmatter\n';
  const out = patchFrontmatterModel(plain, 'sonnet');
  assert.strictEqual(out, plain);
});

test('empty model value is no-op (defense-in-depth guard)', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, '');
  assert.strictEqual(out, REVIEWER_FM, 'empty value should leave file unchanged');
});

test('ignores model value with newline', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, 'so\nnnet');
  assert.strictEqual(out, REVIEWER_FM, 'should return original unchanged');
});

test('ignores model value with control character', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, 'so\x01nnet');
  assert.strictEqual(out, REVIEWER_FM, 'should return original unchanged');
});

test('model line already identical → content unchanged', () => {
  const out = patchFrontmatterModel(REVIEWER_FM, 'haiku');
  assert.strictEqual(out, REVIEWER_FM, 'should be byte-identical when value unchanged');
});

test('no model line and no tools line → inserts before closing ---', () => {
  const fm = '---\nname: test\n---\n\nbody\n';
  const out = patchFrontmatterModel(fm, 'sonnet');
  assert.ok(out.includes('model: sonnet'), 'model line missing');
  const fmClose = out.indexOf('\n---', 3);
  const modelPos = out.indexOf('model: sonnet');
  assert.ok(modelPos < fmClose, 'model line outside frontmatter');
});

test('CRLF files: inserted model line uses CRLF, no mixed endings', () => {
  const crlf = REVIEWER_FM.replace(/\n/g, '\r\n');
  const out = patchFrontmatterModel(crlf, 'sonnet');
  assert.ok(out.includes('model: sonnet'), 'model line missing in CRLF file');
  // No bare LF should appear outside CRLF sequences
  const strippedCR = out.replace(/\r\n/g, '');
  assert.ok(!strippedCR.includes('\n'), 'mixed line endings detected after patch');
});

test('CRLF builder (no model line): inserted model line uses CRLF', () => {
  const crlf = BUILDER_FM.replace(/\n/g, '\r\n');
  const out = patchFrontmatterModel(crlf, 'sonnet');
  assert.ok(out.includes('model: sonnet'), 'model line missing in CRLF builder file');
  const strippedCR = out.replace(/\r\n/g, '');
  assert.ok(!strippedCR.includes('\n'), 'mixed line endings in CRLF builder patch');
});

// ── resolvePluginRoot ──────────────────────────────────────────────────────

console.log('\nresolvePluginRoot\n');

test('resolves to parent of hooks dir', () => {
  const hooksDir = path.join(os.tmpdir(), 'fake-plugin', 'hooks');
  const root = resolvePluginRoot(hooksDir);
  assert.strictEqual(path.basename(root), 'fake-plugin');
});

// ── applyOverrides ─────────────────────────────────────────────────────────

console.log('\napplyOverrides\n');

function withTmpPlugin(fn) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-override-test-'));
  const agentsDir = path.join(tmp, 'agents');
  fs.mkdirSync(agentsDir);
  try {
    fn(tmp, agentsDir);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

test('replaces reviewer model when CAVECREW_REVIEWER_MODEL set', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: 'sonnet' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.ok(out.includes('model: sonnet'), 'reviewer model not patched');
  });
});

test('replaces investigator model when CAVECREW_INVESTIGATOR_MODEL set', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-investigator.md'), INVESTIGATOR_FM, 'utf8');
    applyOverrides(root, { CAVECREW_INVESTIGATOR_MODEL: 'opus' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-investigator.md'), 'utf8');
    assert.ok(out.includes('model: opus'), 'investigator model not patched');
  });
});

test('inserts builder model when CAVECREW_BUILDER_MODEL set and no model line', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-builder.md'), BUILDER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_BUILDER_MODEL: 'sonnet' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-builder.md'), 'utf8');
    assert.ok(out.includes('model: sonnet'), 'builder model not inserted');
  });
});

test('blank env var is no-op', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: '' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.strictEqual(out, REVIEWER_FM, 'blank env var should be no-op');
  });
});

test('whitespace-only env var is no-op', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: '   ' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.strictEqual(out, REVIEWER_FM, 'whitespace env var should be no-op');
  });
});

test('env var with newline in value is ignored', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: 'so\nnnet' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.strictEqual(out, REVIEWER_FM, 'newline in value should be ignored');
  });
});

test('env var with control character in value is ignored', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: 'son\x00net' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.strictEqual(out, REVIEWER_FM, 'control char in value should be ignored');
  });
});

test('missing agent file is silent no-op', () => {
  withTmpPlugin((root) => {
    // agents dir exists but reviewer file does not
    assert.doesNotThrow(() => {
      applyOverrides(root, { CAVECREW_REVIEWER_MODEL: 'sonnet' });
    });
  });
});

test('missing agents dir (non-plugin layout) is silent no-op', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-nolayout-'));
  try {
    assert.doesNotThrow(() => {
      applyOverrides(tmp, { CAVECREW_REVIEWER_MODEL: 'sonnet' });
    });
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test('unset env vars → files untouched', () => {
  withTmpPlugin((root, agentsDir) => {
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), REVIEWER_FM, 'utf8');
    applyOverrides(root, {});
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.strictEqual(out, REVIEWER_FM, 'file should be unchanged when env unset');
  });
});

test('body content preserved after model patch', () => {
  withTmpPlugin((root, agentsDir) => {
    const content = REVIEWER_FM + '\n\n## Extra\n\nExtra section body.\n';
    fs.writeFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), content, 'utf8');
    applyOverrides(root, { CAVECREW_REVIEWER_MODEL: 'sonnet' });
    const out = fs.readFileSync(path.join(agentsDir, 'cavecrew-reviewer.md'), 'utf8');
    assert.ok(out.includes('## Extra'), 'extra body section lost');
    assert.ok(out.includes('Extra section body.'), 'body text lost');
  });
});

// ── Summary ────────────────────────────────────────────────────────────────

console.log('');
if (failed === 0) {
  console.log('All ' + (passed + failed) + ' tests passed.');
  process.exit(0);
} else {
  console.error(failed + ' test(s) failed.');
  process.exit(1);
}
