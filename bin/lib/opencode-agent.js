'use strict';

// Adapt Claude-Code-style subagent frontmatter for opencode, which rejects two
// Claude-only field shapes.
//
// 1. `tools:` as a YAML array (`tools: [Read, Grep, Bash]`):
//
//      Configuration is invalid at .../agents/cavecrew-reviewer.md
//      ↳ Expected object | undefined, got ["Read","Grep","Bash"] tools
//
//    opencode allows `tools` to be a map (`{read: true, grep: true}`) or
//    omitted entirely. Omitting falls back to opencode's default tool set,
//    which is what the cavecrew subagent prompts already self-restrict against
//    in their body ("Read-only locator", "No `Bash` available", etc.), so
//    dropping the array form is safe.
//
// 2. A `model:` value with no provider prefix (`model: haiku`). opencode model
//    references are `provider/model-id`, so it parses that as provider `haiku`
//    with an empty model ID and fails at runtime with `Model not found: haiku/`
//    (#840).
//
//    The test is the SHAPE — does the value contain a slash — not membership in
//    a list of known Claude aliases. A closed list of {haiku, sonnet, opus}
//    happens to cover today's shipped agents and silently lets through every
//    other provider-less value Claude Code accepts: `inherit`, a full dated id
//    like `claude-haiku-4-5-20251001`, or a future alias. Each produces the
//    same `Model not found: <x>/` at runtime.
//
//    Dropping the field makes the subagent inherit the invoking primary model.
//    Rewriting it to a concrete id instead (`anthropic/claude-haiku-4-5`) would
//    preserve the cheap-model intent, but it assumes the user configured an
//    anthropic provider — for an opencode pointed at any other provider that
//    turns a working subagent into a hard failure. A subagent that costs more
//    than intended still works; one pinned to an absent provider does not.
//
// A slash-qualified id (`anthropic/claude-haiku-4-5`) is valid for opencode and
// is left byte-identical.

const TOOLS_FIELD_RE = /^tools[ \t]*:/;
const MODEL_FIELD_RE = /^model[ \t]*:[ \t]*(.*)$/;
const CONTINUATION_RE = /^[ \t]/;
const FRONTMATTER_FENCE = '---\n';

// True when a `model:` value cannot resolve for opencode. Strips a trailing
// YAML comment and surrounding quotes first, so `model: haiku # cheap` and
// `model: "haiku"` are recognised as the same provider-less value. An empty
// value is left alone — that is malformed frontmatter, not our transform to
// silently repair.
function isProviderlessModel(rawValue) {
  let value = rawValue.replace(/\s+#.*$/, '').trim();
  const quoted = /^(['"])([\s\S]*)\1$/.exec(value);
  if (quoted) value = quoted[2].trim();
  return value !== '' && !value.includes('/');
}

function transformOpencodeAgentFrontmatter(content) {
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
    const model = MODEL_FIELD_RE.exec(line);
    if (model && isProviderlessModel(model[1])) continue;
    out.push(line);
  }

  return FRONTMATTER_FENCE + out.join('\n') + rest;
}

module.exports = { transformOpencodeAgentFrontmatter };
