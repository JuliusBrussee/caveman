import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { SHIPPABLE_FILES, verifyExtensionRoot } from "./verify-extension-stage.mjs";

const CRC_TABLE = Array.from({ length: 256 }, (_, value) => {
  let crc = value;
  for (let bit = 0; bit < 8; bit++) crc = (crc & 1) ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1;
  return crc >>> 0;
});

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");
const stage = join(dist, "stage");
const source = verifyExtensionRoot(root);
const packageJSON = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
if (packageJSON.version !== source.manifest.version) {
  throw new Error(`package version ${packageJSON.version} does not match manifest ${source.manifest.version}`);
}

rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
for (const file of SHIPPABLE_FILES) {
  const target = join(stage, file);
  mkdirSync(dirname(target), { recursive: true });
  copyFileSync(join(root, file), target);
}
verifyExtensionRoot(stage, { exact: true });

const output = join(dist, `caveman-browser-${source.manifest.version}.zip`);
writeFileSync(output, createZip(stage, SHIPPABLE_FILES));
process.stdout.write(`${output}\n`);

function crc32(bytes) {
  let crc = 0xffffffff;
  for (const byte of bytes) {
    crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function localHeader(name, bytes, checksum) {
  const header = Buffer.alloc(30);
  header.writeUInt32LE(0x04034b50, 0);
  header.writeUInt16LE(20, 4);
  header.writeUInt16LE(0, 6);
  header.writeUInt16LE(0, 8);
  header.writeUInt16LE(0, 10);
  header.writeUInt16LE(33, 12);
  header.writeUInt32LE(checksum, 14);
  header.writeUInt32LE(bytes.length, 18);
  header.writeUInt32LE(bytes.length, 22);
  header.writeUInt16LE(name.length, 26);
  header.writeUInt16LE(0, 28);
  return header;
}

function centralHeader(name, bytes, checksum, offset) {
  const header = Buffer.alloc(46);
  header.writeUInt32LE(0x02014b50, 0);
  header.writeUInt16LE(0x0314, 4);
  header.writeUInt16LE(20, 6);
  header.writeUInt16LE(0, 8);
  header.writeUInt16LE(0, 10);
  header.writeUInt16LE(0, 12);
  header.writeUInt16LE(33, 14);
  header.writeUInt32LE(checksum, 16);
  header.writeUInt32LE(bytes.length, 20);
  header.writeUInt32LE(bytes.length, 24);
  header.writeUInt16LE(name.length, 28);
  header.writeUInt16LE(0, 30);
  header.writeUInt16LE(0, 32);
  header.writeUInt16LE(0, 34);
  header.writeUInt16LE(0, 36);
  header.writeUInt32LE((0o100644 << 16) >>> 0, 38);
  header.writeUInt32LE(offset, 42);
  return header;
}

function endRecord(count, centralSize, centralOffset) {
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(count, 8);
  end.writeUInt16LE(count, 10);
  end.writeUInt32LE(centralSize, 12);
  end.writeUInt32LE(centralOffset, 16);
  end.writeUInt16LE(0, 20);
  return end;
}

function createZip(rootDir, files) {
  const locals = [];
  const central = [];
  let offset = 0;
  for (const file of [...files].sort()) {
    const name = Buffer.from(file, "utf8");
    const bytes = readFileSync(join(rootDir, file));
    const checksum = crc32(bytes);
    const header = localHeader(name, bytes, checksum);
    locals.push(header, name, bytes);
    central.push(centralHeader(name, bytes, checksum, offset), name);
    offset += header.length + name.length + bytes.length;
  }
  const centralBuffer = Buffer.concat(central);
  return Buffer.concat([...locals, centralBuffer, endRecord(files.length, centralBuffer.length, offset)]);
}
