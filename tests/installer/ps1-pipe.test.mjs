// Regression for #565: `irm .../install.ps1 | iex` crashed with
// "Cannot bind argument to parameter 'Path' because it is null."
//
// Two pipe-execution rules for install.ps1 (static checks — CI has no pwsh):
//   1. No top-level param() block. iex executes the file as a string, so a
//      top-level param can never receive arguments and (depending on host)
//      trips parsing. All logic lives in a function invoked at the bottom.
//   2. Script-path variables ($PSCommandPath / $MyInvocation.MyCommand.Path)
//      are $null under iex — any use must be guarded, never passed straight
//      into Split-Path (that was the #565 crash).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '..', '..');
const PS1 = fs.readFileSync(path.join(REPO_ROOT, 'install.ps1'), 'utf8');

// Strip comment lines so doc mentions of param()/path vars don't false-positive.
const code = PS1.split('\n').filter(l => !/^\s*#/.test(l)).join('\n');

test('#565 install.ps1 has no top-level param block (everything inside a function)', () => {
  const beforeFunction = code.slice(0, code.indexOf('function '));
  assert.ok(code.includes('function '), 'install.ps1 must wrap its logic in a function for iex piping');
  assert.ok(
    !/param\s*\(/i.test(beforeFunction),
    'install.ps1 must not declare a top-level param() — it cannot receive args under `irm | iex` (issue #565)',
  );
});

test('#565 install.ps1 never uses $MyInvocation.MyCommand.Path (null under iex)', () => {
  assert.ok(
    !/\$MyInvocation\.MyCommand\.Path/i.test(code),
    'install.ps1 must not rely on $MyInvocation.MyCommand.Path — it is $null when piped to iex (issue #565)',
  );
});

test('#565 install.ps1 guards $PSCommandPath before Split-Path', () => {
  if (/\$PSCommandPath/i.test(code)) {
    assert.match(
      code,
      /if\s*\(\s*\$PSCommandPath\s*\)/i,
      '$PSCommandPath is $null under `irm | iex` — it must be truthiness-guarded before use (issue #565)',
    );
  }
});

test('#565 install.ps1 invokes its function at the bottom (script still does something)', () => {
  const lastLines = code.trim().split('\n').slice(-3).join('\n');
  assert.match(
    lastLines,
    /Install-Caveman/,
    'install.ps1 must actually invoke Install-Caveman after defining it',
  );
});
