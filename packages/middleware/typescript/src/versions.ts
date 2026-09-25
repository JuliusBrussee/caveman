import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { MiddlewareRuntime } from '@caveman-ai/sdk/middleware';

// Resolve from this module, using the same ESM export conditions as its imports.
// Supported Node bundles retain native frameworks as external packages, including
// their package.json files. Missing metadata fails closed; never guess a version
// from our build-time dependencies or from an unrelated process.cwd() install.
const installed = new Map<string, string | null>();
export function installedFrameworkVersion(name: string, entry = name): string | null {
  const key = `${name}:${entry}`;
  if (installed.has(key)) return installed.get(key)!;
  let version: string | null = null;
  try {
    let directory = dirname(fileURLToPath(import.meta.resolve(entry)));
    for (let depth = 0; depth < 16; depth++) {
      try {
        const metadata = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
        if (metadata.name === name && typeof metadata.version === 'string') { version = metadata.version; break; }
      } catch { /* Entry points may be several directories below their package. */ }
      const parent = dirname(directory);
      if (parent === directory) break;
      directory = parent;
    }
  } catch { /* Missing or unreadable framework metadata cannot certify a version. */ }
  if (installed.size >= 32) installed.clear();
  installed.set(key, version);
  return version;
}

/** Stable releases only. Prereleases have not passed the adapter contract. */
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

export function supportsFramework(runtime: MiddlewareRuntime, name: string, low: string, high: string, entry = name): boolean {
  if (runtime.mode === 'off') return false;
  if (matchesFramework(name, low, high, entry)) return true;
  runtime.decline('unsupported_version');
  return false;
}
