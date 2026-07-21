#!/usr/bin/env node
// caveman — shared mode-change parser (#602)
//
// Single source of truth for interpreting a user prompt as a caveman mode
// change. Extracted from caveman-mode-tracker.js so the Claude Code hook and
// the opencode plugin can't drift out of sync with each other. The tracker's
// regexes are the reference behavior — every pattern below is copied from it
// verbatim; don't tighten or loosen a match here without a matching change
// (and test) on the tracker side.
//
// parseModeChange(prompt, { getDefaultMode, skipNaturalLanguage, expandedTpl, unwrapQuotes })
//   → { action: 'set', mode }  — caller should activate `mode`
//   → { action: 'clear' }      — caller should deactivate (delete the flag)
//   → null                     — prompt does not change state
//
// Options:
//   getDefaultMode      — required; () => resolved default mode string.
//                          Injected rather than required directly so the
//                          caller controls resolution (cwd, env, etc.).
//   skipNaturalLanguage — when true, skip deactivation/activation/brevity
//                          phrase matching entirely. Set this for prompt text
//                          that isn't the user's own words (e.g. a foreign
//                          slash-command envelope's <command-args>), so
//                          another command's arguments can't misfire our
//                          triggers.
//   expandedTpl         — when true, also recognize opencode's expanded
//                          command-template bodies (a typed "/caveman ultra"
//                          gets replaced by the command file's prose before
//                          this parser ever sees it). Claude Code prompts
//                          never take this shape, so Claude Code callers
//                          should leave this off.
//   unwrapQuotes        — when true, strip a single layer of matching quote
//                          characters wrapping the whole prompt (opencode's
//                          non-interactive `run` path delivers messages this
//                          way).

// Sibling require that tolerates the opencode install layout, where this
// file is copied next to a renamed `caveman-config.cjs` (the plugin dir is
// "type": "module", so a bare `.js` sibling would load as ESM). A plain
// `require('./caveman-config')` only auto-resolves the `.js` extension, so
// try that first (dev tree, standalone hook install) and fall back to the
// explicit `.cjs` name.
let cavemanConfig;
try {
  cavemanConfig = require('./caveman-config');
} catch (e) {
  cavemanConfig = require('./caveman-config.cjs');
}
const { VALID_MODES } = cavemanConfig;

// Modes handled by their own slash commands (/caveman-commit, etc.) — not
// selectable via /caveman <arg>.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

