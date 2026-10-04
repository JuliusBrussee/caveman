import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

function packageVersion(directory: string, name: string): string | null {
  try {
    const metadata = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
    return metadata.name === name && typeof metadata.version === 'string' ? metadata.version : null;
  } catch { return null; }
}

function nearest(directory: string, name: string, levels: number, nested: boolean): string | null {
  for (let depth = 0; depth < levels; depth++) {
    const version = packageVersion(nested ? join(directory, 'node_modules', name) : directory, name);
    if (version !== null || dirname(directory) === directory) return version;
    directory = dirname(directory);
  }
  return null;
}

// TS-2: the copy this module imports is the copy the adapter runs, so the gate reads that one, resolved from here and
// never from the working directory: a stray hoisted copy in a workspace can neither certify nor block the app.
// Bundled deploys (CJS, esbuild) cannot resolve it: the result is null and the adapter feature-detects (Decision 3).
const installed = new Map<string, string | null>();
export function installedFrameworkVersion(name: string, entry = name): string | null {
  const key = `${name}:${entry}`;
  if (installed.has(key)) return installed.get(key)!;
  let version: string | null = null;
  // import.meta.resolve is absent in CJS bundles; the throw leaves version null.
  try { version = nearest(dirname(fileURLToPath(import.meta.resolve(entry))), name, 16, false); } catch { /* unresolvable from this module */ }
  if (installed.size >= 32) installed.clear();
  installed.set(key, version);
  return version;
}

/** Once per package: warn when the application's own copy (found from the working directory) differs from the copy
 * this package imports. Only a warning; the gate never reads the application's copy. */
const compared = new Set<string>();
export function warnFrameworkMismatch(name: string, imported: string | null): void {
  if (imported === null || compared.has(name)) return;
  compared.add(name);
  let application: string | null = null;
  try { application = nearest(process.cwd(), name, 32, true); } catch { /* no working directory */ }
  if (application !== null && application !== imported) console.warn(`Caveman middleware: the application resolves ${name} ${application}, but this package imports ${name} ${imported} and is gated on it; dedupe or link ${name} so both resolve one copy.`);
}

/** openai and anthropic: the version of the SDK copy that built this client, from its own User-Agent. */
export function clientVersion(client: unknown): string | null {
  try { return /\/JS (\S+)$/.exec((client as { getUserAgent(): string }).getUserAgent())?.[1] ?? null; } catch { return null; }
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
