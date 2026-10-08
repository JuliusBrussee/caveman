import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  CGO_RELEASE_BINARIES,
  RELEASE_BINARIES,
  RELEASE_TARGETS,
  RELEASE_ZIG_VERSION,
  releaseArtifactName,
  releaseArtifactNames,
  releaseGoBuild,
  writeDarwinLinkStubs,
} from "../../scripts/build-release-binaries.mjs";
import {
  assertManifestNamesRelease,
  checksumSignatureBundle,
  verifyChecksumSignatureBundle,
} from "../../scripts/sign-binary-checksums.mjs";

test("release matrix contains six binaries for six OS/architecture targets", () => {
  const names = releaseArtifactNames();
  assert.equal(names.length, 36);
  assert.equal(new Set(names).size, 36);
  assert.ok(names.includes("caveman-proxy_win32_amd64"));
  assert.ok(names.includes("caveman-shrink_win32_arm64"));
  assert.equal(releaseArtifactName("cavemem", "windows", "amd64"), "cavemem_win32_amd64");
});

// #1020: without cgo the code compressor parses Go only, so the proxy and the
// engine must be cgo builds on every target; the other four stay pure Go.
test("caveman-proxy and caveman-engine build with cgo through zig on every target", () => {
  assert.deepEqual([...CGO_RELEASE_BINARIES].sort(), ["caveman-engine", "caveman-proxy"]);
  for (const [goos, arch] of RELEASE_TARGETS) {
    for (const [name] of RELEASE_BINARIES) {
      const { env, args } = releaseGoBuild(name, goos, arch, { zig: "/z dir/zig", darwinStubs: "/stubs" });
      assert.ok(args.includes("-trimpath"));
      if (!CGO_RELEASE_BINARIES.includes(name)) {
        assert.equal(env.CGO_ENABLED, "0", name);
        assert.equal(env.CC, undefined, name);
        continue;
      }
      assert.equal(env.CGO_ENABLED, "1", `${name} ${goos}/${arch}`);
      // Go splits CC on spaces but honors quotes, so a zig path with a space survives.
      assert.match(env.CC, /^'\/z dir\/zig' cc -target \S+-(macos\.12\.0|linux-musl|windows-gnu)$/);
      // Pure-Go DNS and user lookup on Linux only, where the old pure-Go build had
      // them anyway. darwin and windows keep the system resolver (getaddrinfo,
      // GetAddrInfoW) they used before: scoped VPN DNS, .local, NRPT.
      if (goos === "linux") assert.equal(args[args.indexOf("-tags") + 1], "netgo,osusergo");
      else assert.ok(!args.includes("-tags"), `${name} ${goos}/${arch} must not force the Go resolver`);
      const ldflags = args[args.indexOf("-ldflags") + 1];
      assert.equal(ldflags, {
        linux: "-buildid= -w -linkmode external -extldflags '-static -s'",
        darwin: "-buildid= -w -extldflags '-L/stubs -F/stubs -Wl,-S'",
        windows: "-buildid= -w -extldflags '-Wl,-Brepro -s'",
      }[goos]);
    }
  }
});

test("both workflows download the zig version the build script requires", () => {
  for (const workflow of ["release-binaries.yml", "engine-ci.yml"]) {
    const text = readFileSync(new URL(`../../.github/workflows/${workflow}`, import.meta.url), "utf8");
    const urls = text.match(/https:\/\/ziglang\.org\/download\/\S+/g) ?? [];
    assert.deepEqual(urls, [`https://ziglang.org/download/${RELEASE_ZIG_VERSION}/zig-x86_64-linux-${RELEASE_ZIG_VERSION}.tar.xz`], workflow);
  }
});

