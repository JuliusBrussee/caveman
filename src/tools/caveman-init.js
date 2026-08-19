#!/usr/bin/env node
// caveman init — drop the always-on caveman activation rule into a target
// repo for every IDE agent we support. Idempotent. Safe to re-run.
//
// Usage:
//   node src/tools/caveman-init.js [target-dir] [--dry-run] [--force] [--only <agent>]
//   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/tools/caveman-init.js | node - [args]
//
// Without args, runs in cwd. Generates the rule files for Cursor, Windsurf,
// Cline, Copilot, and AGENTS.md. Does NOT modify CLAUDE.md or compress
// existing memory files — that's the job of `/caveman:compress`.

const fs = require('fs');
const path = require('path');

// Embedded so the tool works standalone (npx-style) without the src/rules/ dir.
// Mirrors src/rules/caveman-activate.md verbatim — keep these in sync.
const RULE_BODY = `Respond terse like smart caveman. All technical substance stay. Only fluff die.

Rules:
- Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging
- Fragments OK. Short synonyms. Technical terms exact. Code unchanged.
- Pattern: [thing] [action] [reason]. [next step].
- Not: "Sure! I'd be happy to help you with that."
- Yes: "Bug in auth middleware. Fix:"

Switch level: /caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra
Stop: "stop caveman" or "normal mode"

Auto-Clarity: drop caveman for security warnings, irreversible actions, user confused. Resume after.

Boundaries: code/commits/PRs written normal.
`;

const SENTINEL = 'Respond terse like smart caveman';

// Marker fence for blocks appended to files the user also authors (AGENTS.md,
// copilot-instructions.md). Same convention as bin/lib/openclaw.js (SOUL.md)
// and the opencode AGENTS.md path in bin/install.js. Without it our block can
// be neither refreshed on upgrade nor removed on uninstall — it just fossilizes
// in the user's repo.
const FENCE_BEGIN = '<!-- caveman-begin -->';
const FENCE_END = '<!-- caveman-end -->';

function countOccurrences(haystack, needle) {
  let n = 0;
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + needle.length)) n++;
  return n;
}

function fencedBlock(ruleBody) {
  return FENCE_BEGIN + '\n' + ruleBody.trimEnd() + '\n' + FENCE_END + '\n';
}

// Atomic write: these land on tracked files in the user's repo, so a partial
// write from an interrupted run would corrupt a file they have committed.
function writeAtomic(fullPath, content) {
  const dir = path.dirname(fullPath);
  fs.mkdirSync(dir, { recursive: true });
  const tmp = path.join(dir, `.${path.basename(fullPath)}.${process.pid}.tmp`);
  try {
    fs.writeFileSync(tmp, content, { mode: 0o644 });
    // rename onto an existing target needs DELETE access and no other handle
    // without FILE_SHARE_DELETE — OneDrive/Dropbox sync and AV scanners hold
    // exactly those, and the plain writeFileSync this replaced did not. Retry
    // brief contention rather than failing the whole init run, same policy as
    // safeWriteFlag() in caveman-config.js.
    for (let attempt = 0; ; attempt++) {
      try {
        fs.renameSync(tmp, fullPath);
        break;
      } catch (error) {
        const transient = error.code === 'EPERM' || error.code === 'EBUSY' ||
                          error.code === 'EACCES' || error.code === 'EEXIST';
        if (!transient || attempt >= 2) throw error;
        Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10 * (attempt + 1));
      }
    }
  } catch (error) {
    try { fs.unlinkSync(tmp); } catch (_) {}
    throw error;
  }
}

// OpenClaw is a global workspace tool (not per-repo) and needs two write
// targets — a skill folder + a SOUL.md bootstrap block. The shared helper
// lives at bin/lib/openclaw.js; we require it lazily so caveman-init.js
// keeps working when run standalone (curl|node) without the helper on disk.
function loadOpenclawHelper() {
  try {
    return require(path.join(__dirname, '..', '..', 'bin', 'lib', 'openclaw.js'));
  } catch (_) { return null; }
}

