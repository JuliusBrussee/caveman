#!/usr/bin/env node

import { cpSync, existsSync, mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '..');
const sourceDir = join(repoRoot, '.github', 'extensions', 'caveman');

function printUsage(exitCode = 0) {
  console.log(`Install Caveman Copilot CLI extension into target repo.

Usage:
  node scripts/install-copilot-extension.mjs <target-repo> [--force]
  node scripts/install-copilot-extension.mjs --global [--force]

Examples:
  node scripts/install-copilot-extension.mjs ../my-project
  node scripts/install-copilot-extension.mjs C:\\src\\my-project --force
  node scripts/install-copilot-extension.mjs --global
`);
  process.exit(exitCode);
}

function fail(message) {
  console.error(`ERROR: ${message}`);
  process.exit(1);
}

function getCopilotConfigRoot() {
  if (process.env.COPILOT_CONFIG_DIR) {
    return resolve(process.env.COPILOT_CONFIG_DIR);
  }

  const home = homedir();
  if (!home) {
    fail('Could not resolve user home directory for global install.');
  }

  return join(home, '.copilot');
}

let force = false;
let globalInstall = false;
const positional = [];

for (const arg of process.argv.slice(2)) {
  if (arg === '--help' || arg === '-h') {
    printUsage(0);
  } else if (arg === '--force' || arg === '-f') {
    force = true;
  } else if (arg === '--global' || arg === '-g') {
    globalInstall = true;
  } else {
    positional.push(arg);
  }
}

if ((globalInstall && positional.length !== 0) || (!globalInstall && positional.length !== 1)) {
  printUsage(1);
}

if (!existsSync(sourceDir)) {
  fail(`Source extension not found: ${sourceDir}`);
}

let destinationDir;

if (globalInstall) {
  destinationDir = join(getCopilotConfigRoot(), 'extensions', 'caveman');
} else {
  const targetRoot = resolve(process.cwd(), positional[0]);
  destinationDir = join(targetRoot, '.github', 'extensions', 'caveman');

  if (!existsSync(targetRoot)) {
    fail(`Target repo does not exist: ${targetRoot}`);
  }

  if (resolve(destinationDir) === resolve(sourceDir)) {
    fail('Target repo resolves to Caveman source repo. Pick another repository path.');
  }
}

if (existsSync(destinationDir) && !force) {
  fail(`Destination already exists: ${destinationDir}\nRe-run with --force to overwrite.`);
}

mkdirSync(dirname(destinationDir), { recursive: true });
cpSync(sourceDir, destinationDir, { recursive: true, force: true });

if (globalInstall) {
  console.log(`Installed Caveman Copilot CLI extension globally into: ${destinationDir}`);
  console.log('Next: open GitHub Copilot CLI anywhere. Caveman auto-loads for your user.');
} else {
  console.log(`Installed Caveman Copilot CLI extension into: ${destinationDir}`);
  console.log('Next: open GitHub Copilot CLI in that repo. Caveman auto-loads there.');
}
