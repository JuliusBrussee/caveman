#!/usr/bin/env node

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const PATCH_COMPATIBILITY_WINDOW = 20;

function parseOptions(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (!token.startsWith('--')) {
      throw new Error(`Unexpected argument: ${token}`);
    }
    const key = token.slice(2);
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`Missing value for --${key}`);
    }
    options[key] = value;
    index += 1;
  }
  return options;
}

function resolveExtensionSource(repoRoot) {
  const candidates = [
    path.join(repoRoot, 'vscode', 'guardex-active-agents'),
    path.join(repoRoot, 'templates', 'vscode', 'guardex-active-agents'),
  ];

  for (const candidate of candidates) {
    if (fs.existsSync(path.join(candidate, 'package.json'))) {
      return candidate;
    }
  }

  throw new Error('Could not find the Guardex VS Code companion sources.');
}

function removeIfExists(targetPath) {
  if (fs.existsSync(targetPath)) {
    fs.rmSync(targetPath, { recursive: true, force: true });
  }
}

function parseSimpleSemver(version) {
  const parts = String(version || '').trim().split('.').map((part) => Number.parseInt(part, 10));
  if (parts.length !== 3 || parts.some((part) => Number.isNaN(part))) {
    throw new Error(`Expected simple semver for the Active Agents companion, received "${version}".`);
  }
  return parts;
}

function buildInstallTargets(extensionId, version, extensionsDir) {
  const [major, minor, patch] = parseSimpleSemver(version);
  const firstCompatiblePatch = Math.max(0, patch - PATCH_COMPATIBILITY_WINDOW);
  const targets = [path.join(extensionsDir, extensionId)];

  for (let compatiblePatch = firstCompatiblePatch; compatiblePatch <= patch; compatiblePatch += 1) {
    targets.push(path.join(extensionsDir, `${extensionId}-${major}.${minor}.${compatiblePatch}`));
  }

  return targets;
}

function main() {
  const repoRoot = path.resolve(__dirname, '..');
  const options = parseOptions(process.argv.slice(2));
  const sourceDir = resolveExtensionSource(repoRoot);
  const manifest = JSON.parse(fs.readFileSync(path.join(sourceDir, 'package.json'), 'utf8'));
  const extensionId = `${manifest.publisher}.${manifest.name}`;
  const extensionsDir = path.resolve(
    options['extensions-dir'] ||
      process.env.GUARDEX_VSCODE_EXTENSIONS_DIR ||
      process.env.VSCODE_EXTENSIONS_DIR ||
      path.join(os.homedir(), '.vscode', 'extensions'),
  );

  fs.mkdirSync(extensionsDir, { recursive: true });
  const targetDirs = buildInstallTargets(extensionId, manifest.version, extensionsDir);
  const canonicalTargetDir = targetDirs[0];
  const keepDirNames = new Set(targetDirs.map((targetDir) => path.basename(targetDir)));

  for (const entry of fs.readdirSync(extensionsDir, { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue;
    }
    if (keepDirNames.has(entry.name)) {
      continue;
    }
    if (entry.name === extensionId || entry.name.startsWith(`${extensionId}-`)) {
      removeIfExists(path.join(extensionsDir, entry.name));
    }
  }

  for (const targetDir of targetDirs) {
    removeIfExists(targetDir);
    fs.cpSync(sourceDir, targetDir, {
      recursive: true,
      force: true,
      preserveTimestamps: true,
    });
  }

  process.stdout.write(
    `[guardex-active-agents] Installed ${extensionId}@${manifest.version} to ${canonicalTargetDir}\n` +
      `[guardex-active-agents] Refreshed ${targetDirs.length - 1} recent patch compatibility path(s) for already-open windows.\n` +
      '[guardex-active-agents] Reload each already-open VS Code window to activate the newest Source Control companion.\n',
  );
}

try {
  main();
} catch (error) {
  process.stderr.write(`[guardex-active-agents] ${error.message}\n`);
  process.exitCode = 1;
}
