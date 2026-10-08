#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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

// caveman-proxy and caveman-engine carry the tree-sitter code compressor, which
// is C. Built without cgo it parses Go only, and a Read of a TypeScript, Python
// or Rust file passes through uncompressed (#1020, #1120). Those two
// cross-compile with cgo through zig cc; the other four stay pure Go.
export const CGO_RELEASE_BINARIES = Object.freeze(["caveman-proxy", "caveman-engine"]);

// The zig the release is built with. release-binaries.yml and engine-ci.yml
// download exactly this version by sha256, and the build refuses any other.
export const RELEASE_ZIG_VERSION = "0.16.0";

// macOS 12 is Go's floor and what the internal linker stamps; zig defaults to 13.
const ZIG_TARGETS = Object.freeze({
  "darwin/arm64": "aarch64-macos.12.0",
  "darwin/amd64": "x86_64-macos.12.0",
  "linux/arm64": "aarch64-linux-musl",
  "linux/amd64": "x86_64-linux-musl",
  "windows/arm64": "aarch64-windows-gnu",
  "windows/amd64": "x86_64-windows-gnu",
});

// The go build environment and flags for one release artifact.
export function releaseGoBuild(name, goos, arch, { zig = "zig", darwinStubs = "", version = "" } = {}) {
  const env = { CGO_ENABLED: "0", GOOS: goos, GOARCH: arch };
  const args = ["-trimpath"];
  // -X main.version stamps the release tag over main.go's "dev" default. Only
  // caveman-proxy and caveman-mcp declare main.version; the linker ignores the
  // flag for the rest.
  const stamp = version ? [`-X main.version=${version}`] : [];
  if (!CGO_RELEASE_BINARIES.includes(name)) {
    if (stamp.length) args.push("-ldflags", stamp.join(" "));
    return { env, args };
  }
  env.CGO_ENABLED = "1";
  // Go splits CC on spaces but honors quotes, so a ZIG path with a space works.
  env.CC = `'${zig}' cc -target ${ZIG_TARGETS[`${goos}/${arch}`]}`;
  // cgo is for the grammars only. Linux keeps the pure-Go DNS and user lookup
  // its old CGO_ENABLED=0 build had, not static musl's. darwin and windows get no
  // tags: they always used the system resolver (libSystem getaddrinfo,
  // GetAddrInfoW), which netgo would replace, breaking scoped VPN DNS and .local.
  if (goos === "linux") args.push("-tags", "netgo,osusergo");
  // No debug info, which keeps the cgo builds reproducible and near the size of
  // the pure-Go ones. An externally linked build ID hashes link inputs that vary
  // between machines and Go caches (the stub directory below among them), and
  // -w also keeps Go from merging DWARF with the build machine's own dsymutil.
  const ldflags = [...stamp, "-buildid=", "-w"];
  // musl, fully static: one Linux binary for any glibc age and for Alpine.
  if (goos === "linux") ldflags.push("-linkmode external -extldflags '-static -s'");
  // zig's Mach-O UUID hashes the paths and mtimes of the objects in its debug
  // map, which sit in Go's temporary link directory; -Wl,-S drops that map.
  if (goos === "darwin") ldflags.push(`-extldflags '-L${darwinStubs} -F${darwinStubs} -Wl,-S'`);
  // lld stamps a timestamp and a PDB of the temporary link inputs into the PE;
  // -Brepro derives the stamp from content and -s drops the PDB.
  if (goos === "windows") ldflags.push("-extldflags '-Wl,-Brepro -s'");
  args.push("-ldflags", ldflags.join(" "));
  return { env, args };
}

// Go's darwin runtime binds libresolv, CoreFoundation and Security by install
// path (//go:cgo_import_dynamic). An external link needs a stub library for
// each, and zig ships only libSystem's; the others come with the macOS SDK,
// which is not licensed for use off Apple hardware. So the build writes text
// stubs (.tbd) listing exactly the symbols the Go toolchain in use imports, and
// dyld binds them to the real system libraries at run time.
export function writeDarwinLinkStubs(goroot, dir) {
  const imports = new Map();
  const directive = /^\/\/go:cgo_import_dynamic \S+ (\S+) "(\/(?:System|usr\/lib)\/[^"]+)"/gm;
  const src = join(goroot, "src");
  for (const file of readdirSync(src, { recursive: true })) {
    if (!file.endsWith(".go") || file.endsWith("_test.go") || file.includes("testdata")) continue;
    for (const [, symbol, path] of readFileSync(join(src, file), "utf8").matchAll(directive)) {
      if (path.endsWith("/libSystem.B.dylib")) continue;
      if (!imports.has(path)) imports.set(path, new Set());
      imports.get(path).add(`_${symbol}`);
    }
  }
  for (const [path, symbols] of imports) {
    const base = path.split("/").pop();
    const stub = path.includes(".framework/")
      ? join(dir, `${base}.framework`, `${base}.tbd`)
      : join(dir, `${base.split(".")[0]}.tbd`);
    mkdirSync(dirname(stub), { recursive: true });
    writeFileSync(stub, [
      "--- !tapi-tbd",
      "tbd-version: 4",
      "targets: [ x86_64-macos, arm64-macos ]",
      `install-name: '${path}'`,
      "exports:",
      "  - targets: [ x86_64-macos, arm64-macos ]",
      `    symbols: [ ${[...symbols].sort().join(", ")} ]`,
      "...",
      "",
    ].join("\n"));
  }
  return [...imports.keys()].sort();
}

