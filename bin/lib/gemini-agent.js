'use strict';

const os = require('os');
const path = require('path');

// Rewrite a Claude-Code-style subagent frontmatter into Gemini CLI's schema.
//
// The Gemini extension loader reads agents from a hardcoded
// `<extension>/agents/` path and validates every entry in an agent's `tools:`
// array with `isValidToolName(name, { allowWildcards: true })`. That validator
// only accepts Gemini's own builtin tool ids (`read_file`, `replace`, …), the
// `*` wildcard, and MCP/discovered prefixes. Claude Code's tool names
// (`Read`, `Edit`, `Write`, `Grep`, `Glob`, `Bash`) all fail, so the whole
// cavecrew agent definition is rejected with:
//
//   [ExtensionManager] Error loading agent from caveman: ...cavecrew-builder.md:
//     Validation failed: Agent Definition: tools.0: Invalid tool name ...
//
// `model: haiku` is an Anthropic id — it passes Gemini's permissive `model`
// schema but fails at runtime when the subagent is invoked, so we remap the
// known Claude tiers to their Gemini equivalents too.
//
// `agents/cavecrew-*.md` is a single source of truth shared with the Claude
// Code plugin (which needs the Claude names), so the source files must stay
// Claude-canonical. This transform is applied at install time on the *copy*
// Gemini loaded — mirroring lib/opencode-agent.js's `stripOpencodeAgentTools`.
//
// See issues #373, #473, #492.

// Claude Code tool name -> Gemini CLI builtin tool id.
// Verified against the @google/gemini-cli bundle (ALL_BUILTIN_TOOL_NAMES):
//   READ_FILE_TOOL_NAME="read_file", EDIT_TOOL_NAME="replace",
//   WRITE_FILE_TOOL_NAME="write_file", GREP_TOOL_NAME="grep_search",
//   GLOB_TOOL_NAME="glob", SHELL_TOOL_NAME="run_shell_command".
const TOOL_MAP = {
  Read: 'read_file',
  Edit: 'replace',
  Write: 'write_file',
  Grep: 'grep_search',
  Glob: 'glob',
  Bash: 'run_shell_command',
};

// Anthropic model tier -> a sensible Gemini default. Only well-known Claude
// tiers are remapped; any other value is left untouched.
const MODEL_MAP = {
  haiku: 'gemini-2.5-flash',
  sonnet: 'gemini-2.5-pro',
  opus: 'gemini-2.5-pro',
};

const FRONTMATTER_FENCE = '---\n';
const TOOLS_INLINE_RE = /^(tools[ \t]*:[ \t]*)\[([^\]]*)\][ \t]*$/;
const MODEL_RE = /^(model[ \t]*:[ \t]*)(.+?)[ \t]*$/;

// Map a single tool name; unknown names pass through so a future tool the
// agent files add isn't silently dropped (Gemini will still validate it).
function mapToolName(name) {
  return TOOL_MAP[name] || name;
}

function mapGeminiAgentFrontmatter(content) {
  if (typeof content !== 'string' || !content.startsWith(FRONTMATTER_FENCE)) return content;
  const fmEnd = content.indexOf('\n---', FRONTMATTER_FENCE.length);
  if (fmEnd < 0) return content;

  const fm = content.slice(FRONTMATTER_FENCE.length, fmEnd);
  const rest = content.slice(fmEnd);

  const out = fm.split('\n').map((line) => {
    const tm = line.match(TOOLS_INLINE_RE);
    if (tm) {
      const mapped = tm[2]
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
        .map(mapToolName);
      return `${tm[1]}[${mapped.join(', ')}]`;
    }
    const mm = line.match(MODEL_RE);
    if (mm && MODEL_MAP[mm[2].trim()]) {
      return `${mm[1]}${MODEL_MAP[mm[2].trim()]}`;
    }
    return line;
  });

  return FRONTMATTER_FENCE + out.join('\n') + rest;
}

// Resolve the installed cavecrew agents directory. Gemini resolves its
// extension storage from GEMINI_CLI_HOME when set (its homedir() returns that
// env value, then ExtensionStorage.getUserExtensionsDir() builds on it), and
// falls back to the real home otherwise. Hard-coding os.homedir() would miss
// the install dir under that override and silently skip the tool-name fix.
// Mirrors SETTINGS.claudeConfigDir's CLAUDE_CONFIG_DIR handling.
function geminiExtAgentsDir() {
  const home = process.env.GEMINI_CLI_HOME || os.homedir();
  return path.join(home, '.gemini', 'extensions', 'caveman', 'agents');
}

module.exports = {
  mapGeminiAgentFrontmatter,
  mapToolName,
  geminiExtAgentsDir,
  TOOL_MAP,
  MODEL_MAP,
};
