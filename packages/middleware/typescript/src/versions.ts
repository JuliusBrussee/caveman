import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import type { MiddlewareRuntime } from '@caveman-ai/sdk/middleware';

// Framework peers cannot be declared globally: independently selectable native
// adapters can require mutually incompatible optional provider SDK versions.
// Consumers install their selected native framework; exact support is checked
// at its entry point before any history, tool, or transport is changed.
const require = createRequire(import.meta.url);
const installed = new Map<string, string | null>();
export function installedFrameworkVersion(name: string, entry = name): string | null {
  const key = `${name}:${entry}`;
  if (installed.has(key)) return installed.get(key)!;
  let version: string | null = null;
  try {
    let directory = dirname(require.resolve(entry));
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

/** Numeric release segments, prerelease and build metadata dropped. */
function release(value: string): number[] {
  const parts: number[] = [];
  for (const chunk of (value.split('+')[0] ?? '').split('-')[0]!.split('.')) {
    const number = Number.parseInt(chunk, 10);
    if (!Number.isInteger(number) || number < 0) break;
    parts.push(number);
  }
  return parts;
}

function compare(a: number[], b: number[]): number {
  for (let index = 0; index < Math.max(a.length, b.length); index++) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0);
    if (difference !== 0) return difference < 0 ? -1 : 1;
  }
  return 0;
}

/** `low <= version < high`, comparing release segments only. A framework that
 * ships a breaking change inside the range is caught by the adapter's own
 * serialization revision, which is part of the runtime's scope identity; this
 * gate only keeps a wildly different major from reaching the wire format. */
export function inRange(version: string | null, low: string, high: string): boolean {
  if (!version) return false;
  const found = release(version);
  return found.length > 0 && compare(found, release(low)) >= 0 && compare(found, release(high)) < 0;
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
