// caveman — shared mode-change parser
//
// Single source of truth for interpreting user prompts as caveman mode
// changes. Used by the Claude Code tracker (caveman-mode-tracker.js) and
// the opencode plugin (src/plugins/opencode/plugin.js) so both stay in sync.
//
// Returns:
//   A valid mode string (including 'off') — caller should apply the change.
//   null                            — prompt does not change state.

const { getDefaultMode, VALID_MODES } = require('./caveman-config');

const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

function parseModeChange(promptRaw, options = {}) {
  let prompt = (promptRaw || '').trim();
  if (!prompt) return null;

  // opencode's non-interactive run path wraps user messages in literal
  // quote characters ("/caveman ultra") — unwrap so the branches still match.
  if (options.unwrapQuotes) {
    const wrapped = /^(["'`])([\s\S]*)\1$/.exec(prompt);
    if (wrapped) prompt = wrapped[2].trim();
  }

  prompt = prompt.toLowerCase();
  if (!prompt) return null;

  // Natural-language deactivation — checked before activation so
  // "stop talking like caveman" doesn't trip the activation regex.
  if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
      /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
      /\bnormal mode\b/i.test(prompt)) {
    return 'off';
  }

  // Expanded /caveman command template (opencode plugin path).
  // opencode replaces a typed "/caveman <level>" with the command file's
  // body ("Activate caveman mode: $ARGUMENTS ...") before chat.message
  // fires — recover the level argument from the template's first line.
  if (options.expandedTpl) {
    const tpl = /^activate caveman mode:[ \t]*(\S*)/.exec(prompt);
    if (tpl) {
      const arg = tpl[1] || '';
      if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
      if (arg === 'wenyan-full') return 'wenyan';
      if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return arg;
      return null;  // Unknown arg — leave flag untouched
    }
  }

  // Natural-language activation and brevity triggers (promised in README).
  if (/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
      /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt) ||
      /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b/i.test(prompt)) {
    const mode = getDefaultMode();
    return mode === 'off' ? null : mode;
  }

  // Slash-command parsing.
  if (prompt.startsWith('/caveman')) {
    const parts = prompt.split(/\s+/);
    const cmd = parts[0];
    const arg = parts[1] || '';

    if (cmd === '/caveman-commit')   return 'commit';
    if (cmd === '/caveman-review')   return 'review';
    if (cmd === '/caveman-compress') return 'compress';

    if (cmd === '/caveman') {
      if (!arg)                                     return getDefaultMode();
      if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
      if (arg === 'wenyan-full')                    return 'wenyan';
      if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return arg;
      return null;  // Unknown arg — leave flag untouched
    }
  }

  return null;
}

module.exports = { parseModeChange, INDEPENDENT_MODES };
