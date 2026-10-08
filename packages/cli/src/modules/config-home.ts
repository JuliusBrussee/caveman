// One config home: ~/.caveman (CAVEMAN_HOME). The cloud and capability config
// used to live in ~/.caveman-cloud/config.json; the first read copies it to
// ~/.caveman/cloud.json and leaves the old file for older CLIs. Every reader
// and writer goes through cloudConfigPath().
import { randomBytes } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, linkSync, mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function cavemanHome(): string {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

export function legacyCloudDir(): string {
  return join(homedir(), ".caveman-cloud");
}

function legacyConfigPath(): string {
  return join(legacyCloudDir(), "config.json");
}

let migrated = "";

// The copy lands under a private name and is hard-linked into place, so a
// racing process sees either no file or the whole 0600 file; EEXIST means
// another process won. A copy that cannot be made leaves the old file in use
// and is retried on the next run.
export function cloudConfigPath(): string {
  const path = join(cavemanHome(), "cloud.json");
  if (migrated === path) return path;
  const legacy = legacyConfigPath();
  if (!existsSync(path) && existsSync(legacy)) {
    const tmp = `${path}.${process.pid}.${randomBytes(6).toString("hex")}.tmp`;
    try {
      mkdirSync(cavemanHome(), { recursive: true, mode: 0o700 });
      copyFileSync(legacy, tmp);
      chmodSync(tmp, 0o600);
      try {
        linkSync(tmp, path);
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      }
    } catch {
      return legacy;
    } finally {
      try { unlinkSync(tmp); } catch { /* never created */ }
    }
  }
  migrated = path;
  return path;
}

// While the old file exists, an older CLI may still run against it (a
// rollback). Keys it must keep respecting, the telemetry decision above all,
// are mirrored there. Best effort: the new file is the authority.
export function mirrorToLegacy(key: string, value: unknown): void {
  const legacy = legacyConfigPath();
  if (!existsSync(legacy) || cloudConfigPath() === legacy) return;
  try {
    const parsed = JSON.parse(readFileSync(legacy, "utf8")) as unknown;
    const out = parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
    out[key] = value;
    const tmp = `${legacy}.${process.pid}.tmp`;
    writeFileSync(tmp, JSON.stringify(out, null, 2), { mode: 0o600 });
    renameSync(tmp, legacy);
  } catch {
    /* an unreadable old file is the older CLI's problem, not this write's */
  }
}
