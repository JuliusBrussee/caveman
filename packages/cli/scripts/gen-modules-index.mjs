#!/usr/bin/env node
// Builds modules.json, the module index of one binary release, from the module
// registry and the release's checksums.txt:
//
//   node packages/cli/scripts/gen-modules-index.mjs <checksums.txt> <bin-v tag> > modules.json
//
// The release workflow then lists modules.json in checksums.txt before that
// manifest is signed, so the index carries the release signature. Imports
// registry.ts directly: Node 22.18+ strips the types.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { MODULES } from "../src/modules/registry.ts";
import { RELEASE_BINARIES, RELEASE_TARGETS, releaseArtifactName } from "../../../scripts/build-release-binaries.mjs";

export const MODULES_SCHEMA = "caveman.modules.v1";

// stage: rewrites requests on the way to the model. pack: shapes what the
// agent does. tool: something the agent runs.
const KIND = { input: "stage", routing: "stage", output: "pack", "waste-fixes": "pack", scripts: "tool", browse: "tool" };

export function modulesIndex(checksums, release, modules = MODULES) {
  const digests = new Map();
  for (const line of String(checksums).split("\n")) {
    const match = line.match(/^([a-f0-9]{64}) {2}([A-Za-z0-9._-]+)$/);
    if (match) digests.set(match[2], match[1]);
    else if (line) throw new Error(`invalid checksums.txt line: ${JSON.stringify(line)}`);
  }
  const hub = new Set(RELEASE_BINARIES.map(([name]) => name));
  return {
    schema: MODULES_SCHEMA,
    release,
    modules: modules.map((module) => {
      const kind = KIND[module.id];
      if (!kind) throw new Error(`module ${module.id} has no kind; add it to gen-modules-index.mjs`);
      const binaries = {};
      for (const name of module.binaries) {
        if (!hub.has(name)) throw new Error(`module ${module.id} names ${name}, which the release does not build`);
        binaries[name] = builds(name, digests, true);
      }
      // An external binary (caveman-blocks) ships only the platforms its own
      // release builds; the CLI says which are missing.
      if (module.external) binaries[module.external.binary] = builds(module.external.binary, digests, false);
      return { id: module.id, kind, defaultOn: module.defaultOn, needsSignIn: module.needsSignIn, binaries };
    }),
  };
}

function builds(name, digests, everyTarget) {
  const out = [];
  for (const [goos, arch] of RELEASE_TARGETS) {
    const artifact = releaseArtifactName(name, goos, arch);
    const sha256 = digests.get(artifact);
    if (sha256) out.push({ os: goos === "windows" ? "win32" : goos, arch, name: artifact, sha256 });
    else if (everyTarget) throw new Error(`checksums.txt has no ${artifact}`);
  }
  if (out.length === 0) throw new Error(`checksums.txt has no ${name} build`);
  return out;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [checksumsPath, release] = process.argv.slice(2);
    if (!checksumsPath || !/^bin-v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(release ?? "")) {
      throw new Error("usage: gen-modules-index.mjs <checksums.txt> <bin-v tag>");
    }
    process.stdout.write(`${JSON.stringify(modulesIndex(readFileSync(checksumsPath, "utf8"), release), null, 2)}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
}