function hostTarget() {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
  return [goos, arch];
}

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
  let test = false;
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
    else if (arg === "--test") test = true;
    else throw new Error(`unknown argument ${arg}`);
  }
  if (test) {
    // --test runs what it built, so it builds only this machine's target.
    const host = hostTarget();
    if (targets.some(([goos, arch]) => goos !== host[0] || arch !== host[1])) {
      throw new Error(`--test runs binaries, so it builds only the host target ${host.join("/")}`);
    }
    return { out, targets: [host], list: false, test };
  }
  return { out, targets: targets.length ? targets : RELEASE_TARGETS, list: false, test };
}

function run(command, args, env) {
  const result = spawnSync(command, args, { cwd: root, env: { ...process.env, ...env }, stdio: "inherit" });
  if (result.error) throw result.error;
  return result.status === 0;
}

function goEnv(name) {
  return spawnSync("go", ["env", name], { cwd: root, encoding: "utf8" }).stdout.trim();
}

function requireZig(zig) {
  const result = spawnSync(zig, ["version"], { encoding: "utf8" });
  const found = result.stdout?.trim() || "none";
  if (found !== RELEASE_ZIG_VERSION) {
    throw new Error(`release cgo binaries need zig ${RELEASE_ZIG_VERSION} (set ZIG=/path/to/zig); found ${found}`);
  }
}

// The release-shape check: the tests that fail without the tree-sitter
// compressor, compiled with caveman-proxy's own release flags, then the built
// caveman-engine's embedded evals, which include Python and TypeScript.
function testReleaseShape(out, [goos, arch], options) {
  const { env, args } = releaseGoBuild("caveman-proxy", goos, arch, options);
  if (!run("go", ["test", "-count=1", ...args, "-run", "^TestReleaseShape", "./engine", "./proxy/internal/gateway"], env)) {
    throw new Error("release-shape tests failed");
  }
  const engine = join(out, releaseArtifactName("caveman-engine", goos, arch));
  const evals = spawnSync(engine, ["evals", "run"], { encoding: "utf8", maxBuffer: 64 << 20 });
  if (evals.status !== 0) {
    process.stderr.write(evals.stdout ?? "");
    throw new Error(`${engine} evals run failed`);
  }
}

function build({ out, targets, test }) {
  mkdirSync(out, { recursive: true });
  // The pinned release tag, so caveman-proxy's and caveman-mcp's `version`
  // names its release instead of the "dev" default in main.go.
  const version = readFileSync(join(root, "packages", "cli", "BINARY_RELEASE"), "utf8").trim();
  const zig = process.env.ZIG || "zig";
  requireZig(zig);
  const darwinStubs = mkdtempSync(join(tmpdir(), "caveman-darwin-stubs-"));
  const artifacts = [];
  try {
    writeDarwinLinkStubs(goEnv("GOROOT"), darwinStubs);
    for (const [goos, arch] of targets) {
      for (const [name, packagePath] of RELEASE_BINARIES) {
        const artifact = releaseArtifactName(name, goos, arch);
        const { env, args } = releaseGoBuild(name, goos, arch, { zig, darwinStubs, version });
        process.stderr.write(`build ${artifact}${env.CGO_ENABLED === "1" ? " (cgo)" : ""}\n`);
        if (!run("go", ["build", ...args, "-o", join(out, artifact), packagePath], env)) {
          throw new Error(`go build failed for ${artifact}`);
        }
        artifacts.push(artifact);
      }
    }
    if (test) testReleaseShape(out, targets[0], { zig, darwinStubs });
  } finally {
    rmSync(darwinStubs, { recursive: true, force: true });
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
