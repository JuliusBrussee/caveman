#!/usr/bin/env node
// Cursor beforeSubmitPrompt adapter — wraps caveman-mode-tracker.js.
// Maps Claude Code UserPromptSubmit behavior to Cursor's beforeSubmitPrompt hook.
// Docs: https://cursor.com/docs/reference/third-party-hooks

'use strict';

const { spawnSync } = require('child_process');
const path = require('path');

function readStdin() {
  return new Promise((resolve) => {
    let data = '';
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', (chunk) => { data += chunk; });
    process.stdin.on('end', () => resolve(data));
  });
}

async function main() {
  const input = await readStdin();
  let payload = { prompt: input.trim() };
  try {
    const parsed = JSON.parse(input);
    if (parsed && typeof parsed.prompt === 'string') payload = parsed;
  } catch (_) { /* plain string fallback */ }

  const script = path.join(__dirname, 'caveman-mode-tracker.js');
  const result = spawnSync(process.execPath, [script], {
    input: JSON.stringify(payload),
    encoding: 'utf8',
    env: process.env,
  });

  const out = (result.stdout || '').trim();
  if (!out) {
    process.stdout.write(JSON.stringify({ continue: true }));
    return;
  }

  try {
    JSON.parse(out);
    process.stdout.write(out);
  } catch (_) {
    process.stdout.write(JSON.stringify({ continue: true, additional_context: out }));
  }
}

main().catch(() => {
  process.stdout.write(JSON.stringify({ continue: true }));
});
