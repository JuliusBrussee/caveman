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

// Parse mode into base intensity and optional language
// e.g. "full-es" → { baseMode: "full", lang: "es" }
//      "lite-en" → { baseMode: "lite", lang: "en" }
//      "full"    → { baseMode: "full", lang: null }
//      "wenyan-full-es" → { baseMode: "wenyan-full", lang: "es" }
const KNOWN_LANGS = new Set(['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh']);

function parseMode(rawMode) {
  const parts = rawMode.split('-');
  const lastPart = parts[parts.length - 1];
  if (KNOWN_LANGS.has(lastPart) && parts.length > 1) {
    return {
      baseMode: parts.slice(0, -1).join('-'),
      lang: lastPart,
    };
  }
  return { baseMode: rawMode, lang: null };
}

const { baseMode, lang } = parseMode(mode);

// Normalize wenyan alias for baseMode
const normalizedBase = baseMode === 'wenyan' ? 'wenyan-full' : baseMode;

// The example label to match: "full-es", "lite-en", or just "full" if no lang
const exampleLabel = lang ? `${normalizedBase}-${lang}` : normalizedBase;

// Modes that have their own independent skill files — not caveman intensity levels.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

if (INDEPENDENT_MODES.has(normalizedBase)) {
  process.stdout.write('CAVEMAN MODE ACTIVE — level: ' + normalizedBase + '. Behavior defined by /caveman-' + normalizedBase + ' skill.');
  process.exit(0);
}

// Read SKILL.md — the single source of truth for caveman behavior.
// Plugin installs: __dirname = <plugin_root>/hooks/, SKILL.md at <plugin_root>/skills/caveman/SKILL.md
// Standalone installs: __dirname = ~/.claude/hooks/, SKILL.md won't exist — falls back to hardcoded rules.
let skillContent = '';
try {
  skillContent = fs.readFileSync(
    path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
  );
} catch (e) { /* standalone install — will use fallback below */ }

let output;

if (skillContent) {
  // Strip YAML frontmatter
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');

  // Filter intensity table and examples for the active mode + language
  const filtered = body.split('\n').reduce((acc, line) => {
    // Intensity table rows: | **level** | ... — filter by baseMode
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      if (tableRowMatch[1] === normalizedBase) {
        acc.push(line);
      }
      return acc;
    }

    // Example lines: "- label: ..." — filter by exact exampleLabel
    // e.g. "- full-es:" matches when mode is full-es
    // Falls back to baseMode label if no lang-specific example exists
    const exampleMatch = line.match(/^- (\S+?):\s/);
    if (exampleMatch) {
      const label = exampleMatch[1];
      if (label === exampleLabel) {
        // Exact lang+mode match — include, strip the label prefix for cleaner output
        acc.push(line);
      }
      // Drop all other example lines (wrong mode or wrong lang)
      return acc;
    }

    acc.push(line);
    return acc;
  }, []);

  const langNote = lang ? ` | lang: ${lang}` : '';
  output = `CAVEMAN MODE ACTIVE — level: ${normalizedBase}${langNote}\n\n` + filtered.join('\n');
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  const langNote = lang ? ` | lang: ${lang}` : '';
  const langRules = lang === 'es'
    ? 'Eliminar: artículos (un/una/el/la/los/las), relleno (básicamente/realmente/simplemente), formalidades, vacilaciones. Responder siempre en castellano.'
    : lang === 'en'
      ? 'Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging. Always respond in English.'
      : 'Drop: articles, filler, pleasantries, hedging. Match the user\'s language.';

  output =
    `CAVEMAN MODE ACTIVE — level: ${normalizedBase}${langNote}\n\n` +
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
    '## Persistence\n\n' +
    'ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. ' +
    'Off only: "stop caveman" / "normal mode" / "modo normal".\n\n' +
    `Current level: **${normalizedBase}**${lang ? `. Language: **${lang}**` : ''}. ` +
    'Switch: `/caveman lite|full|ultra`. Language: `/caveman es|en`.\n\n' +
    '## Rules\n\n' +
    langRules + '\n\n' +
    'Fragments OK. Short synonyms. Technical terms exact. Code blocks unchanged.\n\n' +
    'Pattern: `[thing] [action] [reason]. [next step].`\n\n' +
    '## Auto-Clarity\n\n' +
    'Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user confused. Resume after.\n\n' +
    '## Boundaries\n\n' +
    'Code/commits/PRs: write normal. "stop caveman" / "normal mode" / "modo normal": revert. Level + language persist until changed or session end.';
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
      "(e.g. [CAVEMAN], [CAVEMAN:ULTRA], [CAVEMAN:ES]). It is not configured yet. " +
      "To enable, add this to ~/.claude/settings.json: " +
      statusLineSnippet + " " +
      "Proactively offer to set this up for the user on first interaction.";
  }
} catch (e) {
  // Silent fail — don't block session start over statusline detection
}

process.stdout.write(output);
