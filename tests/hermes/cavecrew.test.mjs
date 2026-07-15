import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const skillPath = path.join(root, 'skills', 'cavecrew', 'SKILL.md');
const text = fs.readFileSync(skillPath, 'utf8');
const match = text.match(/## Hermes Agent \(`delegate_task`\)([\s\S]*?)(?=\n## |$)/);

function hermesSection() {
  assert.ok(match, 'Cavecrew must contain a dedicated Hermes Agent (`delegate_task`) section');
  return match[1];
}

test('Hermes maps all presets to self-contained leaf delegate_task contexts', () => {
  const section = hermesSection();
  for (const preset of ['investigator', 'builder', 'reviewer']) {
    assert.match(section, new RegExp(`### ${preset}`, 'i'), `${preset} mapping missing`);
  }
  assert.match(section, /delegate_task/);
  assert.match(section, /role["'`:]?\s*[=:]\s*["'`]leaf["'`]/i);
  assert.match(section, /complete self-contained context/i);
  assert.match(section, /no parent conversation memory/i);
  assert.match(section, /no nested delegation/i);
});

test('Hermes documents inherited model and capability exception honestly', () => {
  const section = hermesSection();
  assert.match(section, /inherit(?:s|ed)? (?:the )?parent model/i);
  assert.match(section, /reasoning/i);
  assert.match(section, /no per-call model/i);
  assert.match(section, /not capability-sandboxed/i);
  assert.match(section, /inherits? (?:the )?parent(?:'s)? (?:complete )?toolset/i);
  assert.match(section, /must not edit/i);
  assert.match(section, /snapshot/i);
  assert.match(section, /compare/i);
  assert.doesNotMatch(section, /cannot edit/i);
});

test('Hermes preserves each role scope and compressed output contract', () => {
  const section = hermesSection();
  assert.match(section, /path:line/i);
  assert.match(section, /1[–-]2 files/i);
  assert.match(section, /refuse(?:s|d)? 3\+ files/i);
  assert.match(section, /severity/i);
  assert.match(section, /findings only/i);
  assert.match(section, /compressed output/i);
  assert.match(section, /parent (?:must )?verif/i);
  assert.match(section, /external side effects/i);
  assert.match(section, /self-report/i);
});

test('Hermes batches independent scouts and excludes Claude-only execution APIs', () => {
  const section = hermesSection();
  assert.match(section, /one batch/i);
  assert.match(section, /up to (?:the current limit of )?3/i);
  assert.match(section, /Do not use (?:Claude(?: Code)? )?`?(?:Task|Explore)`?/i);
});