const AGENTS = [
  { id: 'cursor',   file: '.cursor/rules/caveman.mdc',
    frontmatter: '---\ndescription: "Caveman mode — terse communication that preserves technical substance and exact code/errors"\nalwaysApply: true\n---\n\n',
    mode: 'replace' },
  { id: 'windsurf', file: '.windsurf/rules/caveman.md',
    frontmatter: '---\ntrigger: always_on\n---\n\n',
    mode: 'replace' },
  { id: 'cline',    file: '.clinerules/caveman.md',
    frontmatter: '',
    mode: 'replace' },
  { id: 'copilot',  file: '.github/copilot-instructions.md',
    frontmatter: '',
    mode: 'append' },
  { id: 'opencode', file: '.opencode/AGENTS.md',
    frontmatter: '',
    mode: 'append' },
  { id: 'agents',   file: 'AGENTS.md',
    frontmatter: '',
    mode: 'append' },
  // OpenClaw — global workspace install, not per-repo. The `installer`
  // callback escape hatch bypasses the file/frontmatter/mode triple and
  // hands off to the shared helper. `description` is what `--help` prints.
  { id: 'openclaw', description: '~/.openclaw/workspace/{skills/caveman/, SOUL.md}',
    installer: 'openclaw' },
];

function loadRuleBody() {
  // Prefer the in-repo source-of-truth when available.
  try {
    const local = path.join(__dirname, '..', 'rules', 'caveman-activate.md');
    if (fs.existsSync(local)) return fs.readFileSync(local, 'utf8').trimEnd() + '\n';
  } catch (e) {}
  return RULE_BODY;
}

function processAgent(agent, targetDir, ruleBody, opts) {
  if (agent.installer === 'openclaw') {
    return processOpenclaw(opts);
  }
  const fullPath = path.join(targetDir, agent.file);
  const exists = fs.existsSync(fullPath);
  const isAppend = agent.mode === 'append';
  // Append targets are shared with the user, so our contribution is fenced.
  // Replace targets are single-purpose files we own outright.
  const desired = isAppend ? fencedBlock(ruleBody) : agent.frontmatter + ruleBody;

  if (!exists) {
    if (!opts.dryRun) writeAtomic(fullPath, desired);
    return { status: 'added', label: '+' };
  }

  const existing = fs.readFileSync(fullPath, 'utf8');

  if (isAppend) {
    const begin = existing.indexOf(FENCE_BEGIN);
    const end = begin === -1 ? -1 : existing.indexOf(FENCE_END, begin);
    // Markers must be exactly one matched pair. An orphan BEGIN (truncated
    // write, bad merge) would otherwise pair with a LATER block's END and the
    // refresh would replace every byte between them — the user's own content
    // included. Damaged markers are damage, not a fence: report and write
    // nothing, so a second run cannot compound it (#596's chain, per-repo).
    const paired = countOccurrences(existing, FENCE_BEGIN) === 1
      && countOccurrences(existing, FENCE_END) === 1
      && begin !== -1 && end > begin;
    if (!paired && (existing.includes(FENCE_BEGIN) || existing.includes(FENCE_END))) {
      return { status: 'skipped-damaged-fence', label: '!' };
    }
    if (paired) {
      // Refresh in place. A presence-only check meant a rule-body change never
      // reached anyone already initialized — the block went stale forever.
      const span = existing.slice(begin, end + FENCE_END.length + 1);
      if (span === desired) return { status: 'skipped-already-installed', label: '=' };
      if (!opts.dryRun) {
        writeAtomic(fullPath, existing.slice(0, begin) + desired
          + existing.slice(end + FENCE_END.length).replace(/^\n/, ''));
      }
      return { status: 'refreshed', label: '~' };
    }
    if (existing.includes(SENTINEL)) {
      // Pre-fence install. Leave it: we cannot tell our bytes from the user's
      // edits around them, and silently rewriting a tracked repo file is worse
      // than a stale block. Re-run after deleting the old block to re-fence.
      return { status: 'skipped-legacy-unfenced', label: '?' };
    }
    if (!opts.dryRun) {
      const sep = existing.endsWith('\n\n') ? '' : (existing.endsWith('\n') ? '\n' : '\n\n');
      writeAtomic(fullPath, existing + sep + desired);
    }
    return { status: 'appended', label: '~' };
  }

  // Replace-mode files carry no marker fence, so a sentinel hit cannot tell
  // "our older ruleset" from "a user file that happens to quote the rule".
  // Refreshing on a guess would clobber their content, so skip and let the
  // existing --force flag be the deliberate refresh path.
  if (existing.includes(SENTINEL)) {
    return { status: 'skipped-already-installed', label: '=' };
  }

  if (opts.force) {
    if (!opts.dryRun) writeAtomic(fullPath, desired);
    return { status: 'overwritten', label: '!' };
  }

  return { status: 'skipped-exists', label: '?' };
}

