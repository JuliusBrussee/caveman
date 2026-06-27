#!/usr/bin/env node
// caveman — PostToolUse hook: trim oversized built-in tool results BEFORE they
// enter the model's context (where they are then re-sent every later turn).
//
// OFF by default. Enable with CAVEMAN_TRIM_TOOL_RESULTS=1.
//
// In agentic Claude Code the weekly token limit is dominated by INPUT: every
// turn re-sends the whole prefix, including every prior tool result. caveman's
// output compression never touches those bytes. This hook does, using the public
// PostToolUse contract — returning
//   { hookSpecificOutput: { hookEventName: "PostToolUse", updatedToolOutput: <str> } }
// REPLACES the tool result the model sees, so a 50KB grep dump can become a few
// KB before it ever costs context.
//
// Design invariants (why this is safe):
//   - DETERMINISTIC + PURE: the output depends only on the input string — no
//     time, no randomness. A given tool_response always trims to the same bytes,
//     so replays and the prompt cache stay byte-stable. (A non-deterministic
//     transform would re-pay cache_creation on the re-sent result every turn.)
//   - LOSSLESS-FIRST: strips only ANSI/control bytes, carriage-return redraws,
//     trailing whitespace, and blank-line runs — none carry meaning. Head/tail
//     elision (the one lossy step) keeps BOTH ends (errors cluster there), cuts
//     on line boundaries, and names how to recover the full output.
//   - NEVER corrupts structured data: a JSON-ish result (starts with { or [) is
//     passed through untouched — whitespace edits could change a string value.
//   - PROTECTS signal: line cuts are whole-line, so path:line / Lnn: / digit
//     tokens inside kept lines are never split.
//   - FAILS OPEN: any parse/processing error emits nothing, so the original
//     result passes through unchanged. A trim hook must never damage a result.
//
// Honors no config dir — it only reads stdin and writes stdout, per the hook API.

'use strict';

const DEFAULT_THRESHOLD = 8000; // chars (~2k tokens) before we act at all
const HEAD_LINES = 160;
const TAIL_LINES = 60;

// Collapse terminal noise that carries no meaning. Lossless.
function stripNoise(text) {
  return text
    .replace(/\r\n/g, '\n')                              // CRLF -> LF
    .replace(/^[^\n]*\r/gm, '')                          // intra-line CR overwrite: keep only the last segment (progress redraw)
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '')   // OSC sequences
    .replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '')           // CSI (color/cursor)
    .replace(/[ \t]+$/gm, '')                             // trailing whitespace
    .replace(/\n{3,}/g, '\n\n');                          // blank-line runs
}

// Keep head + tail, drop the middle, name the recovery action. Whole-line cuts
// only, so tokens inside kept lines stay intact. Falls back to a char-level
// head/tail for the pathological few-lines-but-huge-bytes case (minified blob).
function elide(text, threshold, headLines, tailLines) {
  const lines = text.split('\n');
  if (lines.length > headLines + tailLines + 5) {
    const dropped = lines.length - headLines - tailLines;
    return (
      lines.slice(0, headLines).join('\n') +
      `\n\n... [caveman: trimmed ${dropped} middle lines of ${lines.length}; ` +
      `re-run the tool to see the full output] ...\n\n` +
      lines.slice(-tailLines).join('\n')
    );
  }
  if (text.length > threshold * 4) {
    const head = text.slice(0, threshold * 2);
    const tail = text.slice(-threshold);
    const droppedChars = text.length - head.length - tail.length;
    return (
      head +
      `\n... [caveman: trimmed ${droppedChars} middle chars; re-run the tool ` +
      `for the full output] ...\n` +
      tail
    );
  }
  return text;
}

// Pure transform. Returns the trimmed string, or null to mean "leave untouched"
// (structured data, or nothing worth changing). Deterministic.
function transform(text, threshold = DEFAULT_THRESHOLD, headLines = HEAD_LINES, tailLines = TAIL_LINES) {
  if (typeof text !== 'string') return null;
  const lead = text.trimStart();
  if (lead.startsWith('{') || lead.startsWith('[')) return null; // structured — never touch
  const stripped = elide(stripNoise(text), threshold, headLines, tailLines);
  return stripped.length < text.length ? stripped : null;
}

function resolveThreshold() {
  const n = parseInt(process.env.CAVEMAN_TRIM_THRESHOLD, 10);
  return Number.isFinite(n) && n > 0 ? n : DEFAULT_THRESHOLD;
}

const TRIMMABLE_TOOLS = new Set(['Read', 'Bash', 'Grep', 'Glob']);

function main() {
  // Fast path: do nothing (not even read stdin) unless explicitly enabled.
  if (process.env.CAVEMAN_TRIM_TOOL_RESULTS !== '1') return;

  let input = '';
  process.stdin.on('data', (c) => { input += c; });
  process.stdin.on('end', () => {
    try {
      const data = JSON.parse(input);
      if (!TRIMMABLE_TOOLS.has(data.tool_name)) return;
      const resp = data.tool_response;
      if (typeof resp !== 'string') return;          // only plain-string results
      const threshold = resolveThreshold();
      if (resp.length < threshold) return;           // small enough — leave it
      const trimmed = transform(resp, threshold);
      if (trimmed == null) return;                   // structured / no gain
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: 'PostToolUse',
          updatedToolOutput: trimmed,
        },
      }));
    } catch (e) {
      // Fail open — never damage a tool result.
    }
  });
}

if (require.main === module) main();

module.exports = { transform, stripNoise, elide, DEFAULT_THRESHOLD, TRIMMABLE_TOOLS };
