// This repo is public, and routing decisions are made by Caveman Cloud. The
// local runtime only asks: it sends the caller's models, counts and the raw
// conversation text (contracts route-ask-v1), then applies the answer or fails
// open. No tracked file may carry names from the private router or its policy,
// scoring or classification.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const self = path.relative(root, __filename);
const terms = [
  'localpolicy', 'ClassifyTask', 'ScoutSignals', 'scoutAsk', 'taskclass', 'jevprior',
  'decideAsk', 'continueAsk', 'sliderStop', 'effort_binding', 'askwords', 'evidence matrix', 'routerd',
];
// routerd alone needs letter boundaries: openRouterDeveloper is not it.
const pattern = new RegExp(terms.map((term) => {
  const escaped = term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return term === 'routerd' ? `(?<![a-z])${escaped}(?![a-z])` : escaped;
}).join('|'), 'i');
// Unrelated uses, by path and term.
const allowed = {
  // Eval snapshots: an "evidence matrix" of benchmark arms, nothing to do with routing.
  'tests/test_eval_snapshot_contract.py': ['evidence matrix'],
};
const lockfiles = new Set(['pnpm-lock.yaml', 'package-lock.json', 'yarn.lock', 'uv.lock', 'go.sum', 'Cargo.lock']);

test('no tracked file carries private router identifiers', () => {
  const files = execFileSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8', maxBuffer: 64 << 20 }).split('\0').filter(Boolean);
  const hits = [];
  for (const file of files) {
    if (file === self || lockfiles.has(path.basename(file)) || file.split('/').includes('node_modules')) continue;
    let bytes;
    try {
      const full = path.join(root, file);
      if (!fs.lstatSync(full).isFile()) continue;
      bytes = fs.readFileSync(full);
    } catch {
      continue;
    }
    if (bytes.subarray(0, 8192).includes(0)) continue;
    const text = bytes.toString('utf8');
    if (!pattern.test(text)) continue;
    text.split('\n').forEach((line, i) => {
      const found = [...line.matchAll(new RegExp(pattern.source, 'gi'))].map((match) => match[0].toLowerCase());
      if (found.some((term) => !(allowed[file] || []).includes(term))) hits.push(`${file}:${i + 1}: ${line.trim()}`);
    });
  }
  assert.deepStrictEqual(hits, [], 'routing logic stays in Caveman Cloud; the hub only asks /v1/route');
});
