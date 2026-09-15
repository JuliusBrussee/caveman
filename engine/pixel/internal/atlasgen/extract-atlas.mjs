#!/usr/bin/env node
import { gzipSync } from 'node:zlib';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join, resolve } from 'node:path';

const HEADER = {
  atlas: { ATLAS_CELL_W: 5, ATLAS_CELL_H: 8 },
  gray: { ATLAS_GRAY_CELL_W: 5, ATLAS_GRAY_CELL_H: 8 },
};

function usage() {
  console.error('usage: node public/engine/pixel/internal/atlasgen/extract-atlas.mjs <pxpipe-root>');
  process.exit(2);
}

const pxpipeRoot = process.argv[2] ? resolve(process.argv[2]) : '';
if (!pxpipeRoot) usage();

const repoRoot = resolve(new URL('../../../../..', import.meta.url).pathname);
const outDir = join(repoRoot, 'public/engine/pixel/assets');
mkdirSync(outDir, { recursive: true });

function readSource(rel) {
  return readFileSync(join(pxpipeRoot, rel), 'utf8');
}

function constNumber(src, name) {
  const re = new RegExp(`export\\s+const\\s+${name}\\s*=\\s*(\\d+)\\s*;`);
  const m = src.match(re);
  if (!m) throw new Error(`[extract-atlas] missing numeric const ${name}`);
  return Number(m[1]);
}

function constB64(src, name) {
  const re = new RegExp(`const\\s+${name}\\s*=\\s*"([^"]+)"\\s*;`);
  const m = src.match(re);
  if (!m) throw new Error(`[extract-atlas] missing base64 const ${name}`);
  return Buffer.from(m[1], 'base64');
}

function assertHeader(src, expected) {
  for (const [name, want] of Object.entries(expected)) {
    const got = constNumber(src, name);
    if (got !== want) {
      throw new Error(`[extract-atlas] ${name}=${got}, want ${want}`);
    }
  }
}

function writeBlob(name, bytes) {
  const out = join(outDir, name);
  writeFileSync(out, gzipSync(bytes, { level: 9 }));
  console.log(`${basename(out)} raw=${bytes.length} gzip=${readFileSync(out).length}`);
}

const atlas = readSource('src/core/atlas.ts');
const gray = readSource('src/core/atlas-gray.ts');
assertHeader(atlas, HEADER.atlas);
assertHeader(gray, HEADER.gray);

writeBlob('atlas-codepoints.bin.gz', constB64(atlas, 'CODEPOINTS_B64'));
writeBlob('atlas-offsets.bin.gz', constB64(atlas, 'OFFSETS_B64'));
writeBlob('atlas-wideflags.bin.gz', constB64(atlas, 'WIDE_FLAGS_B64'));
writeBlob('atlas-pixels.bin.gz', constB64(atlas, 'PIXELS_B64'));
writeBlob('atlasgray-codepoints.bin.gz', constB64(gray, 'GRAY_CODEPOINTS_B64'));
writeBlob('atlasgray-offsets.bin.gz', constB64(gray, 'GRAY_OFFSETS_B64'));
writeBlob('atlasgray-wideflags.bin.gz', constB64(gray, 'GRAY_WIDE_FLAGS_B64'));
writeBlob('atlasgray-pixels.bin.gz', constB64(gray, 'GRAY_PIXELS_B64'));
