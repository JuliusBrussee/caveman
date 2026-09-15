#!/usr/bin/env node
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

function usage() {
  console.error('usage: pnpm exec tsx public/engine/pixel/internal/atlasgen/gen-golden.mjs <pxpipe-root>');
  process.exit(2);
}

const pxpipeRoot = process.argv[2] ? resolve(process.argv[2]) : '';
if (!pxpipeRoot) usage();

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '../../../../..');
const outDir = join(repoRoot, 'public/engine/pixel/testdata/golden');
rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const render = await import(pathToFileURL(join(pxpipeRoot, 'src/core/render.ts')).href);

const corpus = [
  {
    id: 'ascii_code',
    text: [
      'func main() {',
      '\tfmt.Println("caveman pixel")   ',
      '}',
      '',
      '// byte-stable render sample',
    ].join('\n'),
  },
  {
    id: 'json_tabs',
    text: '{\n\t"tool": "Read",\n\t"path": "/tmp/project/file.test.ts",   \n\t"ok": true\n}\n',
  },
  {
    id: 'cjk_emoji',
    text: 'English 中國 日本語 한글 😀 mixed width\n第二行 with symbols ✓ ✗ ⚠',
  },
  {
    id: 'literal_newline_arrow',
    text: 'This source contains a literal ↵ marker and a real newline\nnext row.',
  },
  {
    id: 'forty_k_log',
    text: Array.from({ length: 1000 }, (_, i) =>
      `2026-07-04T12:${String(i % 60).padStart(2, '0')}:00Z INFO worker=${i % 7} request=req-${String(i).padStart(5, '0')} status=ok bytes=${40000 + i}`,
    ).join('\n'),
  },
];

const entries = [];

async function addEntry(input, mode, renderFn, options) {
  const images = await renderFn(input.text);
  const imageMeta = [];
  for (let i = 0; i < images.length; i++) {
    const file = `${input.id}-${mode}-${String(i).padStart(2, '0')}.png`;
    writeFileSync(join(outDir, file), Buffer.from(images[i].png));
    imageMeta.push({
      file,
      width: images[i].width,
      height: images[i].height,
      charsRendered: images[i].charsRendered,
      droppedChars: images[i].droppedChars,
    });
  }
  entries.push({
    id: `${input.id}/${mode}`,
    text: input.text,
    mode,
    options,
    images: imageMeta,
  });
}

for (const input of corpus) {
  await addEntry(
    input,
    'single',
    (text) => render.renderTextToPngs(text, 80, {}),
    { cols: 80, style: {} },
  );
  await addEntry(
    input,
    'multicol2',
    (text) => render.renderTextToPngsMultiCol(text, 80, 2),
    { cols: 80, multiCol: 2 },
  );
  await addEntry(
    input,
    'dense',
    (text) => render.renderDensePages(text, { cols: 312, reflow: true }),
    { cols: 312, dense: true, reflow: true },
  );
}

writeFileSync(join(outDir, 'manifest.json'), JSON.stringify({ entries }, null, 2) + '\n');
console.log(`wrote ${entries.length} golden entries to ${outDir}`);
