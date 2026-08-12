#!/usr/bin/env node
// Generates the Caveman Mode extension icon: the pixel-flame brand mark (a
// dot-matrix ember) on the near-black brand tile — the same logo as the site's
// favicon.svg. Zero dependencies; `sips` downscales the supersampled master.
// Re-run: `node generate-icons.js` from this folder.
const fs = require("fs");
const zlib = require("zlib");
const path = require("path");

const S = 512; // master render size
const buf = Buffer.alloc(S * S * 4); // RGBA
const u = S / 48; // brand artwork is authored on a 48-unit grid (favicon.svg)

const TILE = [14, 12, 10]; // #0e0c0a near-black brand tile

// ember gradient stops (favicon.svg): gold -> orange -> deep, top to bottom
const STOPS = [
  [0.0, [255, 206, 107]], // #ffce6b
  [0.55, [242, 121, 43]], // #f2792b
  [1.0, [207, 74, 31]], // #cf4a1f
];
function emberAt(t) {
  for (let i = 1; i < STOPS.length; i++) {
    if (t <= STOPS[i][0]) {
      const [t0, c0] = STOPS[i - 1];
      const [t1, c1] = STOPS[i];
      const k = (t - t0) / (t1 - t0);
      return c0.map((v, j) => Math.round(v + (c1[j] - v) * k));
    }
  }
  return STOPS[STOPS.length - 1][1];
}

function px(x, y, [r, g, b], a = 255) {
  if (x < 0 || y < 0 || x >= S || y >= S) return;
  const i = (y * S + x) * 4;
  const ia = a / 255;
  buf[i] = Math.round(buf[i] * (1 - ia) + r * ia);
  buf[i + 1] = Math.round(buf[i + 1] * (1 - ia) + g * ia);
  buf[i + 2] = Math.round(buf[i + 2] * (1 - ia) + b * ia);
  buf[i + 3] = Math.max(buf[i + 3], Math.round(255 * ia));
}

// 3x3 supersampled coverage of a rounded rect
function roundRectCov(x, y, x0, y0, x1, y1, r) {
  let hit = 0;
  for (let sx = 0; sx < 3; sx++)
    for (let sy = 0; sy < 3; sy++) {
      const px2 = x + (sx + 0.5) / 3;
      const py2 = y + (sy + 0.5) / 3;
      let inside = px2 >= x0 && px2 <= x1 && py2 >= y0 && py2 <= y1;
      if (inside && r > 0) {
        const corners = [
          [x0 + r, y0 + r],
          [x1 - r, y0 + r],
          [x0 + r, y1 - r],
          [x1 - r, y1 - r],
        ];
        for (const [cx, cy] of corners) {
          const inCornerBox =
            (px2 < cx) === (cx === x0 + r) && (py2 < cy) === (cy === y0 + r);
          if (inCornerBox) {
            const dx = px2 - cx;
            const dy = py2 - cy;
            if (dx * dx + dy * dy > r * r) inside = false;
          }
        }
      }
      if (inside) hit++;
    }
  return hit / 9;
}

// brand tile (favicon: rect 48x48 rx10)
for (let y = 0; y < S; y++)
  for (let x = 0; x < S; x++) {
    const cov = roundRectCov(x, y, 0, 0, S, S, 10 * u);
    if (cov > 0) px(x, y, TILE, Math.round(255 * cov));
  }

// flame: the 8 horizontal bars from favicon.svg, colored by vertical position
const BARS = [
  [20, 4, 9],
  [15, 9, 19],
  [15, 14, 19],
  [10, 19, 29],
  [10, 24, 29],
  [5, 29, 39],
  [10, 34, 29],
  [15, 39, 19],
];
const BAR_H = 4;
for (const [bx, by, bw] of BARS) {
  const x0 = bx * u,
    y0 = by * u,
    x1 = (bx + bw) * u,
    y1 = (by + BAR_H) * u;
  for (let y = Math.floor(y0); y < Math.ceil(y1); y++)
    for (let x = Math.floor(x0); x < Math.ceil(x1); x++) {
      const cov = roundRectCov(x, y, x0, y0, x1, y1, 0.5 * u);
      if (cov > 0) {
        const t = (y / S - 2 / 48) / (42 / 48); // map y to gradient (y=2..44)
        px(x, y, emberAt(Math.max(0, Math.min(1, t))), Math.round(255 * cov));
      }
    }
}

// encode PNG
function crc32(b) {
  let c = ~0;
  for (let i = 0; i < b.length; i++) {
    c ^= b[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const t = Buffer.from(type, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([t, data])), 0);
  return Buffer.concat([len, t, data, crc]);
}
const raw = Buffer.alloc((S * 4 + 1) * S);
for (let y = 0; y < S; y++) {
  raw[y * (S * 4 + 1)] = 0;
  buf.copy(raw, y * (S * 4 + 1) + 1, y * S * 4, (y + 1) * S * 4);
}
const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(S, 0);
ihdr.writeUInt32BE(S, 4);
ihdr[8] = 8;
ihdr[9] = 6;
const png = Buffer.concat([
  Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
  chunk("IHDR", ihdr),
  chunk("IDAT", zlib.deflateSync(raw, { level: 9 })),
  chunk("IEND", Buffer.alloc(0)),
]);
const master = path.join(__dirname, "icon-master.png");
fs.writeFileSync(master, png);
console.log("wrote", master, png.length, "bytes");
