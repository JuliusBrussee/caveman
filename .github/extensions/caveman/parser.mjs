// caveman — shared mode/command parser (ESM)
//
// Used by Copilot CLI extension and parser-focused tests.
// Claude Code hook installs keep compatible CJS logic because standalone
// hooks do not ship this ESM module.

export const VALID_MODES = [
  'off', 'lite', 'full', 'ultra',
  'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra',
];

export const INDEPENDENT_MODES = ['commit', 'review', 'compress'];

export const ONE_SHOT_SKILLS = ['commit', 'review', 'compress', 'help'];

const STOP_PATTERN = /^\s*(stop caveman|normal mode)\s*$/i;

/**
 * Parse a user prompt for caveman control commands.
 *
 * @param {string} prompt — raw user prompt text
 * @param {string} [defaultMode='full'] — fallback when bare `/caveman` used
 * @returns {{ type: 'mode'|'oneshot'|'stop'|null, mode?: string, skill?: string }}
 */
export function parseSlashCommand(prompt, defaultMode = 'full') {
  const trimmed = (prompt || '').trim();
  const lower = trimmed.toLowerCase();

  // Stop detection takes priority — even if prompt also has /caveman
  if (STOP_PATTERN.test(lower)) {
    return { type: 'stop' };
  }

  const parts = lower.split(/\s+/);
  const cmd = parts[0];
  const arg = parts[1] || '';

  const isSlashModeCommand = cmd === '/caveman' || cmd === '/caveman:caveman';
  const isPlainModeCommand = cmd === 'caveman';

  // One-shot skill commands: /caveman-commit, /caveman-review, /caveman-compress, /caveman-help
  if (cmd === '/caveman-commit') return { type: 'oneshot', skill: 'commit' };
  if (cmd === '/caveman-review') return { type: 'oneshot', skill: 'review' };
  if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') return { type: 'oneshot', skill: 'compress' };
  if (cmd === '/caveman-help') return { type: 'oneshot', skill: 'help' };
  if (cmd === 'caveman-commit') return { type: 'oneshot', skill: 'commit' };
  if (cmd === 'caveman-review') return { type: 'oneshot', skill: 'review' };
  if (cmd === 'caveman-compress') return { type: 'oneshot', skill: 'compress' };
  if (cmd === 'caveman-help') return { type: 'oneshot', skill: 'help' };

  if (!isSlashModeCommand && !isPlainModeCommand) {
    return { type: null };
  }

  if (isPlainModeCommand && ONE_SHOT_SKILLS.includes(arg)) {
    return { type: 'oneshot', skill: arg };
  }

  // Mode switching: /caveman [level]
  if (isSlashModeCommand || isPlainModeCommand) {
    if (arg === 'lite') return { type: 'mode', mode: 'lite' };
    if (arg === 'full') return { type: 'mode', mode: 'full' };
    if (arg === 'ultra') return { type: 'mode', mode: 'ultra' };
    if (arg === 'wenyan-lite') return { type: 'mode', mode: 'wenyan-lite' };
    if (arg === 'wenyan' || arg === 'wenyan-full') return { type: 'mode', mode: 'wenyan' };
    if (arg === 'wenyan-ultra') return { type: 'mode', mode: 'wenyan-ultra' };
    if (arg === 'off') return { type: 'mode', mode: 'off' };

    // Bare command — use default mode
    if (!arg) return { type: 'mode', mode: defaultMode };

    // Slash variants can safely fall back; plain-text variants should avoid
    // hijacking normal prose like "caveman architecture overview".
    if (isSlashModeCommand) return { type: 'mode', mode: defaultMode };
    return { type: null };
  }

  return { type: null };
}

/**
 * Check if prompt contains a stop/deactivation command.
 */
export function isStopCommand(prompt) {
  return STOP_PATTERN.test(prompt || '');
}
