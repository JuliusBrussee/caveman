#!/usr/bin/env node
// caveman — Claude Code SessionStart activation hook
//
// Runs on every session start:
//   1. Writes flag file at ~/.claude/.caveman-active (statusline reads this)
//   2. Emits caveman ruleset as hidden SessionStart context
//   3. Detects missing statusline config and emits setup nudge

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode } = require('./caveman-config');

const claudeDir = path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
const settingsPath = path.join(claudeDir, 'settings.json');

const mode = getDefaultMode();

// "off" mode — skip activation entirely, don't write flag or emit rules
if (mode === 'off') {
  try { fs.unlinkSync(flagPath); } catch (e) {}
  process.stdout.write('OK');
  process.exit(0);
}

// 1. Write flag file
try {
  fs.mkdirSync(path.dirname(flagPath), { recursive: true });
  fs.writeFileSync(flagPath, mode);
} catch (e) {
  // Silent fail -- flag is best-effort, don't block the hook
}

// 2. Emit full caveman ruleset, filtered to the active intensity level.
//    The old 2-sentence summary was too weak — models drifted back to verbose
//    mid-conversation, especially after context compression pruned it away.
//    Full rules with examples anchor behavior much more reliably.

// Map mode to the intensity table row and matching example lines
const INTENSITY = {
  lite:          { label: 'lite',         what: 'No filler/hedging. Keep articles + full sentences. Professional but tight' },
  full:          { label: 'full',         what: 'Drop articles, fragments OK, short synonyms. Classic caveman' },
  ultra:         { label: 'ultra',        what: 'Abbreviate (DB/auth/config/req/res/fn/impl), strip conjunctions, arrows for causality (X → Y), one word when one word enough' },
  'wenyan-lite': { label: 'wenyan-lite',  what: 'Semi-classical. Drop filler/hedging but keep grammar structure, classical register' },
  wenyan:        { label: 'wenyan-full',  what: 'Maximum classical terseness. Fully 文言文. 80-90% character reduction. Classical sentence patterns, verbs precede objects, subjects often omitted, classical particles (之/乃/為/其)' },
  'wenyan-full': { label: 'wenyan-full',  what: 'Maximum classical terseness. Fully 文言文. 80-90% character reduction. Classical sentence patterns, verbs precede objects, subjects often omitted, classical particles (之/乃/為/其)' },
  'wenyan-ultra':{ label: 'wenyan-ultra', what: 'Extreme abbreviation while keeping classical Chinese feel. Maximum compression, ultra terse' },
};

const EXAMPLES = {
  lite:          [
    'lite: "Your component re-renders because you create a new object reference each render. Wrap it in `useMemo`."',
    'lite: "Connection pooling reuses open connections instead of creating new ones per request. Avoids repeated handshake overhead."',
  ],
  full:          [
    'full: "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."',
    'full: "Pool reuse open DB connections. No new connection per request. Skip handshake overhead."',
  ],
  ultra:         [
    'ultra: "Inline obj prop → new ref → re-render. `useMemo`."',
    'ultra: "Pool = reuse DB conn. Skip handshake → fast under load."',
  ],
  'wenyan-lite': [
    'wenyan-lite: "組件頻重繪，以每繪新生對象參照故。以 useMemo 包之。"',
  ],
  wenyan:        [
    'wenyan-full: "物出新參照，致重繪。useMemo .Wrap之。"',
    'wenyan-full: "池reuse open connection。不每req新開。skip handshake overhead。"',
  ],
  'wenyan-full': [
    'wenyan-full: "物出新參照，致重繪。useMemo .Wrap之。"',
    'wenyan-full: "池reuse open connection。不每req新開。skip handshake overhead。"',
  ],
  'wenyan-ultra':[
    'wenyan-ultra: "新參照→重繪。useMemo Wrap。"',
    'wenyan-ultra: "池reuse conn。skip handshake → fast。"',
  ],
};

const intensity = INTENSITY[mode] || INTENSITY.full;
const examples = EXAMPLES[mode] || EXAMPLES.full;

let output = `CAVEMAN MODE ACTIVE — level: ${intensity.label}

Respond terse like smart caveman. All technical substance stay. Only fluff die.

## Persistence

ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode".

Current level: **${intensity.label}**. Switch: \`/caveman lite|full|ultra\`.

## Rules

Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.

Pattern: \`[thing] [action] [reason]. [next step].\`

Not: "Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by..."
Yes: "Bug in auth middleware. Token expiry check use \`<\` not \`<=\`. Fix:"

## Active Intensity

| Level | What change |
|-------|------------|
| **${intensity.label}** | ${intensity.what} |

Examples at this level:
${examples.map(e => '- ' + e).join('\n')}

## Auto-Clarity

Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.

Example — destructive op:
> **Warning:** This will permanently delete all rows in the \`users\` table and cannot be undone.
> Caveman resume. Verify backup exist first.

## Boundaries

Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.`;

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
      "To enable, add this to ~/.claude/settings.json: " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
