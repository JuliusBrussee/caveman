import { createHash, createPublicKey, randomBytes, verify } from "node:crypto";
import {
  accessSync,
  chmodSync,
  constants,
  mkdirSync,
  readFileSync,
  renameSync,
  statSync,
  unlinkSync,
} from "node:fs";
import { open } from "node:fs/promises";
import { homedir } from "node:os";
import { delimiter, extname, isAbsolute, join } from "node:path";

import {
  BINARY_RELEASE,
  BINARY_RELEASE_BASE_DEFAULT,
  BINARY_SIGNING_PUBKEY,
} from "./release.generated.mjs";

function executable(path) {
  try {
    if (process.platform !== "win32") accessSync(path, constants.X_OK);
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

export function executableCandidateNames(
  name,
  platform = process.platform,
  pathExt = process.env.PATHEXT ?? ".COM;.EXE;.BAT;.CMD",
) {
  if (platform !== "win32" || extname(name)) return [name];
  const candidates = [];
  for (const extension of pathExt.split(";").map((value) => value.trim()).filter(Boolean)) {
    candidates.push(`${name}${extension.startsWith(".") ? extension : `.${extension}`}`);
  }
  // npm installs a POSIX shim beside its native .CMD shim. Windows X_OK is
  // existence-only, so native PATHEXT candidates must win before bare fallback.
  candidates.push(name);
  const seen = new Set();
  return candidates.filter((candidate) => {
    const key = candidate.toLowerCase();
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function onPath(name) {
  if (isAbsolute(name) || /[\\/]/.test(name)) return executable(name) ? name : null;
  for (const dir of (process.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue;
    for (const candidate of executableCandidateNames(name)) {
      const path = join(dir, candidate);
      if (executable(path)) return path;
    }
  }
  return null;
}

export function targetPlatform(os = process.platform, nodeArch = process.arch) {
  const arch = nodeArch === "x64" ? "amd64" : nodeArch;
  if (!(os === "darwin" || os === "linux" || os === "win32") ||
      !(arch === "arm64" || arch === "amd64")) {
    throw new Error(`no prebuilt binary for ${os}/${arch} — supported: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64, win32/arm64, win32/amd64`);
  }
  return { os, arch };
}

export function binaryInstallFilename(name, os = process.platform) {
  return os === "win32" ? `${name}.exe` : name;
}

function timeoutMs() {
  const raw = process.env.CAVE_SETUP_TIMEOUT ?? "300";
  const seconds = Number(raw);
  if (!Number.isInteger(seconds) || seconds <= 0) {
    throw new Error(`CAVE_SETUP_TIMEOUT must be a positive integer (got ${JSON.stringify(raw)})`);
  }
  return seconds * 1000;
}

async function asset(url, timeout) {
  let response;
  try {
    response = await fetch(url, { signal: AbortSignal.timeout(timeout) });
  } catch (error) {
    throw new Error(`binary download failed: ${error.message}`);
  }
  if (!response.ok) throw new Error(`binary download failed: HTTP ${response.status}`);
  return response;
}

function signedDigest(checksums, bundleRaw) {
  try {
    const bundle = JSON.parse(bundleRaw);
    if (bundle.mediaType !== "application/vnd.dev.sigstore.bundle.v0.3+json") return false;
    if (bundle.messageSignature?.messageDigest?.algorithm !== "SHA2_256") return false;
    const digest = createHash("sha256").update(checksums).digest();
    const bundled = Buffer.from(bundle.messageSignature.messageDigest.digest, "base64");
    if (digest.length !== bundled.length || !digest.equals(bundled)) return false;
    return verify(
      "sha256",
      Buffer.from(checksums),
      createPublicKey(BINARY_SIGNING_PUBKEY),
      Buffer.from(bundle.messageSignature.signature, "base64"),
    );
  } catch {
    return false;
  }
}

function expectedDigest(checksums, artifact) {
  for (const line of checksums.split("\n")) {
    if (!line) continue;
    const match = line.match(/^([a-f0-9]{64})  ([A-Za-z0-9._-]+)$/);
    if (!match) throw new Error("signed checksum manifest is malformed");
    if (match[2] === artifact) return match[1];
  }
  throw new Error(`signed checksum manifest does not contain ${artifact}`);
}

function cleanup(path) {
  try {
    unlinkSync(path);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
}

async function download(url, part, timeout) {
  const response = await asset(url, timeout);
  if (!response.body) throw new Error("binary download failed: response body missing");
  const hash = createHash("sha256");
  const file = await open(part, constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY, 0o600);
  const reader = response.body.getReader();
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      hash.update(value);
      await file.write(value);
    }
  } finally {
    reader.releaseLock();
    await file.close();
  }
  return hash.digest("hex");
}

// Windows refuses to replace a file another process holds open, and the
// binaries this installs (caveman-proxy, cavemem) are long-lived daemons — an
// update issued while one is running failed the rename outright and then the
// catch deleted the verified download, so the user was left with neither the
// new binary nor a retry (#657). Back off and try again; the daemon usually
// exits within a second or two of being asked to.
async function replaceWithRetry(part, target, attempts = 6) {
  for (let attempt = 0; ; attempt++) {
    try {
      renameSync(part, target);
      return;
    } catch (error) {
      const locked = error.code === "EPERM" || error.code === "EBUSY" ||
                     error.code === "EACCES" || error.code === "ETXTBSY";
      if (!locked || attempt >= attempts - 1) throw error;
      await new Promise((done) => setTimeout(done, 100 * 2 ** attempt));
    }
  }
}

export async function ensureBinary({ name, envVar }) {
  const explicit = process.env[envVar];
  if (explicit) {
    if (!executable(explicit)) throw new Error(`${envVar} does not point to an executable: ${explicit}`);
    return explicit;
  }
  const found = onPath(name);
  if (found) return found;
  const binDir = join(process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman"), "bin");
  const target = join(binDir, binaryInstallFilename(name));
  if (executable(target)) return target;

  const { os, arch } = targetPlatform();
  const artifact = `${name}_${os}_${arch}`;
  const base = (process.env.CAVE_BINARY_RELEASE_BASE ?? BINARY_RELEASE_BASE_DEFAULT).replace(/\/+$/, "");
  const release = `${base}/${BINARY_RELEASE}`;
  const timeout = timeoutMs();
  const [checksumsResponse, signatureResponse] = await Promise.all([
    asset(`${release}/checksums.txt`, timeout),
    asset(`${release}/checksums.txt.keysig`, timeout),
  ]);
  const [checksums, signature] = await Promise.all([checksumsResponse.text(), signatureResponse.text()]);
  if (!signedDigest(checksums, signature)) {
    throw new Error("signature check failed for checksums.txt — refusing to install");
  }
  const expected = expectedDigest(checksums, artifact);
  mkdirSync(binDir, { recursive: true });
  // Per-process part path, and NO pre-unlink. A fixed `${target}.part` plus a
  // cleanup() immediately before an O_CREAT|O_EXCL open made the exclusivity
  // guard unreachable: two concurrent installs (npx -y caveman-mcp is registered
  // per MCP session, so several agents can start one at once) raced — B unlinked
  // A's directory entry and created its own inode, A hashed ITS OWN bytes and
  // matched, and then the PATH-based chmod+rename published B's half-written file
  // as "checksum verified". Both the chmod and the rename now act on a name only
  // this process can own.
  //
  // ponytail: a SIGKILL mid-download now leaves one stray .part behind instead of
  // reusing the fixed name. Sweep binDir for stale .part files if that ever shows
  // up in the wild.
  const part = `${target}.${process.pid}.${randomBytes(6).toString("hex")}.part`;
  try {
    const actual = await download(`${release}/${artifact}`, part, timeout);
    if (actual !== expected) throw new Error(`signature check failed for ${artifact} — partial download deleted`);
    chmodSync(part, 0o755);
    await replaceWithRetry(part, target);
  } catch (error) {
    cleanup(part);
    throw error;
  }
  process.stderr.write(`${name}  ${os}/${arch}  checksum verified\n`);
  return target;
}
