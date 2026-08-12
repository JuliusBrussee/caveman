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
const crypto = require('crypto');
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

function sameFile(left, right) {
  return left && right && left.dev === right.dev && left.ino === right.ino;
}

function digest(content) {
  return crypto.createHash('sha256').update(content, 'utf8').digest('hex');
}

function sameSnapshot(left, right) {
  return sameFile(left, right) &&
    typeof left.cavemanContentSHA256 === 'string' &&
    left.cavemanContentSHA256 === right.cavemanContentSHA256;
}

function readRegularIfExists(p) {
  let before;
  try { before = fs.lstatSync(p); } catch (error) {
    if (error && error.code === 'ENOENT') return { content: null, stat: null };
    throw error;
  }
  if (before.isSymbolicLink() || !before.isFile()) {
    throw new Error(`openclaw: refusing non-regular file ${p}`);
  }
  const fd = fs.openSync(p, 'r');
  try {
    const opened = fs.fstatSync(fd);
    if (!sameFile(before, opened)) throw new Error(`openclaw: ${p} changed while opening`);
    const content = fs.readFileSync(fd, 'utf8');
    opened.cavemanContentSHA256 = digest(content);
    return { content, stat: opened };
  } finally {
    fs.closeSync(fd);
  }
}

function atomicWriteRegular(p, content, expectedStat) {
  const dir = path.dirname(p);
  const mode = expectedStat ? expectedStat.mode & 0o777 : 0o600;
  const tmp = path.join(dir, `.${path.basename(p)}.${process.pid}.${cryptoRandom()}.tmp`);
  let fd;
  try {
    fd = fs.openSync(tmp, 'wx', mode);
    fs.writeFileSync(fd, content, 'utf8');
    fs.fsyncSync(fd);
    fs.closeSync(fd);
    fd = undefined;

    const current = readRegularIfExists(p);
    if ((expectedStat && !sameSnapshot(expectedStat, current.stat)) || (!expectedStat && current.stat)) {
      throw new Error(`openclaw: ${p} changed before atomic replace`);
    }
    fs.renameSync(tmp, p);
    try {
      const dirFD = fs.openSync(dir, 'r');
      try { fs.fsyncSync(dirFD); } finally { fs.closeSync(dirFD); }
    } catch (_) { /* directory fsync is not supported on every platform */ }
  } finally {
    if (fd !== undefined) try { fs.closeSync(fd); } catch (_) {}
    try { fs.unlinkSync(tmp); } catch (_) {}
  }
}

function cryptoRandom() {
  return crypto.randomBytes(6).toString('hex');
}

function unlinkRegular(p, expectedStat) {
  const current = readRegularIfExists(p);
  if (!sameSnapshot(expectedStat, current.stat)) {
    throw new Error(`openclaw: refusing changed or non-regular file ${p}`);
  }
  fs.unlinkSync(p);
}

function ensureRealDirectory(p, create = false) {
  let stat;
  try { stat = fs.lstatSync(p); } catch (error) {
    if (!error || error.code !== 'ENOENT' || !create) throw error;
    fs.mkdirSync(p);
    stat = fs.lstatSync(p);
  }
  if (stat.isSymbolicLink() || !stat.isDirectory()) {
    throw new Error(`openclaw: refusing non-directory or symlink ${p}`);
  }
}

