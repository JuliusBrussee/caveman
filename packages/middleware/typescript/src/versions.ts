import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

function packageVersion(directory: string, name: string): string | null {
  try {
    const metadata = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
    return metadata.name === name && typeof metadata.version === 'string' ? metadata.version : null;
  } catch { return null; }
}

// Decision 2: read the application's installed copy (node_modules above the working directory) before the copy next
// to this module, so a stray hoisted copy cannot certify the app. Bundled deploys (CJS, esbuild, Next server bundles)
// can have neither: the result is null and the adapter feature-detects instead (Decision 3).
const installed = new Map<string, string | null>();
export function installedFrameworkVersion(name: string, entry = name): string | null {
  const key = `${name}:${entry}`;
  if (installed.has(key)) return installed.get(key)!;
  let version: string | null = null;
  try {
    for (let directory = process.cwd(), depth = 0; version === null && depth < 32; depth++) {
      version = packageVersion(join(directory, 'node_modules', name), name);
      if (dirname(directory) === directory) break;
      directory = dirname(directory);
    }
  } catch { /* no working directory */ }
  try {
    // import.meta.resolve is absent in CJS bundles; the throw leaves version null.
    for (let directory = dirname(fileURLToPath(import.meta.resolve(entry))), depth = 0; version === null && depth < 16; depth++) {
      version = packageVersion(directory, name);
      if (dirname(directory) === directory) break;
      directory = dirname(directory);
    }
  } catch { /* unresolvable from this module */ }
  if (installed.size >= 32) installed.clear();
  installed.set(key, version);
  return version;
}

/** Stable releases only, deliberately: a prerelease (`7.1.0-canary.3`) is outside every range, reports
 * `unsupported_version`, and runs only with `acceptFrameworkVersion`. Build metadata is ignored. */
function release(value: string): number[] {
  if (!/^(0|[1-9]\d*)(\.(0|[1-9]\d*)){0,2}(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(value)) return [];
  const parts = value.split('+')[0]!.split('.').map(Number);
  return parts.every(Number.isSafeInteger) ? parts : [];
}

function compare(a: number[], b: number[]): number {
  for (let index = 0; index < Math.max(a.length, b.length); index++) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0);
    if (difference !== 0) return difference < 0 ? -1 : 1;
  }
  return 0;
}

/** `low <= version < high`. A compatible range is not a claim that each release
 * was tested. Serialization revisions identify our format, not upstream changes. */
export function inRange(version: string | null, low: string, high: string): boolean {
  if (!version) return false;
  const found = release(version);
  const minimum = release(low), maximum = release(high);
  return found.length === 3 && minimum.length > 0 && maximum.length > 0 && compare(found, minimum) >= 0 && compare(found, maximum) < 0;
}

/** Pure version check for adapters retaining a passive per-call delegate. */
export function matchesFramework(name: string, low: string, high: string, entry = name): boolean {
  return inRange(installedFrameworkVersion(name, entry), low, high);
}
