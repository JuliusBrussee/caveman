#!/usr/bin/env node
// caveman — Codex SessionStart activation hook

const fs = require('fs');
const path = require('path');
const {
  getCodexHome,
  getDefaultMode,
  safeWriteFlag,
} = require('./codex-caveman-config');

const flagPath = path.join(getCodexHome(), '.caveman-active');
const mode = getDefaultMode();
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

function filterSkillByMode(skillContent, modeLabel) {
  const body = skillContent.replace(/^---[\s\S]*?---\s*/, '');
  return body.split('\n').reduce((acc, line) => {
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      if (tableRowMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    const exampleMatch = line.match(/^- (\S+?):\s/);
    if (exampleMatch) {
      if (exampleMatch[1] === modeLabel) {
        acc.push(line);
      }
      return acc;
    }

    acc.push(line);
    return acc;
  }, []).join('\n');
}

if (mode === 'off') {
  try { fs.unlinkSync(flagPath); } catch (e) {}
  process.exit(0);
}

safeWriteFlag(flagPath, mode);

if (INDEPENDENT_MODES.has(mode)) {
  process.stdout.write(
    'CAVEMAN MODE ACTIVE — level: ' + mode +
    '. Behavior defined by $caveman-' + mode + ' skill.'
  );
  process.exit(0);
}

const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;
let output;

try {
  const skillPath = path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md');
  const skillContent = fs.readFileSync(skillPath, 'utf8');
  output = 'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
    filterSkillByMode(skillContent, modeLabel);
} catch (e) {
  output =
    'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '\n\n' +
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n' +
    'ACTIVE EVERY RESPONSE. No revert after many turns. Off only: "stop caveman" / "normal mode".\n\n' +
    'Current level: **' + modeLabel + '**. Switch: `$caveman lite|full|ultra`.\n\n' +
    'Drop articles, filler, pleasantries, hedging. Fragments OK. Technical terms exact.\n' +
    'Code/commits/PRs: write normal.';
}

process.stdout.write(output);