function restoreRegularSnapshot(p, snapshot) {
  const current = readRegularIfExists(p);
  if (snapshot.content === null) {
    if (current.stat) unlinkRegular(p, current.stat);
  } else {
    atomicWriteRegular(p, snapshot.content, current.stat);
  }
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

function mergeOpenclawFrontmatter(src, opts = {}) {
  const version = opts.version || SKILL_VERSION;
  const { frontmatter, body } = splitFrontmatter(src);
  const additions = [];
  if (!frontmatterHasKey(frontmatter, 'name')) additions.push(`name: ${SKILL_NAME}`);
  if (!frontmatterHasKey(frontmatter, 'version')) additions.push(`version: ${version}`);
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
    'Default intensity: `full`. Switch with `/caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra`.',
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
//
// Damage tolerance (#596): a stray or truncated marker (interrupted write,
// partial user edit) used to chain into data loss — append saw "no complete
// block" and added a SECOND block; strip then cut from the FIRST begin to the
// FIRST end, which spanned all user content between the stray marker and the
// appended block. The scan below pairs each begin with the nearest end BEFORE
// the next begin; an unpaired marker is removed as just the marker itself,
// never as a span over user content.

function stripAllBootstrapBlocks(text) {
  let result = '';
  let found = false;
  let i = 0;
  while (i < text.length) {
    const b = text.indexOf(MARK_BEGIN, i);
    if (b === -1) { result += text.slice(i); break; }
    result += text.slice(i, b);
    found = true;
    const nextB = text.indexOf(MARK_BEGIN, b + MARK_BEGIN.length);
    const e = text.indexOf(MARK_END, b + MARK_BEGIN.length);
    if (e !== -1 && (nextB === -1 || e < nextB)) {
      i = e + MARK_END.length; // well-formed block — drop begin..end inclusive
    } else {
      i = b + MARK_BEGIN.length; // orphan begin — drop only the marker itself
    }
    // Collapse the blank-line scar around the cut (same cosmetic rule the
    // old single-cut code applied): keep at most one newline on each side.
    result = result.replace(/\n+$/, '\n');
    const lead = /^\n+/.exec(text.slice(i));
    if (lead) i += lead[0].length - (result ? 1 : 0);
  }
  // Orphan end markers (begin already gone or never written) — drop marker only.
  while (result.includes(MARK_END)) { found = true; result = result.replace(MARK_END, ''); }
  return { next: result, found };
}

function appendBootstrapToSoul(soulPath, snippet) {
  const opened = readRegularIfExists(soulPath);
  const existing = opened.content;
  const count = (s, sub) => s.split(sub).length - 1;
  let base = existing;
  let repaired = false;
  if (existing) {
    const nb = count(existing, MARK_BEGIN);
    const ne = count(existing, MARK_END);
    if (nb === 1 && ne === 1 && existing.indexOf(MARK_END) > existing.indexOf(MARK_BEGIN)) {
      return { changed: false, reason: 'already present' };
    }
    if (nb > 0 || ne > 0) {
      // Damaged markers — strip them safely first, then append one clean block.
      base = stripAllBootstrapBlocks(existing).next;
      repaired = true;
    }
  }
  let next;
  if (base && base.length) {
    const sep = base.endsWith('\n\n') ? '' : (base.endsWith('\n') ? '\n' : '\n\n');
    next = base + sep + snippet;
  } else {
    next = snippet;
  }
  atomicWriteRegular(soulPath, next, opened.stat);
  return repaired ? { changed: true, repaired: true } : { changed: true };
}

function stripBootstrapFromSoul(soulPath) {
  const opened = readRegularIfExists(soulPath);
  const existing = opened.content;
  if (!existing) return { changed: false, reason: 'no SOUL.md' };
  const { next: stripped, found } = stripAllBootstrapBlocks(existing);
  if (!found) return { changed: false, reason: 'no marker block' };
  let next = stripped.trimEnd();
  next = next ? next + '\n' : '';
  if (next === '') {
    // SOUL.md only contained our block — remove the file so OpenClaw doesn't
    // bootstrap an empty section every turn.
    unlinkRegular(soulPath, opened.stat);
    return { changed: true, removed: true };
  }
  atomicWriteRegular(soulPath, next, opened.stat);
  return { changed: true };
}

// ── Public API ────────────────────────────────────────────────────────────
function installOpenclaw({ workspace, repoRoot, dryRun = false, force = false, log = noopLog(), version } = {}) {
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

  ensureRealDirectory(ws);
  ensureRealDirectory(path.join(ws, 'skills'), true);
  ensureRealDirectory(skillDir, true);
  const priorSkill = readRegularIfExists(skillFile);
  const merged = mergeOpenclawFrontmatter(skillBody, { version });
  try {
    atomicWriteRegular(skillFile, merged, priorSkill.stat);
    const soul = appendBootstrapToSoul(soulFile, snippet);
    if (soul.changed) log.write(`  wrote bootstrap block to ${soulFile}\n`);
    else log.note(`  ${soulFile} already contains caveman bootstrap`);
  } catch (error) {
    // SOUL is atomic, so failure leaves it unchanged. Roll skill write back too
    // so install never returns with only half of always-on activation present.
    try {
      const currentSkill = readRegularIfExists(skillFile);
      if (priorSkill.content === null) {
        if (currentSkill.stat) unlinkRegular(skillFile, currentSkill.stat);
        try { fs.rmdirSync(skillDir); } catch (_) {}
      } else {
        atomicWriteRegular(skillFile, priorSkill.content, currentSkill.stat);
      }
    } catch (_) { /* preserve original install error */ }
    throw error;
  }
  log.write(`  installed: ${skillFile}\n`);

  return { ok: true };
}

function uninstallOpenclaw({ workspace, dryRun = false, log = noopLog() } = {}) {
  const ws = workspace || resolveWorkspace();
  const skillDir = path.join(ws, 'skills', SKILL_NAME);
  const soulFile = path.join(ws, SOUL_FILE);

  let touched = false;

  const hasSoul = fs.existsSync(soulFile);
  const hasSkill = fs.existsSync(skillDir);
  if (dryRun) {
    if (hasSoul) { log.note(`  would strip caveman block from ${soulFile}`); touched = true; }
    if (hasSkill) { log.note(`  would remove ${skillDir}/`); touched = true; }
    return { ok: true, touched };
  }

  const soulSnapshot = readRegularIfExists(soulFile);
  let stagedSkill = null;
  try {
    if (hasSkill) {
      ensureRealDirectory(path.join(ws, 'skills'));
      ensureRealDirectory(skillDir);
      stagedSkill = path.join(path.dirname(skillDir), `.${SKILL_NAME}.remove.${process.pid}.${cryptoRandom()}`);
      fs.renameSync(skillDir, stagedSkill);
    }
    if (hasSoul) {
      const r = stripBootstrapFromSoul(soulFile);
      if (r.changed) {
        log.note(r.removed ? `  removed ${soulFile}` : `  stripped caveman block from ${soulFile}`);
        touched = true;
      }
    }
    if (stagedSkill) {
      fs.rmSync(stagedSkill, { recursive: true, force: true });
      log.note(`  removed ${skillDir}`);
      touched = true;
    }
  } catch (error) {
    try { restoreRegularSnapshot(soulFile, soulSnapshot); } catch (_) {}
    try {
      if (stagedSkill && fs.existsSync(stagedSkill) && !fs.existsSync(skillDir)) fs.renameSync(stagedSkill, skillDir);
    } catch (_) {}
    throw error;
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
