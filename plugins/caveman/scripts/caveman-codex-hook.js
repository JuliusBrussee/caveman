#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const VALID_MODES = new Set([
  'off',
  'lite',
  'full',
  'ultra',
  'wenyan-lite',
  'wenyan',
  'wenyan-full',
  'wenyan-ultra',
]);

const CONFIG_DIR = cavemanConfigDir();
const CONFIG_PATH = path.join(CONFIG_DIR, 'config.json');
const FLAG_PATH = path.join(CONFIG_DIR, 'active-mode');
const SAVINGS_LEDGER_PATH = path.join(CONFIG_DIR, 'savings-ledger.json');
const LIFETIME_SAVED_PATH = path.join(CONFIG_DIR, 'lifetime-saved-tokens');
const LIFETIME_SUFFIX_PATH = path.join(CONFIG_DIR, 'lifetime-savings-suffix');
const MAX_CONFIG_BYTES = 16 * 1024;
const MAX_FLAG_BYTES = 64;
const MAX_LEDGER_BYTES = 1024 * 1024;
const MAX_TRANSCRIPT_BYTES = 10 * 1024 * 1024;
const SKILL_PATHS = [
  path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'),
  path.join(os.homedir(), '.agents', 'skills', 'caveman', 'SKILL.md'),
];
const SESSIONS_DIR = process.env.CODEX_HOME
  ? path.join(process.env.CODEX_HOME, 'sessions')
  : path.join(os.homedir(), '.codex', 'sessions');

const eventArg = process.argv[2] || '';

let input = '';
process.stdin.on('data', chunk => {
  input += chunk;
});
process.stdin.on('end', () => {
  let data = {};
  try {
    data = input.trim() ? JSON.parse(input) : {};
  } catch (_) {
    data = {};
  }

  const event = data.hook_event_name || data.hookEventName || eventArg;
  if (event === 'SessionStart') {
    activate();
    return;
  }
  if (event === 'UserPromptSubmit') {
    if (isStatsPrompt(extractPrompt(data))) {
      reportStats(data);
      return;
    }
    trackPrompt(data);
  }
});

function cavemanConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'caveman');
  }
  if (process.platform === 'win32') {
    return path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'caveman'
    );
  }
  return path.join(os.homedir(), '.config', 'caveman');
}

function activate() {
  const mode = defaultMode();
  if (mode === 'off') {
    removeFlag();
    writeContext('SessionStart', 'CAVEMAN MODE OFF. Config defaultMode=off.');
    return;
  }

  writeFlag(mode);
  writeContext('SessionStart', rulesFor(mode));
}

function trackPrompt(data) {
  const prompt = extractPrompt(data).trim().toLowerCase();
  if (prompt) {
    const mode = modeFromPrompt(prompt);
    if (mode === 'off') {
      removeFlag();
    } else if (mode) {
      writeFlag(mode);
    }
  }

  const active = readFlag();
  if (active && active !== 'off') {
    writeContext(
      'UserPromptSubmit',
      'CAVEMAN MODE ACTIVE (' + canonicalMode(active) + '). Drop articles/filler/pleasantries/hedging. Fragments OK. Code/commits/security: write normal.'
    );
  }
}

function extractPrompt(data) {
  if (typeof data.prompt === 'string') return data.prompt;
  if (typeof data.user_prompt === 'string') return data.user_prompt;
  if (typeof data.userPrompt === 'string') return data.userPrompt;
  if (typeof data.message === 'string') return data.message;
  if (typeof data.text === 'string') return data.text;
  if (data.input && typeof data.input === 'string') return data.input;
  return '';
}

function isStatsPrompt(prompt) {
  const normalized = String(prompt || '').trim().toLowerCase();
  return normalized === '/caveman-stats' ||
    normalized === '$caveman-stats' ||
    normalized === '/caveman stats' ||
    normalized === '$caveman stats' ||
    normalized === 'caveman stats';
}

