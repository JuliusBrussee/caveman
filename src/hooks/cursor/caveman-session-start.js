#!/usr/bin/env node
// Cursor sessionStart adapter — wraps caveman-activate.js for Cursor's JSON hook contract.
// Docs: https://cursor.com/docs/hooks#sessionstart

'use strict';

const { spawnSync } = require('child_process');
const path = require('path');

const script = path.join(__dirname, 'caveman-activate.js');
const result = spawnSync(process.execPath, [script], {
  encoding: 'utf8',
  env: process.env,
});

const context = (result.stdout || '').trim();
if (!context) process.exit(0);

process.stdout.write(JSON.stringify({ additional_context: context }));
