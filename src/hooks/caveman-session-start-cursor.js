#!/usr/bin/env node
'use strict';
// Cursor plugin sessionStart hook — fail-open primer via additional_context only.

const { loadRuleBody } = require('../../bin/lib/caveman-skill-body');

function finish(payload) {
  try { process.stdout.write(JSON.stringify(payload)); } catch (_) { process.stdout.write('{}'); }
  process.exit(0);
}

function primerText() {
  if (!process.env.CURSOR_PLUGIN_ROOT) return null;
  try {
    return loadRuleBody(process.env.CURSOR_PLUGIN_ROOT).trim();
  } catch (_) {
    return null;
  }
}

process.stdin.setEncoding('utf8');
process.stdin.on('data', () => {});
process.stdin.on('end', () => {
  try {
    const primer = primerText();
    if (!primer) finish({});
    else finish({ additional_context: primer });
  } catch (_) {
    finish({});
  }
});
process.stdin.on('error', () => finish({}));