test("darwin link stubs name every non-libSystem library the Go toolchain imports", () => {
  const goroot = mkdtempSync(join(tmpdir(), "cave-goroot-"));
  const stubs = mkdtempSync(join(tmpdir(), "cave-stubs-"));
  try {
    mkdirSync(join(goroot, "src", "crypto", "macos"), { recursive: true });
    writeFileSync(join(goroot, "src", "crypto", "macos", "security.go"), [
      '//go:cgo_import_dynamic x509_SecTrustEvaluate SecTrustEvaluate "/System/Library/Frameworks/Security.framework/Versions/A/Security"',
      '//go:cgo_import_dynamic x509_CFRelease CFRelease "/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"',
      '//go:cgo_import_dynamic libresolv_res_9_ninit res_9_ninit "/usr/lib/libresolv.9.dylib"',
      '//go:cgo_import_dynamic libc_getpid getpid "/usr/lib/libSystem.B.dylib"',
      "",
    ].join("\n"));
    writeFileSync(join(goroot, "src", "crypto", "macos", "x_test.go"),
      '//go:cgo_import_dynamic t_X X "/usr/lib/libtestonly.dylib"\n');
    assert.deepEqual(writeDarwinLinkStubs(goroot, stubs), [
      "/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation",
      "/System/Library/Frameworks/Security.framework/Versions/A/Security",
      "/usr/lib/libresolv.9.dylib",
    ]);
    const security = readFileSync(join(stubs, "Security.framework", "Security.tbd"), "utf8");
    assert.match(security, /install-name: '\/System\/Library\/Frameworks\/Security\.framework\/Versions\/A\/Security'/);
    assert.match(security, /symbols: \[ _SecTrustEvaluate \]/);
    assert.match(readFileSync(join(stubs, "libresolv.tbd"), "utf8"), /symbols: \[ _res_9_ninit \]/);
  } finally {
    rmSync(goroot, { recursive: true, force: true });
    rmSync(stubs, { recursive: true, force: true });
  }
});

// Released binaries reported version "dev": the build passed no -ldflags, so
// `var version` in main.go kept its default. A stub `go` records each build;
// a stub `zig` and an empty GOROOT satisfy the cgo build's preflight.
test("release binaries are stamped with the pinned release tag", { skip: process.platform === "win32" }, () => {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
  const dir = mkdtempSync(join(tmpdir(), "release-stamp-"));
  try {
    const log = join(dir, "go-args.log");
    const goroot = join(dir, "goroot");
    mkdirSync(join(goroot, "src"), { recursive: true });
    writeFileSync(join(dir, "go"), `#!/bin/sh
if [ "$1" = "env" ]; then echo "${goroot}"; exit 0; fi
printf '%s\\n' "$*" >> "${log}"
while [ $# -gt 0 ]; do if [ "$1" = "-o" ]; then shift; : > "$1"; fi; shift; done
`, { mode: 0o755 });
    writeFileSync(join(dir, "zig"), `#!/bin/sh\necho ${RELEASE_ZIG_VERSION}\n`, { mode: 0o755 });
    const result = spawnSync(process.execPath, ["scripts/build-release-binaries.mjs", "--target", "linux/amd64", "--out", join(dir, "out")], {
      cwd: root,
      env: { ...process.env, PATH: `${dir}${delimiter}${process.env.PATH}`, ZIG: join(dir, "zig") },
      encoding: "utf8",
    });
    assert.equal(result.status, 0, result.stderr);
    const tag = readFileSync(join(root, "packages", "cli", "BINARY_RELEASE"), "utf8").trim();
    const builds = readFileSync(log, "utf8").trim().split("\n");
    assert.equal(builds.length, 6);
    for (const args of builds) assert.ok(args.includes(`-ldflags -X main.version=${tag} `), args);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("checksum signer emits bundle accepted by pinned-key verifier contract", () => {
  const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const privatePEM = privateKey.export({ type: "pkcs8", format: "pem" });
  const publicPEM = publicKey.export({ type: "spki", format: "pem" });
  const checksums = Buffer.from(`${"a".repeat(64)}  caveman-proxy_win32_amd64\n`);
  const bundle = checksumSignatureBundle(checksums, privatePEM);
  assert.equal(verifyChecksumSignatureBundle(checksums, bundle, publicPEM), true);
  assert.equal(verifyChecksumSignatureBundle(Buffer.from("changed"), bundle, publicPEM), false);
});

test("checksum signer refuses a manifest that does not name its release", () => {
  const entry = (tag) => `${createHash("sha256").update(`${tag}\n`).digest("hex")}  RELEASE\n`;
  const binaries = `${"a".repeat(64)}  caveman-proxy_win32_amd64\n`;
  assertManifestNamesRelease(binaries + entry("bin-v2.0.0"), "bin-v2.0.0");
  assert.throws(() => assertManifestNamesRelease(binaries, "bin-v2.0.0"), /exactly one RELEASE entry/);
  assert.throws(() => assertManifestNamesRelease(binaries + entry("bin-v1.9.9"), "bin-v2.0.0"), /exactly one RELEASE entry/);
  assert.throws(() => assertManifestNamesRelease(binaries + entry("bin-v2.0.0").repeat(2), "bin-v2.0.0"), /exactly one RELEASE entry/);
});
