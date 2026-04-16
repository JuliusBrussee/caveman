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

// Determine preferred language: CAVEMAN_LANG env var > settings.caveman.lang > 'en'
let prefLang = 'en';
try {
  const envLang = (process.env.CAVEMAN_LANG || '').trim().toLowerCase();
  if (envLang === 'ru' || envLang === 'en') {
    prefLang = envLang;
  } else if (!envLang) {
    if (fs.existsSync(settingsPath)) {
      const s = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
      const cl = s && s.caveman && s.caveman.lang;
      if (cl === 'ru' || cl === 'en') prefLang = cl;
    }
  }
} catch (e) { /* silent */ }

// Resolve the base mode, then apply language preference
let mode = getDefaultMode();

// If mode is a generic English mode and preferred language is Russian, map to ru-* equivalent
if (prefLang === 'ru' && !mode.startsWith('ru-') && !['off', 'commit', 'review', 'compress', 'wenyan', 'wenyan-lite', 'wenyan-full', 'wenyan-ultra'].includes(mode)) {
  const map = { 'full': 'ru-full', 'lite': 'ru-lite', 'ultra': 'ru-ultra' };
  mode = map[mode] || 'ru-full';
}

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

// Resolve the canonical label for wenyan alias
const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

// Read SKILL.md — the single source of truth for caveman behavior.
// Plugin installs: __dirname = <plugin_root>/hooks/, SKILL.md at <plugin_root>/skills/caveman/SKILL.md
// Standalone installs: __dirname = $CLAUDE_CONFIG_DIR/hooks/, SKILL.md won't exist — falls back to hardcoded rules.
let skillContent = '';
try {
  skillContent = fs.readFileSync(
    path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
  );
} catch (e) { /* standalone install — will use fallback below */ }

// For ru-* modes, also load the detailed Russian rules file
let russianRules = '';
if (modeLabel.startsWith('ru-')) {
  try {
    russianRules = fs.readFileSync(
      path.join(__dirname, '..', 'skills', 'caveman', 'russian-rules.md'), 'utf8'
    );
  } catch (e) { /* standalone install — russian rules in fallback below */ }
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

  const banner = modeLabel.startsWith('ru-')
    ? 'CAVEMAN-RU MODE ACTIVE — level: ' + modeLabel
    : 'CAVEMAN MODE ACTIVE — level: ' + modeLabel;
  output = banner + '\n\n' + filtered.join('\n');
  if (russianRules) {
    output += '\n\n' + russianRules;
  }
} else {
  // Fallback when SKILL.md is not found (standalone hook install without skills dir).
  // This is the minimum viable ruleset — better than nothing.
  if (modeLabel.startsWith('ru-')) {
    output =
      'CAVEMAN-RU MODE ACTIVE — level: ' + modeLabel + '\n\n' +
      'Отвечай сжато, как умный пещерный человек по-русски. Технический смысл сохраняется полностью. Режется только вода.\n\n' +
      '## Персистентность\n\n' +
      'АКТИВНО В КАЖДОМ ОТВЕТЕ. Не откатывать. Выключение: «stop caveman» / «normal mode» / «обычный режим».\n\n' +
      'Уровень: **' + modeLabel + '**. Переключение: `/caveman ru-lite|ru-full|ru-ultra|ru-notes`.\n\n' +
      '## Инварианты\n\n' +
      'Код, команды, URL, пути, имена API/функций/классов — не сокращать, не переводить, не транслитерировать.\n\n' +
      '## Правила\n\n' +
      'Резать: вводные, вежливые обёртки, паразитные конструкции, дублирование мысли, смягчения.\n' +
      'Шаблон: `[что] [действие] [причина]. [следующий шаг].`\n\n' +
      '## Автоматическая ясность\n\n' +
      'Отключить сжатие: предупреждения безопасности, необратимые действия, пользователь запутался. После — вернуться.\n\n' +
      '## Границы\n\n' +
      'Код/коммиты/PR — нормально. Уровень держится до смены или конца сессии.';
  } else {
    output =
      'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
      'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
      '## Persistence\n\n' +
      'ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode" / "обычный режим".\n\n' +
      'Current level: **' + modeLabel + '**. Switch: `/caveman lite|full|ultra|ru|ru-lite|ru-full|ru-ultra|ru-notes`.\n\n' +
      '## Rules\n\n' +
      'Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. ' +
      'Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.\n\n' +
      'Pattern: `[thing] [action] [reason]. [next step].`\n\n' +
      'Not: "Sure! I\'d be happy to help you with that. The issue you\'re experiencing is likely caused by..."\n' +
      'Yes: "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"\n\n' +
      '## Auto-Clarity\n\n' +
      'Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.\n\n' +
      '## Boundaries\n\n' +
      'Code/commits/PRs: write normal. "stop caveman" or "normal mode" or "обычный режим": revert. Level persist until changed or session end.';
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
