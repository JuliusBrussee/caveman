// The signed module index and the module lockfile.
//
// modules.json (scripts/gen-modules-index.mjs) lists, per module, the binaries
// it needs and each platform build's sha256. It is signed through the
// release's checksums.txt: its sha256 is a line of that manifest, which the
// pinned release key signs (checksums.txt.keysig) and which names its release.
// Same key, same verifier as `caveman setup --install`.
//
// ~/.caveman/modules.lock.json records, per module, the release asked for, the
// release installed and the sha256 of each binary on disk.
import { createHash, randomBytes } from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { BINARY_RELEASE, BINARY_RELEASE_BASE_DEFAULT } from "../binaries.generated.js";
import {
  binaryInstallFilename,
  cavemanHome,
  cleanupPartial,
  downloadReleaseBinary,
  ensureCavemanHome,
  fetchReleaseAsset,
  parseSignedChecksums,
  removeAsideBinaries,
  replaceBinary,
  setupPlatform,
  setupTimeoutSeconds,
  sha256File,
  verifyChecksumSignature,
} from "../index.js";
import { trimTrailingSlashes } from "../provider-routing.js";
import type { ModuleId } from "./registry.js";

export type ModuleBuild = { os: string; arch: string; name: string; sha256: string };
export type ModuleIndexEntry = {
  id: ModuleId;
  kind: "stage" | "pack" | "tool";
  defaultOn: boolean;
  needsSignIn: boolean;
  binaries: Record<string, ModuleBuild[]>;
};
export type ModuleIndex = { schema: "caveman.modules.v1"; release: string; modules: ModuleIndexEntry[] };

export type ModuleLockEntry = { asked: string; installed: string; binaries: Record<string, string> };
export type ModuleLock = { schema: "caveman.modules.lock.v1"; modules: Partial<Record<ModuleId, ModuleLockEntry>> };

function releaseBase(): string {
  return `${trimTrailingSlashes(process.env.CAVE_BINARY_RELEASE_BASE ?? BINARY_RELEASE_BASE_DEFAULT)}/${BINARY_RELEASE}`;
}

// A name only this process owns, so concurrent installs (or setup --install,
// which uses `<target>.part`) never write, hash or publish each other's file.
function uniquePath(path: string, suffix: string): string {
  return `${path}.${process.pid}.${randomBytes(6).toString("hex")}.${suffix}`;
}

async function fetchBytes(url: string, timeoutSeconds: number): Promise<Buffer> {
  try {
    return Buffer.from(await (await fetchReleaseAsset(url, timeoutSeconds)).arrayBuffer());
  } catch (error) {
    throw new Error(`could not download the module index (${url}: ${(error as Error).message}) — check the network and try again`);
  }
}

// Fetches modules.json of the pinned release and refuses it unless the signed
// checksums.txt lists its exact bytes.
// The pinned release was cut before modules.json existed. Its checksums.txt is
// signed and simply does not list one, so callers may fall back to the full
// signed install; every other failure here is a refusal.
export class NoModuleIndexError extends Error {}

export async function loadModuleIndex(): Promise<ModuleIndex> {
  const base = releaseBase();
  const timeout = setupTimeoutSeconds();
  const [checksumsRaw, signature] = (await Promise.all([
    fetchBytes(`${base}/checksums.txt`, timeout),
    fetchBytes(`${base}/checksums.txt.keysig`, timeout),
  ])).map((bytes) => bytes.toString("utf8"));
  if (!verifyChecksumSignature(checksumsRaw!, signature!)) {
    throw new Error(`signature check failed for checksums.txt of ${BINARY_RELEASE} — refusing modules.json`);
  }
  let expected: string | undefined;
  try {
    expected = parseSignedChecksums(checksumsRaw!).get("modules.json");
  } catch (error) {
    throw new Error(`signature check failed for checksums.txt (${(error as Error).message}) — refusing modules.json`);
  }
  if (!expected) throw new NoModuleIndexError(`${BINARY_RELEASE} has no signed modules.json — refusing to read modules`);
  const raw = await fetchBytes(`${base}/modules.json`, timeout);
  if (createHash("sha256").update(raw).digest("hex") !== expected) {
    throw new Error(`signature check failed for modules.json of ${BINARY_RELEASE} — refusing it`);
  }
  const index = JSON.parse(raw.toString("utf8")) as ModuleIndex;
  if (index.schema !== "caveman.modules.v1" || index.release !== BINARY_RELEASE || !Array.isArray(index.modules)) {
    throw new Error(`modules.json of ${BINARY_RELEASE} is not a caveman.modules.v1 index for that release`);
  }
  return index;
}

