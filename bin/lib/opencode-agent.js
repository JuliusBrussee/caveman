'use strict';

// Strip the `tools:` field from a Claude-Code-style subagent frontmatter so
// the file is valid for opencode, whose schema rejects the YAML array form
// (`tools: [Read, Grep, Bash]`) with:
//
//   Configuration is invalid at .../agents/cavecrew-reviewer.md
//   ↳ Expected object | undefined, got ["Read","Grep","Bash"] tools
//
// opencode allows `tools` to be a map (`{read: true, grep: true}`) or
// omitted entirely. Omitting falls back to opencode's default tool set,
// which is what the cavecrew subagent prompts already self-restrict against
// in their body ("Read-only locator", "No `Bash` available", etc.), so
// dropping the array form is safe.

const TOOLS_FIELD_RE = /^tools[ \t]*:/;
const CONTINUATION_RE = /^[ \t]/;
const FRONTMATTER_FENCE = '---\n';

function stripOpencodeAgentTools(content) {
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

module.exports = { stripOpencodeAgentTools };
