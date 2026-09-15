#!/usr/bin/env node
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const enc = new TextEncoder();
const dec = new TextDecoder();

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '../../../../..');
const pxpipeRoot = resolve(process.cwd());
const outDir = join(repoRoot, 'public/engine/pixel/testdata/transform_openai');

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const openai = await import(pathToFileURL(join(pxpipeRoot, 'src/core/openai.ts')).href);

const toolParameters = {
  additionalProperties: false,
  properties: {
    alpha: { description: 'Alpha value description repeated for fixture parity.', type: 'string' },
    beta: { description: 'Beta value description repeated for fixture parity.', type: 'string' },
  },
  required: ['alpha', 'beta'],
  type: 'object',
};

const fixtures = [
  {
    id: 'chat',
    shape: 'chat',
    input: {
      model: 'gpt-5.6',
      metadata: { preserved: true },
      messages: [
        { role: 'system', content: 'System guidance for OpenAI chat pixel fixture. '.repeat(360) },
        { role: 'user', content: 'Please answer using the native tool when useful.' },
      ],
      tools: [{
        type: 'function',
        function: {
          name: 'do_thing',
          description: 'Detailed tool description for OpenAI chat fixture. '.repeat(180),
          parameters: toolParameters,
        },
      }],
    },
    transform: openai.transformOpenAIChatCompletions,
  },
  {
    id: 'responses',
    shape: 'responses',
    input: {
      model: 'gpt-5.6',
      metadata: { preserved: true },
      instructions: 'System guidance for OpenAI responses pixel fixture. '.repeat(360),
      input: [
        { role: 'user', content: 'Please answer using the native tool when useful.' },
      ],
      tools: [{
        type: 'function',
        name: 'do_thing',
        description: 'Detailed tool description for OpenAI responses fixture. '.repeat(180),
        parameters: toolParameters,
      }],
    },
    transform: openai.transformOpenAIResponses,
  },
];

const manifest = { entries: [] };

for (const fixture of fixtures) {
  const inputFile = `${fixture.id}-input.json`;
  const outputFile = `${fixture.id}-output.json`;
  const body = enc.encode(JSON.stringify(fixture.input));
  const result = await fixture.transform(body, {
    charsPerToken: 1,
    minCompressChars: 1,
    collapseHistory: false,
  });
  const output = JSON.parse(dec.decode(result.body));
  writeFileSync(join(outDir, inputFile), JSON.stringify(fixture.input, null, 2) + '\n');
  writeFileSync(join(outDir, outputFile), JSON.stringify(output, null, 2) + '\n');
  const imageData = [];
  collectDataImages(output, imageData);
  const images = [];
  for (let i = 0; i < imageData.length; i++) {
    const file = `${fixture.id}-${String(i).padStart(2, '0')}.png`;
    writeFileSync(join(outDir, file), Buffer.from(imageData[i], 'base64'));
    images.push({ file });
  }
  manifest.entries.push({
    id: fixture.id,
    shape: fixture.shape,
    inputFile,
    outputFile,
    options: { charsPerToken: 1, minCompressChars: 1, collapseHistory: false },
    imageCount: images.length,
    images,
  });
}

writeFileSync(join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
console.log(`wrote ${manifest.entries.length} OpenAI transform fixtures to ${outDir}`);

function collectDataImages(value, out) {
  if (typeof value === 'string') {
    const match = /^data:image\/png;base64,(.+)$/.exec(value);
    if (match) out.push(match[1]);
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) collectDataImages(item, out);
    return;
  }
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) collectDataImages(child, out);
  }
}