function lockPath(): string {
  return join(cavemanHome(), "modules.lock.json");
}

// A missing or unreadable lockfile is an empty one: every binary it would name
// is re-checked against its signed digest before use anyway.
export function readLock(): ModuleLock {
  try {
    const parsed = JSON.parse(readFileSync(lockPath(), "utf8")) as ModuleLock;
    if (parsed.schema === "caveman.modules.lock.v1" && parsed.modules && typeof parsed.modules === "object") return parsed;
  } catch {
    // fall through
  }
  return { schema: "caveman.modules.lock.v1", modules: {} };
}

export function writeLock(lock: ModuleLock): void {
  ensureCavemanHome();
  const path = lockPath();
  const temp = uniquePath(path, "tmp");
  writeFileSync(temp, `${JSON.stringify(lock, null, 2)}\n`, { mode: 0o600 });
  chmodSync(temp, 0o600);
  renameSync(temp, path);
}

// Downloads the binaries the named modules need into ~/.caveman/bin, each
// checked against the signed index, and records each module in the lockfile
// as soon as its binaries are in place. A signature or network failure throws;
// a module with no build for this platform is skipped and named in
// `problems`, so the others still install.
export async function ensureModuleBinaries(ids: ModuleId[], downloading?: (name: string) => void): Promise<{ lock: ModuleLock; problems: string[] }> {
  const platform = setupPlatform();
  const index = await loadModuleIndex();
  const timeout = setupTimeoutSeconds();
  const binDir = join(ensureCavemanHome(), "bin");
  mkdirSync(binDir, { recursive: true, mode: 0o700 });
  removeAsideBinaries(binDir);
  const lock = readLock();
  const problems: string[] = [];
  for (const id of ids) {
    const entry = index.modules.find((module) => module.id === id);
    if (!entry) {
      problems.push(`${id}: not in modules.json of ${index.release}`);
      continue;
    }
    const picked = Object.entries(entry.binaries).map(([binary, builds]) => {
      if (!/^[a-z0-9-]+$/.test(binary)) throw new Error(`modules.json of ${index.release} names an invalid binary ${JSON.stringify(binary)}`);
      return [binary, builds.find((build) => build.os === platform.os && build.arch === platform.arch)] as const;
    });
    const missing = picked.filter(([, build]) => !build).map(([binary]) => binary);
    if (missing.length > 0) {
      problems.push(`${id}: ${missing.join(", ")} has no ${platform.os}/${platform.arch} build in ${index.release}`);
      continue;
    }
    const digests: Record<string, string> = {};
    for (const [binary, build] of picked) {
      const { name, sha256 } = build!;
      if (name !== `${binary}_${platform.os}_${platform.arch}` || !/^[a-f0-9]{64}$/.test(sha256)) {
        throw new Error(`modules.json of ${index.release} has an invalid ${binary} build entry`);
      }
      const target = join(binDir, binaryInstallFilename(binary, platform.os));
      digests[binary] = sha256;
      if (sha256File(target) === sha256) continue;
      const part = uniquePath(target, "part");
      downloading?.(binary);
      try {
        let got: string;
        try {
          got = (await downloadReleaseBinary(`${releaseBase()}/${name}`, part, timeout)).sha256;
        } catch (error) {
          throw new Error(`the ${id} module could not download ${name} (${(error as Error).message}) — check the network, then retry with \`caveman on ${id}\``);
        }
        if (got !== sha256) throw new Error(`signature check failed for ${name} — refusing to install the ${id} module; partial download deleted`);
        chmodSync(part, 0o755);
        replaceBinary(part, target);
      } catch (error) {
        cleanupPartial(part);
        throw error;
      }
    }
    lock.modules[id] = { asked: BINARY_RELEASE, installed: index.release, binaries: digests };
    writeLock(lock);
  }
  return { lock, problems };
}
