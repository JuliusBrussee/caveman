// Scheduled drift check: exits 1 when the latest npm or PyPI release of an adapted framework falls outside the range
// the adapters run on, so a new release is noticed the day it ships rather than when an upgrade silently passes
// through with `unsupported_version`. Reads the TypeScript ranges from package.json and the Python ones from
// ../python/caveman_middleware/_versions.py. Run after `npm run build`.
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { inRange } from '../dist/versions.js';

/** `[registry, package, low, exclusiveHigh]` for every TypeScript and Python gate range. */
export async function ranges() {
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  const npm = Object.entries(pkg.supportedFrameworkVersions).map(([name, range]) => ['npm', name, ...range.match(/^>=(\S+) <(\S+)$/).slice(1)]);
  const source = await readFile(new URL('../../python/caveman_middleware/_versions.py', import.meta.url), 'utf8');
  const pins = [...source.slice(source.indexOf('COMPATIBILITY = ')).matchAll(/\("([\w.-]+)", "([\d.]+)", "([\d.]+)"\)/g)];
  return [...npm, ...new Map(pins.map(([, name, low, high]) => [name, ['pypi', name, low, high]])).values()];
}

// PEP 440 stable release/post/local forms, as `_STABLE_VERSION` in _versions.py: any release length, a nonzero epoch,
// prerelease or dev release is null (outside every range), a local label is ignored.
const PEP440 = /^(?:(\d+)!)?(\d+(?:\.\d+)*)(?:[-_.]?(?:post|rev|r)[-_.]?(\d*)|-(\d+))?(?:\+[a-z0-9]+(?:[-_.][a-z0-9]+)*)?$/i;
function stable(value) {
  const m = typeof value === 'string' ? PEP440.exec(value) : null;
  if (!m || Number(m[1] ?? 0) !== 0) return null;
  const post = m[3] ?? m[4];
  return [m[2].split('.').map(Number), post === undefined ? -1 : Number(post || 0)];
}

/** `low <= version < high` exactly as the Python gate's `in_range` decides it, so the PyPI canary agrees with it. */
export function pypiInRange(version, low, high) {
  const versions = [version, low, high].map(stable);
  if (versions.includes(null)) return false;
  const width = Math.max(...versions.map(([release]) => release.length));
  const [found, lower, upper] = versions.map(([release, post]) => [...release, ...Array(width - release.length).fill(0), post]);
  const compare = (a, b) => { const i = a.findIndex((part, j) => part !== b[j]); return i < 0 ? 0 : a[i] - b[i]; };
  return compare(found, lower) >= 0 && compare(found, upper) < 0;
}

/** One line per range whose latest release (`latest['npm:openai']`) is outside it. */
export function drift(entries, latest) {
  return entries.filter(([registry, name, low, high]) => !(registry === 'pypi' ? pypiInRange : inRange)(latest[`${registry}:${name}`], low, high))
    .map(([registry, name, low, high]) => `${registry} ${name} ${latest[`${registry}:${name}`]} is outside >=${low} <${high}`);
}

async function latestRelease(registry, name) {
  const url = registry === 'npm' ? `https://registry.npmjs.org/${name}/latest` : `https://pypi.org/pypi/${name}/json`;
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${url}: HTTP ${response.status}`);
  const body = await response.json();
  return registry === 'npm' ? body.version : body.info.version;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const entries = await ranges();
  const latest = Object.fromEntries(await Promise.all(entries.map(async ([registry, name]) => [`${registry}:${name}`, await latestRelease(registry, name)])));
  const problems = drift(entries, latest);
  for (const line of problems) console.error(line);
  console.log(`${entries.length - problems.length}/${entries.length} latest releases inside their supported range`);
  process.exitCode = problems.length ? 1 : 0;
}
