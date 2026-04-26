#!/usr/bin/env node
// caveman — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Writes flag file at $CLAUDE_CONFIG_DIR/.caveman-active (statusline reads this)
//   2. Emits caveman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
const settingsPath = path.join(claudeDir, 'settings.json');

const mode = getDefaultMode();

// "off" mode — skip activation entirely, don't write flag or emit rules
if (mode === 'off') {
  try { fs.unlinkSync(flagPath); } catch (e) {}
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file (symlink-safe)
safeWriteFlag(flagPath, mode);

// 2. Emit full caveman ruleset, filtered to the active intensity level.
//    The old 2-sentence summary was too weak — models drifted back to verbose
//    mid-conversation, especially after context compression pruned it away.
//    Full rules with examples anchor behavior much more reliably.
//
//    Reads SKILL.md at runtime so edits to the source of truth propagate
//    automatically — no hardcoded duplication to go stale.

// Modes that have their own independent skill files — not caveman intensity levels.
// For these, emit a short activation line; the skill itself handles behavior.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

if (INDEPENDENT_MODES.has(mode)) {
  process.stdout.write('CAVEMAN MODE ACTIVE — level: ' + mode + '. Behavior defined by /caveman-' + mode + ' skill.');
  process.exit(0);
}

// Resolve canonical labels.
// Flag file stores short names (wenyan, hangeul) for statusline readability.
// We expand to canonical full names for the system prompt label.
let modeLabel = mode;
if (mode === 'wenyan') modeLabel = 'wenyan-full';
if (mode === 'hangeul' || mode === 'korean' || mode === 'ko') modeLabel = 'hangeul-full';

// Read SKILL.md — the single source of truth for caveman behavior.
// Multiple lookup paths to handle plugin installs, cloned repos, and standalone installs.
let skillContent = '';
try {
  skillContent = fs.readFileSync(
    path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
  );
} catch (e) { /* plugin install — will try other paths below */ }

// For standalone + cloned repo: also try repo-root-relative path
if (!skillContent) {
  try {
    skillContent = fs.readFileSync(
      path.join(__dirname, '..', '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
    );
  } catch (e) { /* still not found — will use fallback below */ }
}

let output;

if (skillContent) {
  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

  // Filter intensity table: keep header rows + only the active level's row
  const filtered = body.split('\n').reduce((acc, line) => {
    // Intensity table rows start with | **level** |
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      // Keep only the active level's row (and always keep header/separator)
      if (tableRowMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    // Example lines start with "- level:" — keep only lines matching active level
    const exampleMatch = line.match(/^- (\S+?):\s/);
    if (exampleMatch) {
      if (exampleMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    acc.push(line);
    return acc;
  }, []);

  output = 'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' + filtered.join('\n');
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  // This is the minimum viable ruleset — better than nothing.
  output =
    'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
    '## Persistence\n\n' +
    'ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode".\n\n' +
    'Current level: **' + modeLabel + '**. Switch: `/caveman lite|full|ultra`.\n\n' +
    '## Rules\n\n' +
    'Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. ' +
    'Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.\n\n' +
    'Pattern: `[thing] [action] [reason]. [next step].`\n\n' +
    'Not: "Sure! I\'d be happy to help you with that. The issue you\'re experiencing is likely caused by..."\n' +
    'Yes: "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"\n\n' +
    '## Auto-Clarity\n\n' +
    'Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.\n\n' +
    '## Boundaries\n\n' +
    'Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.';
}

// Load language-specific compression rules (hook-based injection).
// Runs AFTER the main output construction — applies to BOTH plugin install
// and standalone fallback paths. English users: this block is skipped.
// Multiple rulesDir tries: plugin (<plugin_root>/rules), cloned repo (<repo>/rules),
// standalone hooks dir (<claude_config>/hooks/../rules).
const resolveRulesDir = () => {
  for (const rel of ['..', '../..']) {
    const candidate = path.join(__dirname, rel, 'rules');
    try {
      if (fs.existsSync(candidate)) return candidate;
    } catch (e) { /* ignore */ }
  }
  return path.join(__dirname, '..', 'rules'); // default, will fail silently
};

if (modeLabel.startsWith('hangeul')) {
  const rulesDir = resolveRulesDir();
  const langRulesPath = path.join(rulesDir, 'hangeul-compression.md');
  try {
    const langRules = fs.readFileSync(langRulesPath, 'utf8');
    output += '\n\n' + langRules;
  } catch (e) {
    // Fallback: inline minimal Korean rules for standalone installs
    output += '\n\n# Korean Compression Rules (minimal)\n' +
      'ACTIVE ONLY when `/caveman hangeul` (or `korean`, `ko`) is set.\n' +
      'Drop filler (사실/그냥/진짜/기본적으로), pleasantries (~드리겠습니다), hedging (~것 같습니다). ' +
      'Use 반말. Drop particles (은/는/이/가). Use noun endings (~함/~됨). Fragments OK.\n';
  }
} else if (modeLabel.startsWith('wenyan')) {
  const rulesDir = resolveRulesDir();
  const langRulesPath = path.join(rulesDir, 'wenyan-compression.md');
  try {
    const langRules = fs.readFileSync(langRulesPath, 'utf8');
    output += '\n\n' + langRules;
  } catch (e) {
    // Fallback: inline minimal wenyan rules for standalone installs
    output += '\n\n# Classical Chinese Rules (minimal)\n' +
      'Respond in Classical Chinese (文言文). Maximum terseness. ' +
      'Classical sentence patterns. Verbs precede objects. Subjects often omitted.\n';
  }
}

// 3. Detect missing statusline config — nudge Claude to help set it up
try {
  let hasStatusline = false;
  if (fs.existsSync(settingsPath)) {
    const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
    if (settings.statusLine) {
      hasStatusline = true;
    }
  }

  if (!hasStatusline) {
    const isWindows = process.platform === 'win32';
    const scriptName = isWindows ? 'caveman-statusline.ps1' : 'caveman-statusline.sh';
    const scriptPath = path.join(__dirname, scriptName);
    const command = isWindows
      ? `powershell -ExecutionPolicy Bypass -File "${scriptPath}"`
      : `bash "${scriptPath}"`;
    const statusLineSnippet =
      '"statusLine": { "type": "command", "command": ' + JSON.stringify(command) + ' }';
    output += "\n\n" +
      "STATUSLINE SETUP NEEDED: The caveman plugin includes a statusline badge showing active mode " +
      "(e.g. [CAVEMAN], [CAVEMAN:ULTRA]). It is not configured yet. " +
      "To enable, add this to " + path.join(claudeDir, 'settings.json') + ": " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
