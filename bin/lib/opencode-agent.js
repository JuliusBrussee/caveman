'use strict';

// Strip the `tools:` field from Claude-Code-style subagent frontmatter so the
// file can be loaded by agents whose schemas reject Claude's tool names or
// YAML array form (`tools: [Read, Grep, Bash]`). opencode reports:
//
//   Configuration is invalid at .../agents/cavecrew-reviewer.md
//   ↳ Expected object | undefined, got ["Read","Grep","Bash"] tools
//
// Gemini rejects the same frontmatter earlier with `tools.0: Invalid tool
// name` because Claude's tool vocabulary does not match Gemini's.
//
// Omitting the field falls back to the target agent's default tool set. The
// cavecrew prompts already self-restrict in their bodies ("Read-only locator",
// "No `Bash` available", etc.), so dropping the Claude-specific field is safe.

const TOOLS_FIELD_RE = /^tools[ \t]*:/;
const CONTINUATION_RE = /^[ \t]/;
const FRONTMATTER_FENCE = '---\n';

function stripAgentTools(content) {
  if (typeof content !== 'string' || !content.startsWith(FRONTMATTER_FENCE)) return content;
  const fmEnd = content.indexOf('\n---', FRONTMATTER_FENCE.length);
  if (fmEnd < 0) return content;

  const fm = content.slice(FRONTMATTER_FENCE.length, fmEnd);
  const rest = content.slice(fmEnd);

  const out = [];
  let dropping = false;
  for (const line of fm.split('\n')) {
    if (dropping) {
      if (CONTINUATION_RE.test(line)) continue;
      dropping = false;
    }
    if (TOOLS_FIELD_RE.test(line)) { dropping = true; continue; }
    out.push(line);
  }

  return FRONTMATTER_FENCE + out.join('\n') + rest;
}

const stripOpencodeAgentTools = stripAgentTools;

module.exports = { stripAgentTools, stripOpencodeAgentTools };
