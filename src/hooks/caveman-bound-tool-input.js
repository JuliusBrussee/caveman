#!/usr/bin/env node
// caveman — PreToolUse hook: bound oversized built-in tool INPUT at the source.
//
// Why this exists (and why it's NOT a PostToolUse trim): Claude Code only honors
// tool-result *output* replacement for MCP tools — a PostToolUse hook cannot trim
// a Read/Bash/Grep result (verified empirically + in the engine: output
// replacement is gated to `mcp__*` tools). But a PreToolUse hook's `updatedInput`
// IS applied to the real tool call (verified live: injecting `limit` into a Read
// capped the result). So instead of shrinking the result after the fact, we cap
// it BEFORE it's produced — which also means it never costs context, an API call,
// or a cache write.
//
// What it bounds:
//   Read  — injects a `limit` when the model didn't set one. An unbounded Read has
//           NO native size protection (its maxResultSizeChars is Infinity), so a
//           huge file dumps fully into context. This is the single biggest leak.
//   Grep  — reins in an explicitly huge `head_limit` (content-mode greps).
//
// OFF by default. Enable with CAVEMAN_BOUND_TOOL_INPUT=1.
//   CAVEMAN_BOUND_READ_LIMIT  max lines injected for an unbounded Read (default 2000)
//   CAVEMAN_BOUND_GREP_HEAD   ceiling for an explicit Grep head_limit  (default 1000)
//
// Invariants:
//   - Only ADD/lower a bound; never override a smaller explicit value — respect
//     the model's intent (if it asked for 50 lines, leave 50).
//   - `updatedInput` REPLACES the whole input, so the original is spread.
//   - Read input is a strict object (file_path/offset/limit/pages) — never add an
//     unknown key, or Zod rejects the call.
//   - Fail-open: any parse/processing error emits nothing → the call runs unchanged.

'use strict';

const DEFAULT_READ_LIMIT = 2000;
const DEFAULT_GREP_HEAD = 1000;

function posIntEnv(name, fallback) {
  const n = parseInt(process.env[name], 10);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

// Pure decision. Returns a new input object to inject, or null to leave the call
// unchanged. Deterministic — depends only on tool_name + input.
function boundInput(toolName, input, opts = {}) {
  if (!input || typeof input !== 'object') return null;
  const readLimit = opts.readLimit || DEFAULT_READ_LIMIT;
  const grepHead = opts.grepHead || DEFAULT_GREP_HEAD;

  if (toolName === 'Read') {
    // Cap an unbounded whole-file read. Only when no limit was set and there's a
    // real file_path. Keep strict-object safe: spread known keys only.
    if (typeof input.file_path === 'string' && input.limit == null) {
      return { ...input, limit: readLimit };
    }
    return null;
  }

  if (toolName === 'Grep') {
    // Only rein in an explicitly oversized head_limit; an omitted head_limit
    // already gets the tool's own small default, so don't touch it.
    if (typeof input.head_limit === 'number' && input.head_limit > grepHead) {
      return { ...input, head_limit: grepHead };
    }
    return null;
  }

  return null;
}

function main() {
  if (process.env.CAVEMAN_BOUND_TOOL_INPUT !== '1') return;

  let raw = '';
  process.stdin.on('data', (c) => { raw += c; });
  process.stdin.on('end', () => {
    try {
      const data = JSON.parse(raw);
      const updated = boundInput(data.tool_name, data.tool_input, {
        readLimit: posIntEnv('CAVEMAN_BOUND_READ_LIMIT', DEFAULT_READ_LIMIT),
        grepHead: posIntEnv('CAVEMAN_BOUND_GREP_HEAD', DEFAULT_GREP_HEAD),
      });
      if (updated == null) return;
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: { hookEventName: 'PreToolUse', updatedInput: updated },
      }));
    } catch (e) {
      // Fail open — never block or corrupt a tool call.
    }
  });
}

if (require.main === module) main();

module.exports = { boundInput, DEFAULT_READ_LIMIT, DEFAULT_GREP_HEAD };