function modeFromPrompt(prompt) {
  if (
    /\b(do not|don\'t|dont|never)\b.*\b(use|enable|activate|start|turn on|talk like)\b.*\bcaveman\b/.test(prompt) ||
    /\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/.test(prompt) ||
    /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/.test(prompt) ||
    /\bnormal mode\b/.test(prompt) ||
    /^\/caveman(?::caveman)?\s+(off|stop|disable)\b/.test(prompt) ||
    /^\$caveman\s+(off|stop|disable)\b/.test(prompt)
  ) {
    return 'off';
  }

  const command = /^(?:\/caveman(?::caveman)?|\$caveman)(?:\s+(\S+))?/.exec(prompt);
  if (command) {
    const arg = command[1];
    if (!arg) return defaultMode();
    if (arg === 'wenyan-full') return 'wenyan';
    if (VALID_MODES.has(arg) && arg !== 'off') return arg;
    return null;
  }

  if (
    /\b(activate|enable|turn on|start|talk like|use)\b.*\bcaveman\b/.test(prompt) ||
    /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/.test(prompt)
  ) {
    return defaultMode();
  }

  return null;
}

function defaultMode() {
  const envMode = (process.env.CAVEMAN_DEFAULT_MODE || '').toLowerCase();
  if (VALID_MODES.has(envMode)) return envMode;

  try {
    const config = JSON.parse(safeReadFile(CONFIG_PATH, MAX_CONFIG_BYTES) || '{}');
    const mode = String(config.defaultMode || '').toLowerCase();
    if (VALID_MODES.has(mode)) return mode;
  } catch (_) {
    // Missing config is fine.
  }

  return 'full';
}

function rulesFor(mode) {
  const modeLabel = canonicalMode(mode);
  const fallback = [
    'CAVEMAN MODE ACTIVE - level: ' + modeLabel,
    '',
    'Respond terse like smart caveman. All technical substance stay. Only fluff die.',
    '',
    'Drop: articles (a/an/the), filler, pleasantries, hedging. Fragments OK. Short synonyms. Technical terms exact. Code blocks unchanged. Errors quoted exact.',
    'Pattern: [thing] [action] [reason]. [next step].',
    'Auto-clarity: security warnings, irreversible confirmations, and ambiguous multi-step sequences use normal clear prose.',
    'Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert.',
  ].join('\n');

  for (const skillPath of SKILL_PATHS) {
    try {
      const body = fs.readFileSync(skillPath, 'utf8').replace(/^---[\s\S]*?---\s*/, '');
      return 'CAVEMAN MODE ACTIVE - level: ' + modeLabel + '\n\n' + filterSkill(body, modeLabel);
    } catch (_) {
      // Try next source.
    }
  }
  return fallback;
}

function filterSkill(body, modeLabel) {
  return body
    .split('\n')
    .filter(line => {
      const row = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
      if (row) return row[1] === modeLabel;
      const example = line.match(/^- (\S+?):\s/);
      if (example) return example[1] === modeLabel;
      return true;
    })
    .join('\n');
}

function canonicalMode(mode) {
  return mode === 'wenyan' ? 'wenyan-full' : mode;
}

function reportStats(data) {
  const transcript = transcriptPath(data);
  if (!transcript) {
    writeBlock('Caveman stats unavailable: no Codex session transcript found.');
    return;
  }

  let stats;
  try {
    stats = readSessionStats(transcript);
  } catch (error) {
    writeBlock('Caveman stats unavailable: failed to read ' + transcript + ': ' + error.message);
    return;
  }

  if (!stats.usage) {
    writeBlock('Caveman stats unavailable: no token_count receipt found in ' + transcript + '.');
    return;
  }

  const mode = canonicalMode(readFlag() || defaultMode());
  const rate = savingsRate(mode);
  const inputTokens = numeric(stats.usage.input_tokens);
  const outputTokens = numeric(stats.usage.output_tokens);
  const baselineTokens = Math.max(outputTokens, Math.round(outputTokens / (1 - rate)));
  const savedTokens = Math.max(0, baselineTokens - outputTokens);
  const totalSaved = writeSavingsFiles(transcript, savedTokens);

  writeBlock([
    'Session: ' + formatNumber(stats.turns) + ' turns',
    'Input:   ' + formatNumber(inputTokens) + ' tokens',
    'Output:  ' + formatNumber(outputTokens) + ' tokens (caveman)',
    'Baseline: ' + formatNumber(baselineTokens) + ' tokens (estimated without caveman)',
    'Saved:    ' + formatNumber(savedTokens) + ' tokens (~' + Math.round(rate * 100) + '%)',
    'Lifetime: ' + formatNumber(totalSaved) + ' tokens saved',
  ].join('\n'));
}

function transcriptPath(data) {
  const candidates = [
    data.transcript_path,
    data.transcriptPath,
    data.session_transcript_path,
    data.sessionTranscriptPath,
  ];
  for (const candidate of candidates) {
    if (typeof candidate === 'string' && candidate) {
      const safePath = safeTranscriptPath(candidate);
      if (safePath) return safePath;
    }
  }
  return null;
}

function safeTranscriptPath(candidate) {
  let sessionsRoot;
  let realFile;
  try {
    sessionsRoot = fs.realpathSync(SESSIONS_DIR);
    const absolute = path.resolve(candidate);
    const stat = fs.lstatSync(absolute);
    if (stat.isSymbolicLink() || !stat.isFile() || stat.size > MAX_TRANSCRIPT_BYTES) return null;
    realFile = fs.realpathSync(absolute);
  } catch (_) {
    return null;
  }

  return pathInside(realFile, sessionsRoot) ? realFile : null;
}

function readSessionStats(file) {
  const stats = {
    turns: 0,
    fallbackTurns: 0,
    usage: null,
  };

  const body = safeReadFile(file, MAX_TRANSCRIPT_BYTES);
  if (body === null) {
    throw new Error('transcript is missing, unsafe, or larger than ' + formatNumber(MAX_TRANSCRIPT_BYTES) + ' bytes');
  }

  const lines = body.split('\n');
  for (const line of lines) {
    if (!line.trim()) continue;

    let event;
    try {
      event = JSON.parse(line);
    } catch (_) {
      continue;
    }

    const payload = event.payload || {};
    if (event.type === 'event_msg' && payload.type === 'user_message') {
      stats.turns += 1;
    }
    if (event.type === 'response_item' && payload.type === 'message' && payload.role === 'user') {
      stats.fallbackTurns += 1;
    }
    if (payload.type === 'token_count' && payload.info && payload.info.total_token_usage) {
      stats.usage = payload.info.total_token_usage;
    }
  }

  if (stats.turns === 0) stats.turns = stats.fallbackTurns;
  delete stats.fallbackTurns;
  return stats;
}

function savingsRate(mode) {
  switch (canonicalMode(mode)) {
    case 'lite':
      return 0.35;
    case 'ultra':
      return 0.75;
    case 'wenyan-lite':
      return 0.75;
    case 'wenyan-full':
      return 0.85;
    case 'wenyan-ultra':
      return 0.9;
    case 'full':
    default:
      return 0.65;
  }
}

function numeric(value) {
  return Number.isFinite(value) ? value : 0;
}

function formatNumber(value) {
  return String(Math.round(value)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

function writeSavingsFiles(transcript, savedTokens) {
  let ledger = {};
  try {
    const parsed = JSON.parse(safeReadFile(SAVINGS_LEDGER_PATH, MAX_LEDGER_BYTES) || '{}');
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) ledger = parsed;
  } catch (_) {
    ledger = {};
  }

  ledger[transcript] = savedTokens;
  const totalSaved = Object.values(ledger).reduce((sum, value) => sum + numeric(value), 0);

  try {
    safeWriteFile(SAVINGS_LEDGER_PATH, JSON.stringify(ledger, null, 2) + '\n');
    safeWriteFile(LIFETIME_SAVED_PATH, String(totalSaved) + '\n');
    safeWriteFile(LIFETIME_SUFFIX_PATH, 'saved ' + compactNumber(totalSaved) + '\n');
  } catch (_) {
    // Stats should still display if statusline files cannot be written.
  }

  return totalSaved;
}

function compactNumber(value) {
  const rounded = Math.round(value);
  if (rounded >= 1000000) return trimDecimal(rounded / 1000000) + 'm';
  if (rounded >= 1000) return trimDecimal(rounded / 1000) + 'k';
  return String(rounded);
}

function trimDecimal(value) {
  return value.toFixed(1).replace(/\.0$/, '');
}

function writeFlag(mode) {
  safeWriteFile(FLAG_PATH, mode + '\n');
}

function readFlag() {
  try {
    const mode = (safeReadFile(FLAG_PATH, MAX_FLAG_BYTES) || '').trim().toLowerCase();
    return VALID_MODES.has(mode) ? mode : null;
  } catch (_) {
    return null;
  }
}

function removeFlag() {
  try {
    fs.unlinkSync(FLAG_PATH);
  } catch (_) {
    // Missing flag is fine.
  }
}

function safeWriteFile(filePath, content) {
  let tempPath = null;
  let fd;
  try {
    const realPath = safeTargetPath(filePath);
    if (!realPath) return false;

    try {
      const st = fs.lstatSync(realPath);
      if (st.isSymbolicLink() || !st.isFile()) return false;
    } catch (error) {
      if (error.code !== 'ENOENT') return false;
    }

    const dir = path.dirname(realPath);
    const base = path.basename(realPath);
    const nonce = String(Date.now()) + '.' + Math.random().toString(16).slice(2);
    tempPath = path.join(dir, '.' + base + '.' + process.pid + '.' + nonce + '.tmp');
    const nofollow = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | nofollow;
    fd = fs.openSync(tempPath, flags, 0o600);
    fs.writeSync(fd, String(content));
    try { fs.fchmodSync(fd, 0o600); } catch (_) {}
    fs.closeSync(fd);
    fd = undefined;
    fs.renameSync(tempPath, realPath);
    tempPath = null;
    return true;
  } catch (_) {
    return false;
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch (_) {}
    }
    if (tempPath) {
      try { fs.unlinkSync(tempPath); } catch (_) {}
    }
  }
}

function safeReadFile(filePath, maxBytes) {
  let fd;
  try {
    const st = fs.lstatSync(filePath);
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (typeof maxBytes === 'number' && st.size > maxBytes) return null;

    const size = typeof maxBytes === 'number' ? Math.min(st.size, maxBytes) : st.size;
    const nofollow = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    fd = fs.openSync(filePath, fs.constants.O_RDONLY | nofollow);
    const buffer = Buffer.alloc(size);
    const bytes = fs.readSync(fd, buffer, 0, size, 0);
    return buffer.slice(0, bytes).toString('utf8');
  } catch (_) {
    return null;
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch (_) {}
    }
  }
}

