import { lstatSync, readdirSync, readFileSync } from "node:fs";
import { dirname, posix, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const SHIPPABLE_FILES = Object.freeze([
  "LICENSE",
  "PRIVACY.md",
  "fonts/geist-mono.woff2",
  "fonts/geist-sans.woff2",
  "icons/icon16.png",
  "icons/icon32.png",
  "icons/icon48.png",
  "icons/icon128.png",
  "manifest.json",
  "popup.css",
  "popup.html",
  "popup.js",
  "src/background.js",
  "src/caveman.js",
  "src/directive.js",
  "src/indicator.css",
]);

function normalizedRelative(root, target) {
  return relative(root, target).split(sep).join("/");
}

function walk(root, directory = root) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const full = resolve(directory, entry.name);
    const stat = lstatSync(full);
    if (stat.isSymbolicLink()) throw new Error(`symlink refused: ${normalizedRelative(root, full)}`);
    if (stat.isDirectory()) files.push(...walk(root, full));
    else if (stat.isFile()) files.push(normalizedRelative(root, full));
    else throw new Error(`non-file refused: ${normalizedRelative(root, full)}`);
  }
  return files;
}

function localReference(base, value) {
  if (!value || value.startsWith("#") || /^(?:[a-z]+:|\/\/)/i.test(value)) return null;
  const path = posix.normalize(posix.join(posix.dirname(base), value.split(/[?#]/, 1)[0]));
  if (path === ".." || path.startsWith("../") || posix.isAbsolute(path)) {
    throw new Error(`reference escapes extension root: ${base} -> ${value}`);
  }
  return path;
}

function manifestReferences(manifest) {
  const refs = [];
  if (manifest.action?.default_popup) refs.push(manifest.action.default_popup);
  if (manifest.background?.service_worker) refs.push(manifest.background.service_worker);
  refs.push(...Object.values(manifest.icons ?? {}));
  refs.push(...Object.values(manifest.action?.default_icon ?? {}));
  for (const script of manifest.content_scripts ?? []) {
    refs.push(...(script.js ?? []), ...(script.css ?? []));
  }
  for (const resource of manifest.web_accessible_resources ?? []) refs.push(...(resource.resources ?? []));
  return refs;
}

function textReferences(path, text) {
  const refs = [];
  if (path.endsWith(".html")) {
    for (const match of text.matchAll(/\b(?:href|src)\s*=\s*["']([^"']+)["']/gi)) refs.push(match[1]);
  }
  if (path.endsWith(".css")) {
    for (const match of text.matchAll(/url\(\s*["']?([^"')]+)["']?\s*\)/gi)) refs.push(match[1]);
  }
  return refs;
}

export function verifyExtensionRoot(root, { exact = false } = {}) {
  const absoluteRoot = resolve(root);
  const allowed = new Set(SHIPPABLE_FILES);
  for (const file of SHIPPABLE_FILES) {
    const full = resolve(absoluteRoot, file);
    const rel = normalizedRelative(absoluteRoot, full);
    if (rel !== file || !lstatSync(full).isFile()) throw new Error(`missing shippable file: ${file}`);
  }
  if (exact) {
    const actual = walk(absoluteRoot).sort();
    const expected = [...SHIPPABLE_FILES].sort();
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      const unexpected = actual.filter((file) => !allowed.has(file));
      const missing = expected.filter((file) => !actual.includes(file));
      throw new Error(`staged allowlist mismatch; unexpected=${unexpected.join(",") || "none"}; missing=${missing.join(",") || "none"}`);
    }
  }

  const manifest = JSON.parse(readFileSync(resolve(absoluteRoot, "manifest.json"), "utf8"));
  if (manifest.manifest_version !== 3) throw new Error("manifest_version must be 3");
  const references = new Set(manifestReferences(manifest));
  for (const file of ["popup.html", "popup.css", "src/indicator.css"]) {
    const body = readFileSync(resolve(absoluteRoot, file), "utf8");
    for (const value of textReferences(file, body)) {
      const ref = localReference(file, value);
      if (ref) references.add(ref);
    }
  }
  for (const ref of references) {
    if (!allowed.has(ref)) throw new Error(`runtime reference not in package allowlist: ${ref}`);
    if (!lstatSync(resolve(absoluteRoot, ref)).isFile()) throw new Error(`runtime reference missing: ${ref}`);
  }
  return { manifest, files: [...SHIPPABLE_FILES] };
}

const invoked = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invoked) {
  const root = resolve(process.argv[2] ?? new URL("..", import.meta.url).pathname);
  const result = verifyExtensionRoot(root, { exact: true });
  process.stdout.write(`verified ${result.files.length} extension files for ${result.manifest.version}\n`);
}
