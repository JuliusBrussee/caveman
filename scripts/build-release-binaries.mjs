#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export const RELEASE_BINARIES = Object.freeze([
  ["caveman-proxy", "./proxy/cmd/caveman-proxy"],
  ["caveman-engine", "./engine/cmd/caveman-engine"],
  ["caveman-mcp", "./mcp/cmd/caveman-mcp"],
  ["cavemem", "./mem/cmd/cavemem"],
  ["caveman-browse", "./browse/cmd/caveman-browse"],
  ["caveman-shrink", "./shrink/cmd/caveman-shrink"],
]);

export const RELEASE_TARGETS = Object.freeze([
  ["darwin", "arm64"],
  ["darwin", "amd64"],
  ["linux", "arm64"],
  ["linux", "amd64"],
  ["windows", "arm64"],
  ["windows", "amd64"],
]);

export function releaseArtifactName(name, goos, arch) {
  const os = goos === "windows" ? "win32" : goos;
  return `${name}_${os}_${arch}`;
}

export function releaseArtifactNames(targets = RELEASE_TARGETS) {
  return targets.flatMap(([goos, arch]) =>
    RELEASE_BINARIES.map(([name]) => releaseArtifactName(name, goos, arch)));
}

function parseArgs(argv) {
  let out = resolve(root, "dist", "binaries");
  const targets = [];
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index];
    if (arg === "--out") out = resolve(argv[++index] ?? "");
    else if (arg === "--target") {
      const value = argv[++index] ?? "";
      const [goos, arch] = value.split("/");
      if (!RELEASE_TARGETS.some(([knownOS, knownArch]) => knownOS === goos && knownArch === arch)) {
        throw new Error(`unsupported release target ${JSON.stringify(value)}`);
      }
      targets.push([goos, arch]);
    } else if (arg === "--list") return { out, targets: RELEASE_TARGETS, list: true };
    else throw new Error(`unknown argument ${arg}`);
  }
  return { out, targets: targets.length ? targets : RELEASE_TARGETS, list: false };
}

function build({ out, targets }) {
  mkdirSync(out, { recursive: true });
  const artifacts = [];
  for (const [goos, arch] of targets) {
    for (const [name, packagePath] of RELEASE_BINARIES) {
      const artifact = releaseArtifactName(name, goos, arch);
      const output = join(out, artifact);
      process.stderr.write(`build ${artifact}\n`);
      const result = spawnSync("go", ["build", "-trimpath", "-o", output, packagePath], {
        cwd: root,
        env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: arch },
        stdio: "inherit",
      });
      if (result.error) throw result.error;
      if (result.status !== 0) throw new Error(`go build failed for ${artifact}`);
      artifacts.push(artifact);
    }
  }
  artifacts.sort();
  const checksums = artifacts.map((artifact) => {
    const digest = createHash("sha256").update(readFileSync(join(out, artifact))).digest("hex");
    return `${digest}  ${artifact}`;
  }).join("\n") + "\n";
  writeFileSync(join(out, "checksums.txt"), checksums, { mode: 0o600 });
  return artifacts;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseArgs(process.argv.slice(2));
    if (options.list) process.stdout.write(`${releaseArtifactNames(options.targets).sort().join("\n")}\n`);
    else process.stdout.write(`built ${build(options).length} signed-release inputs in ${options.out}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
}
