#!/usr/bin/env node
// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, '../../../../../');
const outDir = process.env.CAVEMAN_PIXEL_ANTHROPIC_OUT ??
  path.join(repoRoot, 'public/engine/pixel/testdata/transform_anthropic');

const pxpipeRoot = process.cwd();
const transformModule = await import(pathToFileURL(path.join(pxpipeRoot, 'src/core/transform.ts')).href);
const { transformRequest } = transformModule;

const enc = new TextEncoder();
const dec = new TextDecoder();

const big = (prefix, n) =>
  Array.from({ length: n }, (_, i) => `${prefix} line ${String(i).padStart(4, '0')} value=${'x'.repeat(48)}`).join('\n');

function toolSchema() {
  return {
    type: 'object',
    properties: {
      file_path: {
        type: 'string',
        description: 'Absolute path to edit. Must match current workspace file.',
      },
      old_string: {
        type: 'string',
        description: 'Exact previous bytes to replace.',
      },
      new_string: {
        type: 'string',
        description: 'Exact replacement bytes.',
      },
    },
    required: ['file_path', 'old_string', 'new_string'],
  };
}

function turns(count, chars) {
  const messages = [];
  for (let i = 0; i < count; i++) {
    const body = `turn ${i}: ` + 'x'.repeat(chars);
    messages.push({ role: i % 2 === 0 ? 'user' : 'assistant', content: body });
  }
  return messages;
}

const cases = [
  {
    name: 'slab_tool_result',
    input: {
      model: 'claude-fable-5',
      max_tokens: 4096,
      extra_top_level: { survives: true },
      system: [
        {
          type: 'text',
          text:
            'x-anthropic-billing-header: stable-fixture\n' +
            '# Claude Code Rules\n' +
            big('static policy', 1200) +
            '\n<env>\nWorking directory: /repo\nIs directory a git repo: Yes\nToday\'s date: 2026-07-04\n</env>',
          cache_control: { type: 'ephemeral' },
        },
      ],
      tools: [
        {
          name: 'Edit',
          description: 'Edit a file after reading it in this session.',
          input_schema: toolSchema(),
        },
      ],
      messages: [
        {
          role: 'user',
          content: [
            {
              type: 'text',
              text: '<system-reminder>\n' + big('reminder', 140) + '\n</system-reminder>',
              cache_control: { type: 'ephemeral' },
            },
            {
              type: 'tool_result',
              tool_use_id: 'toolu_big',
              content: big('tool result', 900),
            },
            { type: 'text', text: 'Please continue.' },
          ],
        },
      ],
    },
  },
  {
    name: 'history_relocation',
    input: {
      model: 'claude-fable-5',
      system: [{ type: 'text', text: big('history slab', 1200), cache_control: { type: 'ephemeral' } }],
      messages: turns(56, 3500),
    },
  },
  {
    name: 'early_history_only',
    input: {
      model: 'claude-fable-5',
      system: 'short system',
      messages: turns(15, 3500),
    },
  },
];

await fs.rm(outDir, { recursive: true, force: true });
await fs.mkdir(outDir, { recursive: true });

const manifest = [];
for (const c of cases) {
  const inputBytes = enc.encode(JSON.stringify(c.input));
  const { body, info } = await transformRequest(inputBytes);
  const expected = JSON.parse(dec.decode(body));
  await fs.writeFile(path.join(outDir, `${c.name}.input.json`), JSON.stringify(c.input, null, 2) + '\n');
  await fs.writeFile(path.join(outDir, `${c.name}.expected.json`), JSON.stringify(expected, null, 2) + '\n');
  manifest.push({
    name: c.name,
    input: `${c.name}.input.json`,
    expected: `${c.name}.expected.json`,
    info: {
      compressed: info.compressed === true,
      imageCount: info.imageCount ?? 0,
      collapsedTurns: info.collapsedTurns ?? 0,
      collapsedImages: info.collapsedImages ?? 0,
      historyReason: info.historyReason ?? '',
    },
  });
}

await fs.writeFile(path.join(outDir, 'manifest.json'), JSON.stringify({ cases: manifest }, null, 2) + '\n');
console.log(`wrote ${manifest.length} Anthropic transform fixtures to ${outDir}`);
