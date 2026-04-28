#!/usr/bin/env node
// caveman-stats reads the active Claude Code session file and prints token usage
// plus an estimated savings figure based on the full-mode benchmark average.
// Run it directly any time: node hooks/caveman-stats.js
// Inside Claude Code, /caveman-stats triggers this via the UserPromptSubmit hook.

const fs   = require('fs');
const path = require('path');
const os   = require('os');
const { readFlag } = require('./caveman-config');

// Only 'full' mode has a measured compression ratio (65% average across 10 tasks,
// from the benchmark in benchmarks/). No measured data exists for other modes.
const COMPRESSION = {
  'full': 0.65,
};

function findMostRecentFile(dirs, matchFn) {
  let latest = null;
  let latestMtime = 0;
  for (const dir of dirs) {
    let entries;
    try { entries = fs.readdirSync(dir); } catch { continue; }
    for (const entry of entries) {
      const fp = path.join(dir, entry);
      try {
        const st = fs.statSync(fp);
        if (st.isDirectory()) {
          const nested = findMostRecentFile([fp], matchFn);
          if (nested && nested.mtime > latestMtime) {
            latestMtime = nested.mtime; latest = nested;
          }
        } else if (matchFn(entry) && st.mtimeMs > latestMtime) {
          latestMtime = st.mtimeMs; latest = { file: fp, mtime: st.mtimeMs };
        }
      } catch {}
    }
  }
  return latest;
}

function findClaudeCodeSession() {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  const result = findMostRecentFile(
    [path.join(claudeDir, 'projects')],
    (name) => name.endsWith('.jsonl')
  );
  return result ? result.file : null;
}

function parseSession(filePath) {
  let raw;
  try { raw = fs.readFileSync(filePath, 'utf8'); } catch { return { outputTokens: 0, cacheReadTokens: 0, turns: 0 }; }
  let outputTokens = 0, cacheReadTokens = 0, turns = 0;
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    try {
      const entry = JSON.parse(line);
      if (entry.type === 'assistant' && entry.message?.usage) {
        const u = entry.message.usage;
        outputTokens    += u.output_tokens           || 0;
        cacheReadTokens += u.cache_read_input_tokens || 0;
        turns++;
      }
    } catch {}
  }
  return { outputTokens, cacheReadTokens, turns };
}

// --session-file is passed by caveman-mode-tracker.js via the hook's transcript_path
// so we always read the active session, not whichever file was modified most recently.
const cliArgs = process.argv.slice(2);
const sessionFileArg = (() => {
  const i = cliArgs.indexOf('--session-file');
  return i !== -1 ? cliArgs[i + 1] : null;
})();

const sessionFile = sessionFileArg || findClaudeCodeSession();

if (!sessionFile) {
  process.stderr.write('caveman-stats: no Claude Code session found.\n');
  process.exit(1);
}

const { outputTokens, cacheReadTokens, turns } = parseSession(sessionFile);
const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const mode  = readFlag(path.join(claudeDir, '.caveman-active'));
const ratio = COMPRESSION[mode] ?? null;
const sep   = '──────────────────────────────────';
const shortPath = sessionFile.length > 45 ? '...' + sessionFile.slice(-45) : sessionFile;

if (turns === 0) {
  process.stdout.write(`
Caveman Stats
${sep}
No conversation yet — stats available after first response.
${sep}
`);
  process.exit(0);
}

let savingsBlock, footer;
if (ratio !== null) {
  const estNormal = Math.round(outputTokens / (1 - ratio));
  const estSaved  = estNormal - outputTokens;
  savingsBlock = [
    `Est. without caveman:  ${estNormal.toLocaleString()}`,
    `Est. tokens saved:     ${estSaved.toLocaleString()} (~${Math.round(ratio * 100)}%)`,
  ].join('\n');
  footer = 'Savings est. from 10-task benchmark (benchmarks/). Actual varies by task.';
} else if (mode && mode !== 'off') {
  savingsBlock = `No savings estimate for '${mode}' mode — only 'full' has benchmark data.`;
  footer = '';
} else {
  savingsBlock = 'Caveman not active this session.';
  footer = '';
}

process.stdout.write(`
Caveman Stats
${sep}
Session:  ${shortPath}
Turns:    ${turns}
${sep}
Output tokens:         ${outputTokens.toLocaleString()}
Cache-read tokens:     ${cacheReadTokens.toLocaleString()}
${sep}
${savingsBlock}
${footer ? footer + '\n' : ''}`);
