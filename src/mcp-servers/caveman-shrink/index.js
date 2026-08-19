#!/usr/bin/env node
// caveman-shrink — MCP middleware that proxies an upstream MCP server and
// compresses prose fields so the model sees fewer tokens.
//
// Usage:
//   caveman-shrink <upstream-command> [...args]
//
// Example wrapping the filesystem MCP server:
//   "mcpServers": {
//     "fs-shrunk": {
//       "command": "npx",
//       "args": ["caveman-shrink", "npx", "@modelcontextprotocol/server-filesystem", "/some/path"]
//     }
//   }
//
// Compression is applied to:
//   - "description" fields in tools/list, prompts/list, resources/list responses
//   - same boundaries as caveman-compress: code, URLs, paths, identifiers preserved
//
// What we deliberately DON'T touch in v1:
//   - tools/call response content (high risk of breaking downstream parsing)
//   - request payloads going TO the upstream server
//
// Configuration (env vars):
//   CAVEMAN_SHRINK_FIELDS   comma-separated extra field names to compress
//                           (default: description)
//   CAVEMAN_SHRINK_DEBUG=1  log compression deltas to stderr

const { spawn } = require('child_process');
const { constants: osConstants } = require('os');
const { StringDecoder } = require('string_decoder');
const { compressDescriptionsInPlace, compress } = require('./compress');

const args = process.argv.slice(2);
if (args.length === 0) {
  process.stderr.write('caveman-shrink: missing upstream command.\n');
  process.stderr.write('Usage: caveman-shrink <upstream-command> [...args]\n');
  process.exit(2);
}

const debug = process.env.CAVEMAN_SHRINK_DEBUG === '1';
const fields = (process.env.CAVEMAN_SHRINK_FIELDS || 'description')
  .split(',').map(s => s.trim()).filter(Boolean);

const { getSpawnInvocation, getSpawnOptions } = require('./spawn-options');

let invocation;
try {
  invocation = getSpawnInvocation(args[0], args.slice(1));
} catch (error) {
  process.stderr.write(`caveman-shrink: failed to resolve upstream safely: ${error.message}\n`);
  process.exit(1);
}
const upstream = spawn(invocation.command, invocation.args, getSpawnOptions());

let spawnFailed = false;
upstream.on('error', err => {
  spawnFailed = true;
  process.stderr.write(`caveman-shrink: failed to spawn upstream: ${err.message}\n`);
});

// `exit` can fire while stdout still has unread data and while our own stdout
// is backpressured. Wait for child `close`, stop accepting client input, then
// let Node exit naturally so every transformed byte drains.
upstream.on('close', (code, signal) => {
  process.stdin.pause();
  process.stdin.removeListener('data', forwardInput);
  process.stdin.removeListener('end', endInput);
  if (spawnFailed) {
    process.exitCode = 1;
  } else if (signal) {
    process.exitCode = 128 + (osConstants.signals[signal] || 1);
  } else {
    process.exitCode = code || 0;
  }
});

// JSON-RPC framing over stdio: messages are separated by newlines (the
// MCP stdio transport uses LSP-like content but most servers emit one JSON
// object per line). We line-buffer in both directions and parse opportunistically.
function makeLineBuffer(onLine) {
  let buf = '';
  const decoder = new StringDecoder('utf8');
  const flushLines = () => {
    let nl;
    while ((nl = buf.indexOf('\n')) !== -1) {
      const line = buf.slice(0, nl);
      buf = buf.slice(nl + 1);
      if (line.trim()) onLine(line);
    }
  };
  return {
    push(chunk) {
      buf += decoder.write(chunk);
      flushLines();
    },
    end() {
      buf += decoder.end();
      if (buf.trim()) onLine(buf);
      buf = '';
    },
  };
}

function transformResponse(msg) {
  // Compress description fields on list-style responses. Match by method
  // shape — we don't always know the original request's method, so we
  // detect by the presence of a tools/prompts/resources array.
  if (!msg || !msg.result || typeof msg.result !== 'object') return msg;
  const r = msg.result;
  let compressedSomething = false;

  for (const arrayName of ['tools', 'prompts', 'resources', 'resourceTemplates']) {
    if (Array.isArray(r[arrayName])) {
      for (const item of r[arrayName]) {
        for (const field of fields) {
          if (typeof item[field] === 'string') {
            const before = item[field];
            const out = compress(before).compressed;
            if (out !== before) {
              item[field] = out;
              compressedSomething = true;
              if (debug) {
                process.stderr.write(
                  `[caveman-shrink] ${arrayName}.${item.name || '?'}.${field}: ` +
                  `${before.length}→${out.length} bytes\n`
                );
              }
            }
          }
        }
      }
    }
  }

  // Walk nested inputSchema descriptions (e.g. tool parameter descriptions).
  // Always run — top-level compression does not cover nested schemas.
  for (const arrayName of ['tools', 'prompts', 'resources', 'resourceTemplates']) {
    if (Array.isArray(r[arrayName])) {
      for (const item of r[arrayName]) {
        if (item.inputSchema) compressDescriptionsInPlace(item.inputSchema, fields);
      }
    }
  }

  return msg;
}

function writeClient(value) {
  if (process.stdout.write(value)) return;
  upstream.stdout.pause();
  process.stdout.once('drain', () => upstream.stdout.resume());
}

// Upstream → us → client (model). Transform here.
const responses = makeLineBuffer(line => {
  let msg;
  try { msg = JSON.parse(line); } catch {
    // Pass through unparseable lines unchanged.
    writeClient(line + '\n');
    return;
  }
  const out = transformResponse(msg);
  writeClient(JSON.stringify(out) + '\n');
});
upstream.stdout.on('data', chunk => responses.push(chunk));
upstream.stdout.on('end', () => responses.end());

// Client → us → upstream. Pass through unchanged for v1.
function forwardInput(chunk) {
  if (!upstream.stdin.writable || upstream.stdin.destroyed) return;
  if (!upstream.stdin.write(chunk)) {
    process.stdin.pause();
    upstream.stdin.once('drain', () => process.stdin.resume());
  }
}
function endInput() {
  if (upstream.stdin.writable && !upstream.stdin.destroyed) upstream.stdin.end();
}
upstream.stdin.on('error', err => {
  if (err.code !== 'EPIPE' && !spawnFailed) {
    process.stderr.write(`caveman-shrink: upstream stdin failed: ${err.message}\n`);
    process.exitCode = 1;
  }
});
process.stdin.on('data', forwardInput);
process.stdin.on('end', endInput);
