'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const OWNED = require('./owned-install');

const REPO = 'JuliusBrussee/caveman';

function usesNativeSkills(provider, env = process.env) {
  return provider === 'continue' || provider === 'aider-desk' ||
    provider === 'antigravity' || provider === 'antigravity-2' ||
    (provider === 'iflow' && Boolean(env.IFLOW_HOME)) ||
    (provider === 'crush' && Boolean(env.CRUSH_SKILLS_DIR));
}

// Match the vendor's own truthy environment handling, including relative paths.
// Continue: continuedev/continue core/util/paths.ts (5522c6f).
// AiderDesk: hotovo/aider-desk src/main/constants.ts (e76c2f0).
function skillsRoot(provider, {
  env = process.env, home = os.homedir(), cwd = process.cwd(), platform = process.platform,
} = {}) {
  const paths = platform === 'win32' ? path.win32 : path.posix;
  let configDir;
  if (provider === 'continue') {
    configDir = env.CONTINUE_GLOBAL_DIR || paths.join(home, '.continue');
  } else if (provider === 'aider-desk') {
    configDir = env.AIDER_DESK_HOME_DIR || paths.join(home, env.AIDER_DESK_DIR || '.aider-desk');
  } else if (provider === 'antigravity' || provider === 'antigravity-2') {
    // Official docs distinguish IDE and 2.0 global discovery. Neither page
    // documents a home override: /docs/ide/skills/ and /docs/skills/ at
    // https://www.antigravity.google (checked 2026-09-08).
    configDir = paths.join(home, '.gemini', provider === 'antigravity' ? 'antigravity' : 'config');
  } else if (provider === 'iflow') {
    // @iflow-ai/iflow-cli 0.5.19: Tn() normalizes a truthy IFLOW_HOME;
    // SkillScanner joins it with "skills" and has no other global root.
    configDir = env.IFLOW_HOME || paths.join(home, '.iflow');
  } else if (provider === 'crush' && env.CRUSH_SKILLS_DIR) {
    // charmbracelet/crush internal/config/load.go (7f9a8e4): this override
    // replaces the entire skill search path, without adding a suffix.
    return paths.resolve(cwd, env.CRUSH_SKILLS_DIR);
  } else {
    throw new Error(`no native skill directory for ${provider}`);
  }
  return paths.resolve(cwd, configDir, 'skills');
}

function skillOperations(sourceRoot, prefix) {
  const operations = [];
  for (const entry of fs.readdirSync(sourceRoot, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const source = path.join(sourceRoot, entry.name);
    if (!fs.existsSync(path.join(source, 'SKILL.md'))) continue;
    operations.push({ relativePath: `${prefix}/${entry.name}`, write: stage => OWNED.copyPath(source, stage) });
  }
  if (operations.length === 0) throw new Error(`no skill directories found in ${sourceRoot}`);
  return operations;
}

function integrationName(provider) {
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(provider)) throw new Error('invalid skill provider');
  return `skills-${provider}`;
}

function ownershipOptions(provider, root) {
  const resolved = path.resolve(root);
  const prefix = path.basename(resolved);
  if (!prefix) throw new Error('the filesystem root cannot be a managed skill directory');
  // Keep backup SKILL.md files outside recursive vendor discovery.
  return { root: path.dirname(resolved), integration: integrationName(provider), prefix };
}

// root can be supplied for another vendor whose directory has been verified.
// run uses the installer's portable process launcher; staging never targets HOME.
function install({
  provider, root = skillsRoot(provider), repoRoot, force = false, dryRun = false,
  note = () => {}, run,
}) {
  const ownership = ownershipOptions(provider, root);
  if (dryRun) {
    note(`  would copy Caveman skills into ${root}`);
    return { root, count: 0, dryRun: true };
  }
  let temporary;
  try {
    let sourceRoot;
    if (repoRoot) {
      sourceRoot = path.join(repoRoot, 'skills');
    } else {
      if (typeof run !== 'function') throw new Error('skill download requires the portable installer launcher');
      temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-skill-source-'));
      // The universal profile is only a disposable download destination. --copy
      // materializes every selected skill without touching any agent profile.
      const result = run('npx', [
        '-y', 'skills', 'add', REPO, '--skill', '*', '-a', 'codex', '--yes', '--copy',
      ], { cwd: temporary });
      if (!result || result.status !== 0 || result.error || result.signal) {
        throw new Error(`skill download failed${result?.error?.message ? `: ${result.error.message}` : ''}`);
      }
      sourceRoot = path.join(temporary, '.agents', 'skills');
    }
    const operations = skillOperations(sourceRoot, ownership.prefix);
    OWNED.installOwned({ ...ownership, operations, force, note });
    return { root, count: operations.length };
  } finally {
    if (temporary) fs.rmSync(temporary, { recursive: true, force: true });
  }
}

function uninstall({ provider, root = skillsRoot(provider), dryRun = false, note, warn }) {
  return OWNED.uninstallOwned({ ...ownershipOptions(provider, root), dryRun, note, warn });
}

module.exports = { usesNativeSkills, skillsRoot, install, uninstall };
