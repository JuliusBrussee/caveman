// caveman → Hermes Agent install / uninstall helper.
//
// Hermes Agent is a personal AI agent that runs across CLI, Telegram,
// Discord, Slack, and 20+ platforms. It uses a skill-based system at
// ~/.hermes/skills/<category>/<name>/SKILL.md. Skills are loaded on-demand
// by keyword matching in the agent's system prompt.
//
// For always-on caveman behavior, we do two writes:
//   1. Drop skills/caveman/SKILL.md into ~/.hermes/skills/productivity/caveman/
//      with Hermes-required frontmatter merged in. Makes the skill discoverable
//      via the agent's skill loader.
//   2. Write a tiny marker-fenced bootstrap snippet to ~/.hermes/memories/caveman.
//      Hermes auto-injects memory entries every turn (subject to a char cap),
//      so this drives always-on behavior — similar to OpenClaw's SOUL.md approach.
//
// Idempotent on both writes. Uninstall removes the skill folder and strips
// the marker block from the memory file while preserving any user-authored content.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const SKILL_NAME = 'caveman';
const SKILL_CATEGORY = 'productivity';
const MARK_BEGIN = '<!-- caveman-begin -->';
const MARK_END = '<!-- caveman-end -->';

function resolveHermesHome(env = process.env) {
  if (env.HERMES_HOME) return path.resolve(env.HERMES_HOME);
  return path.join(os.homedir(), '.hermes');
}

function readIfExists(p) {
  try { return fs.readFileSync(p, 'utf8'); } catch (_) { return null; }
}

// ── Bootstrap snippet load ────────────────────────────────────────────────
function loadBootstrapSnippet(repoRoot) {
  if (repoRoot) {
    const p = path.join(repoRoot, 'src', 'rules', 'caveman-hermes-bootstrap.md');
    const body = readIfExists(p);
    if (body) return body.endsWith('\n') ? body : body + '\n';
  }
  // Standalone fallback (curl|node case where there's no repo on disk).
  // Keep this in sync with src/rules/caveman-hermes-bootstrap.md.
  return [
    MARK_BEGIN,
    'Caveman mode active. Respond terse like smart caveman. All technical substance stay. Only fluff die.',
    '',
    'Rules: Drop filler/hedging/articles. Fragments OK. Short synonyms. Technical terms exact. Code unchanged.',
    'Pattern: [thing] [action] [reason]. [next step].',
    'Stop: "stop caveman" / "normal mode".',
    '',
    'Auto-Clarity: drop caveman for security warnings, irreversible actions, user confused. Resume after.',
    'Boundaries: code, commits, PRs written normal.',
    MARK_END,
    '',
  ].join('\n');
}

function loadSkillBody(repoRoot) {
  if (!repoRoot) return null;
  return readIfExists(path.join(repoRoot, 'skills', 'caveman', 'SKILL.md'));
}

// ── Memory file marker-block append/strip ─────────────────────────────────
function appendBootstrapToMemory(memPath, snippet) {
  const existing = readIfExists(memPath);
  if (existing && existing.includes(MARK_BEGIN) && existing.includes(MARK_END)) {
    return { changed: false, reason: 'already present' };
  }
  let next;
  if (existing && existing.length) {
    const sep = existing.endsWith('\n\n') ? '' : (existing.endsWith('\n') ? '\n' : '\n\n');
    next = existing + sep + snippet;
  } else {
    next = snippet;
  }
  fs.writeFileSync(memPath, next, { mode: 0o644 });
  return { changed: true };
}

function stripBootstrapFromMemory(memPath) {
  const existing = readIfExists(memPath);
  if (!existing) return { changed: false, reason: 'no memory file' };
  const begin = existing.indexOf(MARK_BEGIN);
  const end = existing.indexOf(MARK_END);
  if (begin === -1 || end === -1 || end <= begin) return { changed: false, reason: 'no marker block' };
  const before = existing.slice(0, begin);
  const after = existing.slice(end + MARK_END.length);
  let next = (before.replace(/\n+$/, '\n') + after.replace(/^\n+/, '\n')).trimEnd();
  next = next ? next + '\n' : '';
  if (next === '') {
    // Memory file only contained our block — remove the file.
    try { fs.unlinkSync(memPath); } catch (_) {}
    return { changed: true, removed: true };
  }
  fs.writeFileSync(memPath, next, { mode: 0o644 });
  return { changed: true };
}

// ── Public API ────────────────────────────────────────────────────────────
function installHermes({ hermesHome, repoRoot, dryRun = false, force = false, log = noopLog() } = {}) {
  const home = hermesHome || resolveHermesHome();
  const skillBody = loadSkillBody(repoRoot);
  if (!skillBody) {
    log.warn('  hermes install requires the caveman repo on disk (skills/caveman/SKILL.md missing).');
    log.note('  Re-run from a clone or via `npx -y github:JuliusBrussee/caveman -- --only hermes`.');
    return { ok: false, reason: 'repo not available' };
  }

  const snippet = loadBootstrapSnippet(repoRoot);
  const skillDir = path.join(home, 'skills', SKILL_CATEGORY, SKILL_NAME);
  const skillFile = path.join(skillDir, 'SKILL.md');
  const memFile = path.join(home, 'memories', SKILL_NAME);

  if (dryRun) {
    log.note(`  would write ${skillFile}`);
    log.note(`  would ${fs.existsSync(memFile) ? 'update' : 'create'} ${memFile} (always-on bootstrap)`);
    return { ok: true, dryRun: true };
  }

  // Write skill file
  fs.mkdirSync(skillDir, { recursive: true });
  fs.writeFileSync(skillFile, skillBody, { mode: 0o644 });
  log.write(`  installed: ${skillFile}\n`);

  // Write memory entry for always-on behavior
  fs.mkdirSync(path.dirname(memFile), { recursive: true });
  const mem = appendBootstrapToMemory(memFile, snippet);
  if (mem.changed) log.write(`  wrote bootstrap to ${memFile}\n`);
  else log.note(`  ${memFile} already contains caveman bootstrap`);

  return { ok: true };
}

function uninstallHermes({ hermesHome, dryRun = false, log = noopLog() } = {}) {
  const home = hermesHome || resolveHermesHome();
  const skillDir = path.join(home, 'skills', SKILL_CATEGORY, SKILL_NAME);
  const memFile = path.join(home, 'memories', SKILL_NAME);

  let touched = false;

  if (fs.existsSync(skillDir)) {
    if (dryRun) {
      log.note(`  would remove ${skillDir}/`);
    } else {
      try { fs.rmSync(skillDir, { recursive: true, force: true }); } catch (_) {}
      log.note(`  removed ${skillDir}`);
    }
    touched = true;
  }

  if (fs.existsSync(memFile)) {
    if (dryRun) {
      log.note(`  would strip caveman block from ${memFile}`);
      touched = true;
    } else {
      const r = stripBootstrapFromMemory(memFile);
      if (r.changed) {
        log.note(r.removed ? `  removed ${memFile}` : `  stripped caveman block from ${memFile}`);
        touched = true;
      }
    }
  }

  return { ok: true, touched };
}

function noopLog() {
  return {
    write: (_) => {},
    note: (_) => {},
    warn: (_) => {},
  };
}

module.exports = {
  installHermes,
  uninstallHermes,
  resolveHermesHome,
  appendBootstrapToMemory,
  stripBootstrapFromMemory,
  loadBootstrapSnippet,
  MARK_BEGIN,
  MARK_END,
  SKILL_NAME,
  SKILL_CATEGORY,
};
