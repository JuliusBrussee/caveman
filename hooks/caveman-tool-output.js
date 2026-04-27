#!/usr/bin/env node
// caveman — ToolOutput hook for post-processing tool results
//
// Runs after tool execution to condense verbose outputs and redact sensitive data.
// This hook is triggered via ToolResult hook point in Claude Code.
//
// Features:
//   - Truncate long terminal output (show head + tail)
//   - Summarize error messages, strip stack traces
//   - Redact sensitive patterns (API keys, file paths, tokens)
//   - Compress JSON to essential keys only
//   - Configurable via CAVEMAN_TOOL_MODE env var: "lite", "full", "ultra"

const fs = require('fs');
const path = require('path');
const os = require('os');

const { getDefaultMode } = require('./caveman-config');

// Sensitive patterns to redact
const SENSITIVE_PATTERNS = [
  // API keys, tokens, secrets
  { pattern: /sk-[a-zA-Z0-9]{20,}/g, replacement: '[API_KEY]' },
  { pattern: /xox[baprs]-[a-zA-Z0-9]{10,}/g, replacement: '[SLACK_TOKEN]' },
  { pattern: /ghp_[a-zA-Z0-9]{36}/g, replacement: '[GITHUB_TOKEN]' },
  { pattern: /AKIA[0-9A-Z]{16}/g, replacement: '[AWS_ACCESS_KEY]' },
  { pattern: /[a-zA-Z0-9_-]*secret[a-zA-Z0-9_-]*/gi, replacement: '[SECRET]' },
  { pattern: /password[=:]\s*\S+/gi, replacement: 'password=[REDACTED]' },
  { pattern: /token[=:]\s*\S+/gi, replacement: 'token=[REDACTED]' },

  // File paths with home directory
  { pattern: new RegExp(os.homedir().replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'g'), replacement: '~' },

  // Private keys
  { pattern: /-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----/g, replacement: '-----BEGIN PRIVATE KEY-----' },
];

const MAX_OUTPUT_LINES = {
  lite: { head: 15, tail: 10 },
  full: { head: 10, tail: 5 },
  ultra: { head: 5, tail: 3 },
};

const MAX_JSON_KEYS = {
  lite: 20,
  full: 10,
  ultra: 5,
};

function getToolMode() {
  const envMode = process.env.CAVEMAN_TOOL_MODE;
  if (envMode && ['lite', 'full', 'ultra'].includes(envMode.toLowerCase())) {
    return envMode.toLowerCase();
  }
  return getDefaultMode();
}

function redactSensitive(text) {
  let result = text;
  for (const { pattern, replacement } of SENSITIVE_PATTERNS) {
    result = result.replace(pattern, replacement);
  }
  return result;
}

function truncateOutput(text, mode) {
  const lines = text.split('\n');
  const { head, tail } = MAX_OUTPUT_LINES[mode] || MAX_OUTPUT_LINES.full;

  if (lines.length <= head + tail) {
    return text;
  }

  const headLines = lines.slice(0, head);
  const tailLines = lines.slice(-tail);
  const omitted = lines.length - head - tail;

  return [
    ...headLines,
    `... [${omitted} lines omitted] ...`,
    ...tailLines
  ].join('\n');
}

function summarizeError(text, mode) {
  const lines = text.split('\n');

  // Extract key error info from first line
  const firstLine = lines[0] || '';

  if (mode === 'ultra') {
    // Ultra: just error type
    const match = firstLine.match(/^(\w+Error|TypeError|ReferenceError|SyntaxError|Error)/);
    return match ? match[1] : 'Error';
  }

  if (mode === 'full') {
    // Full: error type + message (truncated)
    const errorMatch = firstLine.match(/^(\w+Error):\s*(.+)/);
    if (errorMatch) {
      const [, type, msg] = errorMatch;
      const truncatedMsg = msg.length > 60 ? msg.substring(0, 60) + '...' : msg;
      return `${type}: ${truncatedMsg}`;
    }
    return firstLine.substring(0, 100);
  }

  // Lite: type + message + location
  const errorMatch = firstLine.match(/^(\w+Error):\s*(.+?)\s+at\s+(.+)/);
  if (errorMatch) {
    const [, type, msg, location] = errorMatch;
    const truncatedMsg = msg.length > 50 ? msg.substring(0, 50) + '...' : msg;
    return `${type}: ${truncatedMsg} at ${location}`;
  }

  return truncateOutput(text, mode);
}

function compressJson(jsonText, mode) {
  try {
    const obj = JSON.parse(jsonText);
    const maxKeys = MAX_JSON_KEYS[mode];

    // Get top-level keys
    const keys = Object.keys(obj).slice(0, maxKeys);
    const truncated = {};
    for (const key of keys) {
      const val = obj[key];
      if (val === null || val === undefined) continue;
      if (typeof val === 'string' && val.length > 100) {
        truncated[key] = val.substring(0, 100) + '...';
      } else if (typeof val === 'object') {
        truncated[key] = `[${Array.isArray(val) ? 'array' : 'object'} ${Object.keys(val).length}]`;
      } else {
        truncated[key] = val;
      }
    }

    if (Object.keys(obj).length > maxKeys) {
      truncated[`...`] = `[${Object.keys(obj).length - maxKeys} more keys]`;
    }

    return JSON.stringify(truncated, null, 2);
  } catch (e) {
    return jsonText;
  }
}

function processToolResult(toolResult, mode) {
  let output = toolResult;

  // Detect content type and process accordingly
  const isJson = output.trim().startsWith('{') || output.trim().startsWith('[');
  const isError = output.toLowerCase().includes('error') ||
                  output.toLowerCase().includes('exception') ||
                  output.toLowerCase().includes('failed');

  // Step 1: Redact sensitive data first
  output = redactSensitive(output);

  // Step 2: Process based on content type
  if (isJson && !isError) {
    output = compressJson(output, mode);
  } else if (isError) {
    output = summarizeError(output, mode);
  } else {
    output = truncateOutput(output, mode);
  }

  return output;
}

// Main entry point — receives tool result via stdin
let input = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  const mode = getToolMode();
  const result = processToolResult(input.trim(), mode);
  process.stdout.write(result);
});

process.stdin.on('error', () => {
  // Silent fail — don't break tool execution
  process.stdout.write(input);
});