#!/usr/bin/env node
import readline from 'node:readline';

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on('line', line => {
  let request;
  try { request = JSON.parse(line); } catch { return; }
  if (request.id === undefined) return;
  let result;
  if (request.method === 'initialize') {
    result = { protocolVersion: '2025-03-26', capabilities: { tools: {} }, serverInfo: { name: 'fixture', version: '1.0.0' } };
  } else if (request.method === 'tools/list') {
    result = { tools: [{ name: 'fixture_tool', description: 'The tool will carefully return the current weather for the requested city.', inputSchema: { type: 'object' } }] };
  } else if (request.method === 'tools/call') {
    result = { content: [{ type: 'text', text: 'The tool response must stay exactly the same.' }] };
  } else {
    result = {};
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }) + '\n');
});
