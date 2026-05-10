// caveman → OpenClaw install / uninstall helper.
//
// OpenClaw is a self-hosted gateway that orchestrates Claude Code, Codex,
// Pi, OpenCode, and others. It has its own workspace + skills system at
// ~/.openclaw/workspace/. Skills there appear in a compact list and are
// loaded on-demand by the model — they are NOT injected as system prompt
// each turn. The bootstrap files (AGENTS.md, SOUL.md, TOOLS.md, MEMORY.md)
// ARE injected each turn under "Project Context", subject to a 12K-per-file
// and 60K-total cap.
//
// To make caveman always-on through OpenClaw, we do two writes:
//   1. Drop a copy of skills/caveman/SKILL.md into <workspace>/skills/caveman/
//      with OpenClaw-required frontmatter (`version`, `always: true`) merged
//      in. Makes the skill discoverable via `openclaw skills list` and lets
//      the orchestrated agent `read` it on demand.
//   2. Append a tiny marker-fenced bootstrap snippet to <workspace>/SOUL.md
//      pointing the agent at the skill. SOUL.md is auto-injected each turn,
//      so this is what actually drives always-on behavior.
//
// Idempotent on both writes. Uninstall removes the skill folder and strips
// the marker block from SOUL.md while preserving any user-authored content.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const SKILL_NAME = 'caveman';
const SKILL_VERSION = '1.0.0';
const MARK_BEGIN = '<!-- caveman-begin -->';
const MARK_END = '<!-- caveman-end -->';
const SOUL_FILE = 'SOUL.md';

function resolveWorkspace(env = process.env) {
  if (env.OPENCLAW_WORKSPACE) return path.resolve(env.OPENCLAW_WORKSPACE);
  return path.join(os.homedir(), '.openclaw', 'workspace');
}

function readIfExists(p) {
  try { return fs.readFileSync(p, 'utf8'); } catch (_) { return null; }
}

// ── Frontmatter helpers ───────────────────────────────────────────────────
// Lightweight YAML merge — we only need to insert `version` and `always` if
// they're absent. Avoids pulling in a YAML dep for a job this small. The
// caveman SKILL.md uses block-scalar `description: >`, which a naive split
// would mangle — but since we're only ever appending top-level keys (never
// editing existing ones), a string-prepend after the leading `---\n` is safe.

function splitFrontmatter(src) {
  if (!src.startsWith('---\n') && !src.startsWith('---\r\n')) {
    return { frontmatter: '', body: src };
  }
  const after = src.slice(src.indexOf('\n') + 1);
  const endRe = /(^|\n)---\s*(\r?\n|$)/;
  const m = endRe.exec(after);
  if (!m) return { frontmatter: '', body: src };
  const fmEnd = m.index + (m[1] ? 1 : 0);
  const fm = after.slice(0, fmEnd);
  const rest = after.slice(m.index + m[0].length);
  return { frontmatter: fm, body: rest };
}

function frontmatterHasKey(fm, key) {
  const re = new RegExp('(^|\\n)' + key + '\\s*:', 'i');
  return re.test(fm);
}

function mergeOpenclawFrontmatter(src) {
  const { frontmatter, body } = splitFrontmatter(src);
  const additions = [];
  if (!frontmatterHasKey(frontmatter, 'name')) additions.push(`name: ${SKILL_NAME}`);
  if (!frontmatterHasKey(frontmatter, 'version')) additions.push(`version: ${SKILL_VERSION}`);
  if (!frontmatterHasKey(frontmatter, 'always')) additions.push('always: true');
  if (additions.length === 0 && frontmatter) return src;
  const fmBody = (frontmatter ? frontmatter.trimEnd() + '\n' : '') + additions.join('\n') + (additions.length ? '\n' : '');
  return '---\n' + fmBody + '---\n' + body;
}

// ── Bootstrap snippet load ────────────────────────────────────────────────
function loadBootstrapSnippet(repoRoot) {
  if (repoRoot) {
    const p = path.join(repoRoot, 'src', 'rules', 'caveman-openclaw-bootstrap.md');
    const body = readIfExists(p);
    if (body) return body.endsWith('\n') ? body : body + '\n';
  }
  // Standalone fallback (curl|node case where there's no repo on disk).
  // Keep this in sync with src/rules/caveman-openclaw-bootstrap.md.
  return [
    MARK_BEGIN,
    '## Caveman mode (always on)',
    '',
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.',
    '',
    "The full ruleset and intensity levels live in this workspace's caveman skill:",
    '',
    '  skills/caveman/SKILL.md',
    '',
    'Default intensity: `full`. Switch with `/caveman lite|full|ultra|wenyan`.',
    'Stop with: "stop caveman" / "normal mode" / "deactivate caveman".',
    '',
    'Auto-Clarity: drop caveman for security warnings, irreversible action',
    'confirmations, multi-step sequences where fragments risk misread, or when',
    'user is confused or repeating. Resume after.',
    '',
    'Boundaries: code, commit messages, and PR descriptions stay normal prose.',
    MARK_END,
    '',
  ].join('\n');
}

