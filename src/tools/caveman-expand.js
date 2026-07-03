#!/usr/bin/env node
// caveman-expand — EXPERIMENTAL asymmetric renderer.
//
// The idea: let the expensive frontier model speak caveman-ultra (cheap
// output tokens), then expand the terse prose back to readable English with
// a free local model. You pay smart-model prices for the thinking and the
// compressed answer; the readability tax is paid locally at zero API cost.
//
// This is a paraphraser, not a decoder — semantic compression is lossy, and
// the expansion is the local model's wording, not the frontier model's. Code
// fences, inline code, paths, and numbers pass through byte-identical; only
// prose paragraphs are expanded. Keep the terse original when it matters.
//
// Usage:
//   some-agent | node caveman-expand.js            # expand stdin → stdout
//   node caveman-expand.js --file notes.md         # expand a file → stdout
//   node caveman-expand.js --check                 # probe the local model
//
// Backend: a local Ollama server (https://ollama.com). Configure with:
//   OLLAMA_HOST            default http://127.0.0.1:11434
//   CAVEMAN_EXPAND_MODEL   default llama3.2  (or pass --model <name>)
//
// If no local model is reachable the input passes through UNCHANGED (exit 0,
// note on stderr) — this tool must never eat your output.

'use strict';

const fs = require('fs');

const DEFAULT_HOST = 'http://127.0.0.1:11434';
const DEFAULT_MODEL = 'llama3.2';
const REQUEST_TIMEOUT_MS = 30_000;

// Split markdown into segments: fenced code blocks are preserved verbatim,
// everything between them is prose eligible for expansion. Fences keep their
// surrounding newlines so reassembly is byte-faithful for the code parts.
function segmentMarkdown(text) {
  const segments = [];
  const fence = /^(```|~~~)[^\n]*\n[\s\S]*?^\1[ \t]*$/gm;
  let last = 0;
  let m;
  while ((m = fence.exec(text)) !== null) {
    if (m.index > last) segments.push({ type: 'prose', text: text.slice(last, m.index) });
    segments.push({ type: 'code', text: m[0] });
    last = m.index + m[0].length;
  }
  if (last < text.length) segments.push({ type: 'prose', text: text.slice(last) });
  return segments;
}

// The expansion instruction. Inline code, paths, numbers, and flags must
// survive verbatim — the local model is told to only re-word prose around
// them, never to add facts the terse text doesn't contain.
function buildPrompt(prose) {
  return (
    'Rewrite the following terse notes as clear, friendly English prose. ' +
    'Rules: do NOT add information, opinions, or steps that are not in the notes. ' +
    'Keep every `inline code` span, file path, number, flag, and URL exactly as written. ' +
    'Keep markdown structure (headings, lists) intact. Output only the rewrite.\n\n' +
    prose
  );
}

async function ollamaGenerate({ host, model, prompt }) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(`${host}/api/generate`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model, prompt, stream: false }),
      signal: controller.signal,
    });
    if (!res.ok) return null;
    const body = await res.json();
    return typeof body.response === 'string' && body.response.trim() ? body.response : null;
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

async function probe(host) {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 3_000);
    const res = await fetch(`${host}/api/tags`, { signal: controller.signal });
    clearTimeout(timer);
    return res.ok;
  } catch {
    return false;
  }
}

async function expand(text, { host, model }) {
  const segments = segmentMarkdown(text);
  const out = [];
  let anyExpanded = false;
  for (const seg of segments) {
    if (seg.type === 'code' || !seg.text.trim()) {
      out.push(seg.text);
      continue;
    }
    const expanded = await ollamaGenerate({ host, model, prompt: buildPrompt(seg.text) });
    if (expanded === null) {
      out.push(seg.text); // backend gone mid-run — degrade to passthrough
    } else {
      out.push(expanded);
      anyExpanded = true;
    }
  }
  return { text: out.join(''), anyExpanded };
}

function readStdin() {
  return new Promise((resolve) => {
    let data = '';
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', (c) => { data += c; });
    process.stdin.on('end', () => resolve(data));
  });
}

async function main() {
  const args = process.argv.slice(2);
  const host = (process.env.OLLAMA_HOST || DEFAULT_HOST).replace(/\/$/, '');
  const mi = args.indexOf('--model');
  const model = mi !== -1 ? args[mi + 1] : (process.env.CAVEMAN_EXPAND_MODEL || DEFAULT_MODEL);
  const fi = args.indexOf('--file');

  if (args.includes('--help')) {
    process.stdout.write('caveman-expand (experimental): pipe terse text in, readable prose out.\n' +
      'Flags: --file <path>, --model <ollama-model>, --check, --help\n' +
      `Backend: Ollama at OLLAMA_HOST (default ${DEFAULT_HOST}), model ${DEFAULT_MODEL} by default.\n`);
    return;
  }

  if (args.includes('--check')) {
    const ok = await probe(host);
    process.stdout.write(ok
      ? `ok: Ollama reachable at ${host} (model: ${model})\n`
      : `unavailable: no Ollama at ${host} — expansion will pass through unchanged\n`);
    process.exitCode = ok ? 0 : 1;
    return;
  }

  const input = fi !== -1 ? fs.readFileSync(args[fi + 1], 'utf8') : await readStdin();
  if (!input.trim()) return;

  if (!(await probe(host))) {
    process.stderr.write(`caveman-expand: no local model at ${host} — passing through unchanged\n`);
    process.stdout.write(input);
    return;
  }

  const { text, anyExpanded } = await expand(input, { host, model });
  if (!anyExpanded) {
    process.stderr.write('caveman-expand: backend did not respond — passed through unchanged\n');
  }
  process.stdout.write(text);
}

if (require.main === module) {
  main().catch(() => {
    // Never fail the pipe: on any unexpected error, stay silent on stdout
    // (nothing was written yet in the error paths above) and exit non-zero.
    process.exitCode = 1;
  });
}

module.exports = { segmentMarkdown, buildPrompt, expand, probe };