function parseModeChange(promptRaw, options) {
  options = options || {};
  const getDefaultMode = options.getDefaultMode || cavemanConfig.getDefaultMode;

  let prompt = (promptRaw || '').trim();
  if (options.unwrapQuotes) {
    const wrapped = /^(["'`])([\s\S]*)\1$/.exec(prompt);
    if (wrapped) prompt = wrapped[2].trim();
  }
  // Capture the first line before whitespace collapse. The expandedTpl
  // templates (opencode's commands/caveman.md etc.) put $ARGUMENTS at the end
  // of the first line, followed by a blank line and then fixed boilerplate
  // ("If no level given, use full. If \"off\", deactivate."). Collapsing all
  // whitespace to single spaces (below) merges an EMPTY argument directly
  // into that boilerplate, so a bare `/caveman` with no level looked like the
  // level was the word "if" and got rejected as bogus. Extract the template
  // argument from this uncollapsed first line instead.
  const firstLine = prompt.toLowerCase().split(/\r?\n/, 1)[0];
  // Collapse whitespace so phrase triggers still match multiline prompts —
  // every regex below expects a single-line prompt (#598).
  prompt = prompt.toLowerCase().replace(/\s+/g, ' ');
  if (!prompt) return null;

  // Deactivation intent — computed FIRST so "turn caveman mode off" never
  // falls through to the activation patterns (#598), and applied with the
  // highest priority: it's what the tracker's original unconditional
  // end-of-function deactivation check amounted to.
  const wantsOff = !options.skipNaturalLanguage && (
    /\b(stop|disable|deactivate|quit|exit|kill)\s+(the\s+)?caveman\b/.test(prompt) ||
    /\bcaveman(\s+mode)?\s+(off|stop|disabled?)\b/.test(prompt) ||
    /\bturn\s+off\s+(the\s+)?caveman\b/.test(prompt) ||
    // "normal mode" only as a command (prompt-initial, optionally led by a
    // switch-back verb) or with caveman context — never mid-sentence for
    // e.g. vim's normal mode ("how do I exit vim normal mode").
    /^(please\s+)?(go\s+|back\s+to\s+|switch\s+(back\s+)?to\s+|return\s+to\s+)?normal\s+mode\b/.test(prompt) ||
    (/\bnormal\s+mode\b/.test(prompt) && /\bcaveman\b/.test(prompt))
  );
  if (wantsOff) return { action: 'clear' };

  // opencode expands a typed "/caveman <level>" (and the independent-mode
  // commands) into the command file's prose before chat.message fires, so
  // the literal slash-command branch below never sees the original text.
  // Recover the level from each template's fixed prefix instead. This MUST
  // run before the generic NL-activation match below: "Activate caveman
  // mode: ultra" would otherwise trip the "activate ... caveman" trigger and
  // swallow the level, activating at the default instead (#602). Claude Code
  // prompts never take this shape, so gate behind expandedTpl (opencode-only).
  if (options.expandedTpl) {
    if (/^generate a commit message for the current staged changes\b/.test(prompt)) {
      return { action: 'set', mode: 'commit' };
    }
    if (/^review the current diff\b/.test(prompt)) {
      return { action: 'set', mode: 'review' };
    }
    if (/^compress the file at:/.test(prompt)) {
      return { action: 'set', mode: 'compress' };
    }
    const tpl = /^activate caveman mode:[ \t]*(\S*)/.exec(firstLine);
    if (tpl) {
      const arg = tpl[1] || '';
      if (!arg) {
        const mode = getDefaultMode();
        return mode === 'off' ? { action: 'clear' } : { action: 'set', mode };
      }
      if (arg === 'off' || arg === 'stop' || arg === 'disable') return { action: 'clear' };
      if (arg === 'wenyan-full') return { action: 'set', mode: 'wenyan' };
      if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return { action: 'set', mode: arg };
      return null; // unknown/bogus level — leave flag untouched (#602)
    }
  }

  if (!options.skipNaturalLanguage) {
    // Questions about caveman are not activation commands
    // ("what is caveman mode?", "does caveman lite drop articles?").
    const isQuestion =
      /^(what|whats|what's|how|why|when|where|who|does|do|did|is|are|can|could|would|should|tell me|explain)\b/.test(prompt);

    // Natural language activation (e.g. "activate caveman", "turn on caveman
    // mode", "talk like caveman"). Also brevity requests ("less tokens",
    // "be brief/terse", "fewer tokens", "shorter answers") — but not when
    // scoped to a single section ("be brief in the summary"), which is a
    // one-off instruction, not a session-wide mode switch.
    if (!isQuestion) {
      if (/\b(activate|enable|start|turn on|use|switch to|want|give me)\b[^.]{0,40}\bcaveman\b/.test(prompt) ||
          /\btalk like\b[^.]{0,40}\bcaveman\b/.test(prompt) ||
          /\bcaveman\s+mode\s+(on|please|now)\b/.test(prompt) ||
          /^caveman(\s+mode)?\s*[.!]*$/.test(prompt) ||
          /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b(?!\s+(in|for|on|about|when|during|with)\b)/.test(prompt)) {
        const mode = getDefaultMode();
        // Mirrors the tracker exactly: a configured-off default makes this a
        // no-op (leave whatever flag state already exists), NOT a clear —
        // that's only what an explicit "/caveman" bare command does.
        return mode !== 'off' ? { action: 'set', mode } : null;
      }
    }
  }

  // Match /caveman commands. Marketplace plugin installs surface commands
  // namespaced as /caveman:caveman-<name> — accept both forms for every
  // skill (#599: only compress and stats had the namespaced variant).
  if (prompt.startsWith('/caveman')) {
    const parts = prompt.split(/\s+/);
    const cmd = parts[0]; // /caveman, /caveman-commit, /caveman-review, etc.
    const arg = parts[1] || '';

    if (cmd === '/caveman-commit' || cmd === '/caveman:caveman-commit') {
      return { action: 'set', mode: 'commit' };
    }
    if (cmd === '/caveman-review' || cmd === '/caveman:caveman-review') {
      return { action: 'set', mode: 'review' };
    }
    if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
      return { action: 'set', mode: 'compress' };
    }
    if (cmd === '/caveman' || cmd === '/caveman:caveman') {
      // Bare /caveman → activate at configured default
      if (!arg) {
        const mode = getDefaultMode();
        return mode === 'off' ? { action: 'clear' } : { action: 'set', mode };
      }
      if (arg === 'off' || arg === 'stop' || arg === 'disable') return { action: 'clear' };
      if (arg === 'wenyan-full') return { action: 'set', mode: 'wenyan' }; // canonical alias — config stores as 'wenyan'
      if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return { action: 'set', mode: arg };
      // Unknown arg → no-op, flag untouched (no silent overwrite with default)
      return null;
    }
  }

  return null;
}

module.exports = { parseModeChange, INDEPENDENT_MODES };