function processOpenclaw(opts) {
  const helper = loadOpenclawHelper();
  if (!helper) {
    return {
      status: 'unsupported-standalone',
      label: 'x',
      detail: '~/.openclaw/workspace (helper unavailable in standalone curl|node mode — use `npx -y github:JuliusBrussee/caveman -- --only openclaw`)',
    };
  }
  const repoRoot = path.resolve(__dirname, '..', '..');
  const log = {
    write: (_) => {},
    note: (_) => {},
    warn: (_) => {},
  };
  const r = helper.installOpenclaw({
    workspace: process.env.OPENCLAW_WORKSPACE || undefined,
    repoRoot,
    dryRun: opts.dryRun,
    force: opts.force,
    log,
  });
  if (!r.ok) {
    return { status: 'skipped-' + (r.reason || 'failed'), label: '?', detail: helper.resolveWorkspace ? helper.resolveWorkspace() : '~/.openclaw/workspace' };
  }
  if (r.dryRun) return { status: 'would-add', label: '+', detail: helper.resolveWorkspace() };
  return { status: 'installed', label: '+', detail: helper.resolveWorkspace() };
}

function parseArgs(argv) {
  const opts = { dryRun: false, force: false, only: null, target: process.cwd() };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--force' || a === '-f') opts.force = true;
    else if (a === '--only') { opts.only = argv[++i]; }
    else if (a === '-h' || a === '--help') opts.help = true;
    else if (!a.startsWith('-')) opts.target = path.resolve(a);
  }
  // A missing or misspelled --only value used to fall through as falsy and
  // install for EVERY agent, or match nothing and report a silent all-zero
  // run. Both look like success. Fail loudly instead.
  if (argv.includes('--only')) {
    const known = AGENTS.map(a => a.id);
    if (!opts.only || !known.includes(opts.only)) {
      console.error(`caveman-init: --only requires one of: ${known.join(', ')}`);
      process.exit(2);
    }
  }
  return opts;
}

function help() {
  console.log(`caveman init — drop always-on caveman rule into a target repo

Usage: caveman-init.js [target-dir] [--dry-run] [--force] [--only <agent>]

Defaults to current working directory. Idempotent — safe to re-run.

Targets installed:
${AGENTS.map(a => `  ${a.id.padEnd(10)} ${a.file || a.description || ''}`).join('\n')}

Flags:
  --dry-run   show what would change, do not write
  --force     overwrite existing rule files (default: skip)
  --only <id> only install for one agent (id from list above)
`);
}

function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) { help(); return; }

  console.log(`🪨 caveman init — ${opts.target}${opts.dryRun ? ' (dry run)' : ''}\n`);

  const ruleBody = loadRuleBody();
  const counts = { added: 0, appended: 0, refreshed: 0, overwritten: 0, skipped: 0 };

  for (const agent of AGENTS) {
    if (opts.only && opts.only !== agent.id) continue;
    const result = processAgent(agent, opts.target, ruleBody, opts);
    const target = agent.file || result.detail || agent.description || agent.id;
    console.log(`  ${result.label} ${target} (${result.status})`);
    if (result.status === 'added' || result.status === 'installed' || result.status === 'would-add') counts.added++;
    else if (result.status === 'appended') counts.appended++;
    else if (result.status === 'refreshed') counts.refreshed++;
    else if (result.status === 'overwritten') counts.overwritten++;
    else counts.skipped++;
  }

  console.log(`\n${counts.added} added, ${counts.appended} appended, ` +
              `${counts.refreshed} refreshed, ` +
              `${counts.overwritten} overwritten, ${counts.skipped} skipped`);
  if (opts.dryRun) console.log('(dry run — no files were written)');
}

// Run when executed directly AND when piped via `curl … | node -` (the
// documented standalone path, #603): under stdin execution require.main is
// undefined and module.id is '[stdin]', so the classic guard alone silently
// no-ops with exit code 0 — the worst kind of failure.
if (require.main === module || (!require.main && module.id === '[stdin]')) main();

module.exports = { processAgent, loadRuleBody, AGENTS, SENTINEL, RULE_BODY };