function safeTargetPath(filePath) {
  try {
    const dir = path.dirname(filePath);
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 });

    let realDir = dir;
    const dirStat = fs.lstatSync(dir);
    if (dirStat.isSymbolicLink()) {
      realDir = fs.realpathSync(dir);
      const realStat = fs.statSync(realDir);
      if (!realStat.isDirectory()) return null;
      if (typeof process.getuid === 'function') {
        if (realStat.uid !== process.getuid()) return null;
      } else if (!pathUnderHome(realDir)) {
        return null;
      }
    } else if (!dirStat.isDirectory()) {
      return null;
    }

    return path.join(realDir, path.basename(filePath));
  } catch (_) {
    return null;
  }
}

function pathUnderHome(candidate) {
  const home = path.resolve(os.homedir()).toLowerCase();
  const real = path.resolve(candidate).toLowerCase();
  return real === home || real.startsWith(home + path.sep);
}

function pathInside(candidate, root) {
  let real = path.resolve(candidate);
  let base = path.resolve(root);
  if (process.platform === 'win32') {
    real = real.toLowerCase();
    base = base.toLowerCase();
  }
  return real === base || real.startsWith(base + path.sep);
}

function writeContext(event, context) {
  process.stdout.write(JSON.stringify({
    continue: true,
    hookSpecificOutput: {
      hookEventName: event,
      additionalContext: context,
    },
  }));
}

function writeBlock(reason) {
  process.stdout.write(JSON.stringify({
    decision: 'block',
    reason,
  }));
}
