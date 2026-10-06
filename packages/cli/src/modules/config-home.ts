// One config home: ~/.caveman (CAVEMAN_HOME). The cloud and capability config
// used to live in ~/.caveman-cloud/config.json; the first read copies it to
// ~/.caveman/cloud.json and leaves the old file for older CLIs. Every reader
// and writer goes through cloudConfigPath().
import { chmodSync, constants, copyFileSync, existsSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function cavemanHome(): string {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

export function legacyCloudDir(): string {
  return join(homedir(), ".caveman-cloud");
}

let migrated = "";

export function cloudConfigPath(): string {
  const path = join(cavemanHome(), "cloud.json");
  if (migrated === path) return path;
  const legacy = join(legacyCloudDir(), "config.json");
  if (!existsSync(path) && existsSync(legacy)) {
    try {
      mkdirSync(cavemanHome(), { recursive: true, mode: 0o700 });
      copyFileSync(legacy, path, constants.COPYFILE_EXCL);
      chmodSync(path, 0o600);
    } catch {
      /* another process copied it first, or the home is read-only */
    }
  }
  migrated = path;
  return path;
}