function loadSkillBody(repoRoot) {
  if (!repoRoot) return null;
  return readIfExists(path.join(repoRoot, 'skills', 'caveman', 'SKILL.md'));
}

// ── SOUL.md marker-block append/strip ─────────────────────────────────────
function appendBootstrapToSoul(soulPath, snippet) {
  const existing = readIfExists(soulPath);
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
  fs.writeFileSync(soulPath, next, { mode: 0o644 });
  return { changed: true };
}

function stripBootstrapFromSoul(soulPath) {
  const existing = readIfExists(soulPath);
  if (!existing) return { changed: false, reason: 'no SOUL.md' };
  const begin = existing.indexOf(MARK_BEGIN);
  const end = existing.indexOf(MARK_END);
  if (begin === -1 || end === -1 || end <= begin) return { changed: false, reason: 'no marker block' };
  const before = existing.slice(0, begin);
  const after = existing.slice(end + MARK_END.length);
  // Collapse adjacent blank lines around the cut so we don't leave a triple
  // newline scar from `\n\n<begin>...\n<end>\n\n`.
  let next = (before.replace(/\n+$/, '\n') + after.replace(/^\n+/, '\n')).trimEnd();
  next = next ? next + '\n' : '';
  if (next === '') {
    // SOUL.md only contained our block — remove the file so OpenClaw doesn't
    // bootstrap an empty section every turn.
    try { fs.unlinkSync(soulPath); } catch (_) {}
    return { changed: true, removed: true };
  }
  fs.writeFileSync(soulPath, next, { mode: 0o644 });
  return { changed: true };
}

// ── Public API ────────────────────────────────────────────────────────────
function installOpenclaw({ workspace, repoRoot, dryRun = false, force = false, log = noopLog() } = {}) {
  const ws = workspace || resolveWorkspace();
  const skillBody = loadSkillBody(repoRoot);
  if (!skillBody) {
    log.warn('  openclaw install requires the caveman repo on disk (skills/caveman/SKILL.md missing).');
    log.note('  Re-run from a clone or via `npx -y github:JuliusBrussee/caveman -- --only openclaw`.');
    return { ok: false, reason: 'repo not available' };
  }
  const snippet = loadBootstrapSnippet(repoRoot);

  if (!fs.existsSync(ws)) {
    if (!force) {
      log.warn(`  openclaw workspace not found at ${ws}.`);
      log.note('  Either install OpenClaw (https://openclaw.ai) and re-run, or pass --force to mkdir.');
      return { ok: false, reason: 'workspace missing' };
    }
    if (!dryRun) fs.mkdirSync(ws, { recursive: true });
  }

  const skillDir = path.join(ws, 'skills', SKILL_NAME);
  const skillFile = path.join(skillDir, 'SKILL.md');
  const soulFile = path.join(ws, SOUL_FILE);

  if (dryRun) {
    log.note(`  would write ${skillFile} (with version/always frontmatter)`);
    log.note(`  would ${fs.existsSync(soulFile) ? 'append to' : 'create'} ${soulFile} (caveman bootstrap block)`);
    return { ok: true, dryRun: true };
  }

  fs.mkdirSync(skillDir, { recursive: true });
  const merged = mergeOpenclawFrontmatter(skillBody);
  fs.writeFileSync(skillFile, merged, { mode: 0o644 });
  log.write(`  installed: ${skillFile}\n`);

  const soul = appendBootstrapToSoul(soulFile, snippet);
  if (soul.changed) log.write(`  wrote bootstrap block to ${soulFile}\n`);
  else log.note(`  ${soulFile} already contains caveman bootstrap`);

  return { ok: true };
}

function uninstallOpenclaw({ workspace, dryRun = false, log = noopLog() } = {}) {
  const ws = workspace || resolveWorkspace();
  const skillDir = path.join(ws, 'skills', SKILL_NAME);
  const soulFile = path.join(ws, SOUL_FILE);

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

  if (fs.existsSync(soulFile)) {
    if (dryRun) {
      log.note(`  would strip caveman block from ${soulFile}`);
      touched = true;
    } else {
      const r = stripBootstrapFromSoul(soulFile);
      if (r.changed) {
        log.note(r.removed ? `  removed ${soulFile}` : `  stripped caveman block from ${soulFile}`);
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
  installOpenclaw,
  uninstallOpenclaw,
  resolveWorkspace,
  // exported for tests
  mergeOpenclawFrontmatter,
  splitFrontmatter,
  appendBootstrapToSoul,
  stripBootstrapFromSoul,
  loadBootstrapSnippet,
  MARK_BEGIN,
  MARK_END,
  SKILL_NAME,
  SKILL_VERSION,
};
