import { opendir, readFile, realpath } from "node:fs/promises";
import { builtinModules, createRequire, findPackageJSON } from "node:module";
import { basename, dirname, extname, isAbsolute, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { init, parse } from "es-module-lexer";
import { sha256, stableStringify } from "./context-ir.js";

export type SourceManifestEntry = { name: string; sha256: string };

const SOURCE_EXTENSIONS = [".ts", ".tsx", ".mts", ".cts", ".js", ".mjs", ".cjs"];
const RESOLUTION_EXTENSIONS = [...SOURCE_EXTENSIONS, ".json"];
const TYPESCRIPT_SOURCE_EXTENSIONS = new Set([".ts", ".tsx", ".mts", ".cts"]);
const LEGACY_LOAD_START_PATTERN = /\bFunction\b|\bReflect\b|\bcreateRequire\b|\beval\b|\bglobalThis\b|\bmodule\b|\brequire\b|\bnew\b/g;
const TYPESCRIPT_IMPORT_EXPORT_PATTERN = /\bimport\b|\bexport\b/g;
const BUILTINS = new Set([...builtinModules, ...builtinModules.map((name) => `node:${name}`)]);

export async function expandSourceGraph(
  root: string,
  initialFiles: Iterable<string>,
  includeBareDependencies = true,
  traversalStopRoots: readonly string[] = [],
): Promise<Set<string>> {
  await init;
  const canonicalRoot = await realpath(root);
  const files = new Set<string>();
  for (const path of initialFiles) files.add(await realpath(path));
  const queue = [...files];
  const visited = new Set<string>();
  const external = new Set<string>();
  const packageRoots = new Set<string>();
  while (queue.length > 0) {
    const path = queue.shift()!;
    if (visited.has(path) || !SOURCE_EXTENSIONS.includes(extname(path))) continue;
    visited.add(path);
    const source = await readFile(path, "utf8");
    const code = executableCodeMask(source);
    const typescript = typescriptSourceSyntax(source, path, code);
    const specifiers = new Set([
      ...esmSourceSpecifiers(typescript.lexableSource, path),
      ...typescript.specifiers,
      ...legacySourceSpecifiers(source, path, code),
    ]);
    for (const specifier of specifiers) {
      if (BUILTINS.has(specifier)) continue;
      const bare = !specifier.startsWith(".");
      if (bare && !includeBareDependencies) continue;
      let base: string;
      let packageRoot: string | undefined;
      if (bare) {
        packageRoot = dependencyPackageRoot(specifier, path);
        try {
          base = createRequire(path).resolve(specifier);
        } catch (requireError) {
          try {
            base = await resolveImportOnlyDependency(specifier, path);
          } catch {
            throw new Error(
              `caveman build: unresolved source dependency ${JSON.stringify(specifier)}`,
              { cause: requireError },
            );
          }
        }
      } else {
        base = resolve(dirname(path), specifier);
      }
      const candidates = extname(base)
        ? explicitImportCandidates(base, path)
        : [
          base,
          ...RESOLUTION_EXTENSIONS.map((extension) => base + extension),
          ...SOURCE_EXTENSIONS.map((extension) => resolve(base, `index${extension}`)),
        ];
      let found: string | undefined;
      for (const candidate of candidates) {
        try {
          await readFile(candidate);
          found = candidate;
          break;
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
        }
      }
      if (!found) {
        throw new Error(`caveman build: unresolved relative source import ${JSON.stringify(specifier)}`);
      }
      if (bare) {
        await collectPackageClosure(packageRoot!, files, packageRoots);
        continue;
      }
      const lexicalFound = found;
      found = await realpath(found);
      const insideRoot = isPathWithin(canonicalRoot, found);
      if (!insideRoot && !bare && !external.has(path)) {
        const lexicalInsideRoot = isPathWithin(canonicalRoot, lexicalFound);
        if (lexicalInsideRoot) {
          throw new Error("caveman build: source graph symlink escapes project root");
        }
        throw new Error("caveman build: imported source escapes project root");
      }
      if (!insideRoot) external.add(found);
      if (!files.has(found)) {
        files.add(found);
        if (!traversalStopRoots.some((stop) => isPathWithin(stop, found))) {
          queue.push(found);
        }
      }
    }
  }
  return files;
}

function explicitImportCandidates(base: string, importer: string): string[] {
  const importerExtension = extname(importer);
  if (![".ts", ".tsx", ".mts", ".cts"].includes(importerExtension)) return [base];
  const extension = extname(base);
  const stem = base.slice(0, -extension.length);
  // Match TypeScript NodeNext extension substitution. Authored TypeScript uses
  // runtime-valid .js/.mjs/.cjs specifiers while compiler and loader resolve
  // their .ts/.mts/.cts sources.
  if (extension === ".js") return [`${stem}.ts`, `${stem}.tsx`, base];
  if (extension === ".mjs") return [`${stem}.mts`, base];
  if (extension === ".cjs") return [`${stem}.cts`, base];
  return [base];
}

async function resolveImportOnlyDependency(specifier: string, importer: string): Promise<string> {
  const packageName = barePackageName(specifier);
  const packageJSONPath = resolvePackageJSON(packageName, importer);
  const packageJSON = JSON.parse(await readFile(packageJSONPath, "utf8")) as {
    exports?: unknown;
  };
  const subpath = specifier === packageName ? "." : `.${specifier.slice(packageName.length)}`;
  const target = resolvePackageExport(packageJSON.exports, subpath);
  if (target === undefined || !target.startsWith("./")) {
    throw new Error("import export not found");
  }
  const packageRoot = dirname(packageJSONPath);
  const absolute = resolve(packageRoot, target);
  const relativeTarget = relative(packageRoot, absolute);
  if (relativeTarget === ".." || relativeTarget.startsWith("../") || relativeTarget.startsWith("..\\")) {
    throw new Error("package export escapes package root");
  }
  return absolute;
}

function dependencyPackageRoot(specifier: string, importer: string): string {
  return dirname(resolvePackageJSON(barePackageName(specifier), importer));
}

function resolvePackageJSON(packageName: string, importer: string): string {
  const packageJSONPath = findPackageJSON(packageName, pathToFileURL(importer));
  if (packageJSONPath === undefined) throw new Error("package not found");
  return packageJSONPath;
}

async function collectPackageClosure(
  packageRoot: string,
  files: Set<string>,
  visitedRoots: Set<string>,
): Promise<void> {
  const canonicalRoot = await realpath(packageRoot);
  if (visitedRoots.has(canonicalRoot)) return;
  visitedRoots.add(canonicalRoot);
  // Resolve dependency edges from the physical package location. pnpm exposes
  // the project dependency as a symlink, while its private dependency links
  // live beside the physical package under node_modules/.pnpm.
  const packageJSONPath = resolve(canonicalRoot, "package.json");
  const packageJSON = JSON.parse(await readFile(packageJSONPath, "utf8")) as {
    dependencies?: Record<string, unknown>;
    optionalDependencies?: Record<string, unknown>;
    peerDependencies?: Record<string, unknown>;
  };
  await collectPackageFiles(canonicalRoot, files);
  const required = new Set(Object.keys(packageJSON.dependencies ?? {}));
  const optional = new Set([
    ...Object.keys(packageJSON.optionalDependencies ?? {}),
    ...Object.keys(packageJSON.peerDependencies ?? {}),
  ]);
  for (const dependency of [...new Set([...required, ...optional])].sort()) {
    try {
      const childPackageJSON = findPackageJSON(dependency, pathToFileURL(packageJSONPath));
      if (childPackageJSON === undefined) throw new Error("package not found");
      await collectPackageClosure(dirname(childPackageJSON), files, visitedRoots);
    } catch (error) {
      if (!required.has(dependency)) continue;
      throw new Error(`caveman build: unresolved package dependency ${JSON.stringify(dependency)}`, {
        cause: error,
      });
    }
  }
}

async function collectPackageFiles(directory: string, files: Set<string>): Promise<void> {
  const entries = await opendir(directory);
  for await (const entry of entries) {
    if (entry.name === "node_modules" || entry.name === ".git") continue;
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      await collectPackageFiles(path, files);
    } else if (entry.isFile()) {
      files.add(path);
    } else if (entry.isSymbolicLink()) {
      throw new Error(`caveman build: package artifact symlink is not lockable: ${JSON.stringify(path)}`);
    }
  }
}

function barePackageName(specifier: string): string {
  const parts = specifier.split("/");
  if (specifier.startsWith("@")) {
    if (parts.length < 2 || parts[1] === "") throw new Error("invalid scoped package");
    return `${parts[0]}/${parts[1]}`;
  }
  if (parts[0] === "") throw new Error("invalid package");
  return parts[0]!;
}

function resolvePackageExport(exportsValue: unknown, subpath: string): string | undefined {
  if (typeof exportsValue === "string" || Array.isArray(exportsValue)) {
    return subpath === "." ? resolveConditionalExport(exportsValue) : undefined;
  }
  if (!isRecord(exportsValue)) return undefined;
  const keys = Object.keys(exportsValue);
  if (!keys.some((key) => key.startsWith("."))) {
    return subpath === "." ? resolveConditionalExport(exportsValue) : undefined;
  }
  if (Object.hasOwn(exportsValue, subpath)) {
    return resolveConditionalExport(exportsValue[subpath]);
  }
  const patterns = keys
    .filter((key) => key.startsWith("./") && key.includes("*"))
    .sort((left, right) => right.length - left.length);
  for (const pattern of patterns) {
    const [prefix, suffix = ""] = pattern.split("*", 2);
    if (!subpath.startsWith(prefix!) || !subpath.endsWith(suffix)) continue;
    const matched = subpath.slice(prefix!.length, subpath.length - suffix.length);
    const target = resolveConditionalExport(exportsValue[pattern]);
    if (target !== undefined) return target.replaceAll("*", matched);
  }
  return undefined;
}

function resolveConditionalExport(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) {
    for (const candidate of value) {
      const resolved = resolveConditionalExport(candidate);
      if (resolved !== undefined) return resolved;
    }
    return undefined;
  }
  if (!isRecord(value)) return undefined;
  for (const [condition, candidate] of Object.entries(value)) {
    if (condition !== "import" && condition !== "node" && condition !== "default") continue;
    const resolved = resolveConditionalExport(candidate);
    if (resolved !== undefined) return resolved;
  }
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isPathWithin(root: string, candidate: string): boolean {
  const child = relative(root, candidate);
  return child === "" || (!isAbsolute(child) && child !== ".." &&
    !child.startsWith("../") && !child.startsWith("..\\"));
}

function esmSourceSpecifiers(source: string, path: string): string[] {
  let imports: ReturnType<typeof parse>[0];
  try {
    [imports] = parse(source, path);
  } catch (error) {
    throw new Error(
      `caveman build: source module syntax is not lexable in ${JSON.stringify(path)}`,
      { cause: error },
    );
  }
  const specifiers: string[] = [];
  for (const item of imports) {
    if (item.d === -2) continue; // import.meta is metadata, not a dependency.
    if (item.n === undefined) {
      throw new Error(`caveman build: computed source dependency is not lockable in ${JSON.stringify(path)}`);
    }
    specifiers.push(item.n);
  }
  return specifiers;
}

function typescriptSourceSyntax(
  source: string,
  path: string,
  code: Uint8Array,
): { lexableSource: string; specifiers: string[] } {
  if (!TYPESCRIPT_SOURCE_EXTENSIONS.has(extname(path))) {
    return { lexableSource: source, specifiers: [] };
  }
  const masks: Array<{ start: number; end: number }> = [];
  const specifiers: string[] = [];
  let precedingExportEnd: number | undefined;
  for (const match of source.matchAll(TYPESCRIPT_IMPORT_EXPORT_PATTERN)) {
    const start = match.index;
    const keyword = match[0]!;
    if (!code[start] || !isIdentifierTokenAt(source, start, keyword)) continue;
    if (keyword === "export") {
      precedingExportEnd = start + keyword.length;
      const specifier = typescriptTypeExportSpecifier(source, start, path, code);
      if (specifier !== undefined) specifiers.push(specifier);
      continue;
    }
    const exported = precedingExportEnd !== undefined &&
      skipSourceTrivia(source, precedingExportEnd) === start;
    precedingExportEnd = undefined;
    if (exported) continue; // es-module-lexer accepts `export import x = ...`.
    const end = typescriptImportEqualsPrefixEnd(source, start, path);
    if (end !== undefined) masks.push({ start, end });
  }
  if (masks.length === 0) return { lexableSource: source, specifiers };
  const chars = source.split("");
  for (const mask of masks) {
    for (let index = mask.start; index < mask.end; index += 1) {
      if (chars[index] !== "\n" && chars[index] !== "\r") chars[index] = " ";
    }
  }
  return { lexableSource: chars.join(""), specifiers };
}

function typescriptImportEqualsPrefixEnd(
  source: string,
  start: number,
  path: string,
): number | undefined {
  let cursor = skipSourceTrivia(source, start + "import".length);
  let binding = readIdentifierToken(source, cursor);
  if (binding === undefined) return undefined;
  cursor = skipSourceTrivia(source, binding.end);
  if (source.slice(binding.start, binding.end) === "type" && source[cursor] !== "=") {
    binding = readIdentifierToken(source, cursor);
    if (binding === undefined) return undefined;
    cursor = skipSourceTrivia(source, binding.end);
  }
  if (source[cursor] !== "=") return undefined;
  const prefixEnd = skipSourceTrivia(source, cursor + 1);
  let referenceEnd: number;
  if (isIdentifierTokenAt(source, prefixEnd, "require")) {
    const required = parseRequireCall(source, prefixEnd, path);
    if (required === undefined) throw sourceSyntaxError(path);
    referenceEnd = required.end;
  } else {
    const qualified = readQualifiedIdentifier(source, prefixEnd);
    if (qualified === undefined) throw sourceSyntaxError(path);
    referenceEnd = qualified;
  }
  if (!sourceStatementEndsAt(source, referenceEnd)) throw sourceSyntaxError(path);
  return prefixEnd;
}

function typescriptTypeExportSpecifier(
  source: string,
  start: number,
  path: string,
  code: Uint8Array,
): string | undefined {
  let cursor = skipSourceTrivia(source, start + "export".length);
  const typeEnd = consumeIdentifierToken(source, cursor, "type");
  if (typeEnd === undefined) return undefined;
  cursor = skipSourceTrivia(source, typeEnd);
  if (source[cursor] === "{") {
    let depth = 1;
    cursor += 1;
    while (cursor < source.length && depth > 0) {
      if (code[cursor]) {
        if (source[cursor] === "{") depth += 1;
        else if (source[cursor] === "}") depth -= 1;
      }
      cursor += 1;
    }
    if (depth !== 0) throw sourceSyntaxError(path);
  } else if (source[cursor] === "*") {
    cursor = skipSourceTrivia(source, cursor + 1);
    const asEnd = consumeIdentifierToken(source, cursor, "as");
    if (asEnd !== undefined) {
      cursor = skipSourceTrivia(source, asEnd);
      const alias = readIdentifierToken(source, cursor) ?? readQuotedSpecifier(source, cursor, path);
      if (alias === undefined) throw sourceSyntaxError(path);
      cursor = alias.end;
    }
  } else {
    return undefined; // A local `export type Name = ...` declaration.
  }
  cursor = skipSourceTrivia(source, cursor);
  const fromEnd = consumeIdentifierToken(source, cursor, "from");
  if (fromEnd === undefined) return undefined; // Local named type export.
  cursor = skipSourceTrivia(source, fromEnd);
  const literal = readQuotedSpecifier(source, cursor, path);
  if (literal === undefined) throw sourceSyntaxError(path);
  return literal.specifier;
}

function legacySourceSpecifiers(source: string, path: string, code: Uint8Array): string[] {
  const trivia = String.raw`(?:\s|\/\*[\s\S]*?\*\/|\/\/[^\r\n]*(?:\r?\n|$))*`;
  const loaderAliases = [
    new RegExp(String.raw`\bcreateRequire${trivia}\(`),
    new RegExp(String.raw`\b(?:const|let|var)${trivia}[A-Za-z_$][\w$]*${trivia}=${trivia}require\b`),
    new RegExp(String.raw`\b(?:const|let|var)${trivia}[A-Za-z_$][\w$]*${trivia}=${trivia}URL\b`),
  ];
  if (loaderAliases.some((pattern) => hasCodeMatch(source, pattern, code))) {
    throw new Error(`caveman build: aliased source loader is not lockable in ${JSON.stringify(path)}`);
  }
  const specifiers: string[] = [];
  for (const match of source.matchAll(LEGACY_LOAD_START_PATTERN)) {
    const start = match.index;
    const keyword = match[0]!;
    if (!code[start] || !isIdentifierTokenAt(source, start, keyword)) continue;
    if (keyword === "module") {
      assertNoModuleRequireAlias(source, start, path);
      continue;
    }
    if (keyword === "Reflect") {
      assertNoReflectRequireAlias(source, start, path, code);
      continue;
    }
    if (keyword === "globalThis") {
      assertNoGlobalLoaderAlias(source, start, path);
      continue;
    }
    if (keyword === "createRequire" || keyword === "eval" || keyword === "Function") {
      assertNoDirectLoaderFactoryCall(source, start, keyword, path);
      continue;
    }
    if (keyword === "require") {
      const required = parseRequireCall(source, start, path);
      if (required !== undefined) specifiers.push(required.specifier);
      continue;
    }
    const asset = parseImportMetaURL(source, start, path);
    if (asset !== undefined) specifiers.push(asset.specifier);
  }
  return specifiers;
}

function parseRequireCall(
  source: string,
  start: number,
  path: string,
): { specifier: string; end: number } | undefined {
  let cursor = skipSourceTrivia(source, start + "require".length);
  const optionalMemberEnd = consumeOptionalChainToken(source, cursor);
  if (optionalMemberEnd !== undefined) {
    cursor = skipSourceTrivia(source, optionalMemberEnd);
    if (source[cursor] !== "(") {
      const identifier = readIdentifierToken(source, cursor);
      const member = source[cursor] === "["
        ? readSourceMember(source, cursor, path)
        : identifier === undefined
          ? undefined
          : { name: source.slice(identifier.start, identifier.end), end: identifier.end };
      if (member === undefined || member.name !== "resolve") throw computedSourceError(path);
      cursor = skipSourceTrivia(source, member.end);
    }
  } else if (source[cursor] === "." || source[cursor] === "[") {
    const member = readSourceMember(source, cursor, path);
    if (member === undefined) return undefined;
    if (member.name !== "resolve") {
      if (LEGACY_REQUIRE_ALIAS_MEMBERS.has(member.name)) throw aliasedSourceError(path);
      return undefined;
    }
    cursor = skipSourceTrivia(source, member.end);
  }
  const optionalCallEnd = consumeOptionalChainToken(source, cursor);
  if (optionalCallEnd !== undefined) cursor = skipSourceTrivia(source, optionalCallEnd);
  if (source[cursor] !== "(") {
    if (parenthesizedLoaderContinuation(source, cursor)) throw computedSourceError(path);
    return undefined;
  }
  cursor = skipSourceTrivia(source, cursor + 1);
  const literal = readQuotedSpecifier(source, cursor, path);
  if (literal === undefined) throw computedSourceError(path);
  cursor = skipSourceTrivia(source, literal.end);
  if (source[cursor] !== ")") throw computedSourceError(path);
  return { specifier: literal.specifier, end: cursor + 1 };
}

function assertNoModuleRequireAlias(source: string, start: number, path: string): void {
  let cursor = skipSourceTrivia(source, start + "module".length);
  const optionalMemberEnd = consumeOptionalChainToken(source, cursor);
  let member: { name: string; end: number } | undefined;
  if (optionalMemberEnd !== undefined) {
    cursor = skipSourceTrivia(source, optionalMemberEnd);
    const identifier = readIdentifierToken(source, cursor);
    member = source[cursor] === "["
      ? readSourceMember(source, cursor, path)
      : identifier === undefined
        ? undefined
        : { name: source.slice(identifier.start, identifier.end), end: identifier.end };
  } else {
    if (source[cursor] !== "." && source[cursor] !== "[") return;
    member = readSourceMember(source, cursor, path);
  }
  if (member?.name === "require") throw aliasedSourceError(path);
}

function assertNoReflectRequireAlias(
  source: string,
  start: number,
  path: string,
  code: Uint8Array,
): void {
  let cursor = skipSourceTrivia(source, start + "Reflect".length);
  const optionalMemberEnd = consumeOptionalChainToken(source, cursor);
  if (optionalMemberEnd !== undefined) cursor = skipSourceTrivia(source, optionalMemberEnd);
  const member = optionalMemberEnd === undefined
    ? readSourceMember(source, cursor, path)
    : source[cursor] === "["
      ? readSourceMember(source, cursor, path)
      : (() => {
          const identifier = readIdentifierToken(source, cursor);
          return identifier === undefined
            ? undefined
            : { name: source.slice(identifier.start, identifier.end), end: identifier.end };
        })();
  if (member?.name !== "apply") return;
  cursor = skipSourceTrivia(source, member.end);
  const optionalCallEnd = consumeOptionalChainToken(source, cursor);
  if (optionalCallEnd !== undefined) cursor = skipSourceTrivia(source, optionalCallEnd);
  if (source[cursor] !== "(") return;
  cursor += 1;
  let depth = 0;
  while (cursor < source.length) {
    if (!code[cursor]) {
      cursor += 1;
      continue;
    }
    const char = source[cursor]!;
    if (char === "(") depth += 1;
    else if (char === ")") {
      if (depth === 0) return;
      depth -= 1;
    } else if (char === "," && depth === 0) {
      return;
    } else if (isIdentifierTokenAt(source, cursor, "require")) {
      throw aliasedSourceError(path);
    }
    cursor += 1;
  }
}

function assertNoGlobalLoaderAlias(source: string, start: number, path: string): void {
  let cursor = skipSourceTrivia(source, start + "globalThis".length);
  const optionalMemberEnd = consumeOptionalChainToken(source, cursor);
  if (optionalMemberEnd !== undefined) cursor = skipSourceTrivia(source, optionalMemberEnd);
  const member = optionalMemberEnd === undefined
    ? readSourceMember(source, cursor, path)
    : source[cursor] === "["
      ? readSourceMember(source, cursor, path)
      : (() => {
          const identifier = readIdentifierToken(source, cursor);
          return identifier === undefined
            ? undefined
            : { name: source.slice(identifier.start, identifier.end), end: identifier.end };
        })();
  if (member !== undefined && GLOBAL_LOADER_MEMBERS.has(member.name)) throw aliasedSourceError(path);
}

function assertNoDirectLoaderFactoryCall(
  source: string,
  start: number,
  keyword: string,
  path: string,
): void {
  let cursor = skipSourceTrivia(source, start + keyword.length);
  const optionalCallEnd = consumeOptionalChainToken(source, cursor);
  if (optionalCallEnd !== undefined) cursor = skipSourceTrivia(source, optionalCallEnd);
  if (source[cursor] === "(") throw aliasedSourceError(path);
}

function readSourceMember(
  source: string,
  start: number,
  path: string,
): { name: string; end: number } | undefined {
  if (source[start] === ".") {
    const memberStart = skipSourceTrivia(source, start + 1);
    const member = readIdentifierToken(source, memberStart);
    if (member === undefined) return undefined;
    return { name: source.slice(member.start, member.end), end: member.end };
  }
  if (source[start] !== "[") return undefined;
  const literalStart = skipSourceTrivia(source, start + 1);
  const literal = readQuotedSpecifier(source, literalStart, path);
  if (literal === undefined) return undefined;
  const end = skipSourceTrivia(source, literal.end);
  if (source[end] !== "]") throw sourceSyntaxError(path);
  return { name: literal.specifier, end: end + 1 };
}

function parenthesizedLoaderContinuation(source: string, start: number): boolean {
  let cursor = start;
  let closed = false;
  while (source[cursor] === ")") {
    closed = true;
    cursor = skipSourceTrivia(source, cursor + 1);
  }
  if (!closed) return false;
  return source[cursor] === "(" || source[cursor] === "." || source[cursor] === "[" ||
    consumeOptionalChainToken(source, cursor) !== undefined;
}

function parseImportMetaURL(
  source: string,
  start: number,
  path: string,
): { specifier: string; end: number } | undefined {
  let cursor = skipSourceTrivia(source, start + "new".length);
  const urlEnd = consumeIdentifierToken(source, cursor, "URL");
  if (urlEnd === undefined) return undefined;
  cursor = skipSourceTrivia(source, urlEnd);
  if (source[cursor] !== "(") return undefined;
  cursor = skipSourceTrivia(source, cursor + 1);
  const literal = readQuotedSpecifier(source, cursor, path);
  if (literal === undefined) throw computedSourceError(path);
  cursor = skipSourceTrivia(source, literal.end);
  if (source[cursor] !== ",") return undefined;
  cursor = skipSourceTrivia(source, cursor + 1);
  const importEnd = consumeIdentifierToken(source, cursor, "import");
  if (importEnd === undefined) return undefined;
  cursor = skipSourceTrivia(source, importEnd);
  if (source[cursor] !== ".") return undefined;
  cursor = skipSourceTrivia(source, cursor + 1);
  const metaEnd = consumeIdentifierToken(source, cursor, "meta");
  if (metaEnd === undefined) return undefined;
  cursor = skipSourceTrivia(source, metaEnd);
  if (source[cursor] !== ".") return undefined;
  cursor = skipSourceTrivia(source, cursor + 1);
  const urlPropertyEnd = consumeIdentifierToken(source, cursor, "url");
  if (urlPropertyEnd === undefined) return undefined;
  cursor = skipSourceTrivia(source, urlPropertyEnd);
  if (source[cursor] !== ")") throw computedSourceError(path);
  return { specifier: literal.specifier, end: cursor + 1 };
}

function readQuotedSpecifier(
  source: string,
  start: number,
  path: string,
): { specifier: string; end: number } | undefined {
  const quote = source[start];
  if (quote !== "\"" && quote !== "'") return undefined;
  let cursor = start + 1;
  while (cursor < source.length) {
    const char = source[cursor]!;
    if (char === "\\") {
      cursor += Math.min(2, source.length - cursor);
      continue;
    }
    if (char === quote) {
      const end = cursor + 1;
      try {
        const [imports] = parse(`import(${source.slice(start, end)})`);
        const specifier = imports[0]?.n;
        if (specifier === undefined) throw new Error("literal did not decode");
        return { specifier, end };
      } catch (error) {
        throw sourceSyntaxError(path, error);
      }
    }
    if (char === "\n" || char === "\r") throw sourceSyntaxError(path);
    cursor += 1;
  }
  throw sourceSyntaxError(path);
}

function readQualifiedIdentifier(source: string, start: number): number | undefined {
  const first = readIdentifierToken(source, start);
  if (first === undefined) return undefined;
  let cursor = first.end;
  while (true) {
    const dot = skipSourceTrivia(source, cursor);
    if (source[dot] !== ".") return cursor;
    const memberStart = skipSourceTrivia(source, dot + 1);
    const member = readIdentifierToken(source, memberStart);
    if (member === undefined) return undefined;
    cursor = member.end;
  }
}

function readIdentifierToken(source: string, start: number): { start: number; end: number } | undefined {
  if (!isIdentifierStart(source[start] ?? "")) return undefined;
  let end = start + 1;
  while (end < source.length && isIdentifierPart(source[end]!)) end += 1;
  return { start, end };
}

function consumeIdentifierToken(source: string, start: number, expected: string): number | undefined {
  return isIdentifierTokenAt(source, start, expected) ? start + expected.length : undefined;
}

function consumeOptionalChainToken(source: string, start: number): number | undefined {
  return source.startsWith("?.", start) ? start + 2 : undefined;
}

function isIdentifierTokenAt(source: string, start: number, expected: string): boolean {
  return source.startsWith(expected, start) &&
    (start === 0 || !isIdentifierPart(source[start - 1]!)) &&
    !isIdentifierPart(source[start + expected.length] ?? "");
}

function skipSourceTrivia(source: string, start: number): number {
  let cursor = start;
  while (cursor < source.length) {
    if (/\s/.test(source[cursor]!)) {
      cursor += 1;
      continue;
    }
    if (source[cursor] === "/" && source[cursor + 1] === "/") {
      cursor += 2;
      while (cursor < source.length && source[cursor] !== "\n" && source[cursor] !== "\r") {
        cursor += 1;
      }
      continue;
    }
    if (source[cursor] === "/" && source[cursor + 1] === "*") {
      const end = source.indexOf("*/", cursor + 2);
      if (end === -1) return source.length;
      cursor = end + 2;
      continue;
    }
    break;
  }
  return cursor;
}

function sourceStatementEndsAt(source: string, start: number): boolean {
  let cursor = start;
  let sawLineTerminator = false;
  while (cursor < source.length) {
    const char = source[cursor]!;
    if (char === "\n" || char === "\r") {
      sawLineTerminator = true;
      cursor += 1;
      continue;
    }
    if (char === " " || char === "\t" || char === "\v" || char === "\f") {
      cursor += 1;
      continue;
    }
    if (char === "/" && source[cursor + 1] === "/") return true;
    if (char === "/" && source[cursor + 1] === "*") {
      const end = source.indexOf("*/", cursor + 2);
      if (end === -1) return false;
      if (/\r|\n/.test(source.slice(cursor + 2, end))) sawLineTerminator = true;
      cursor = end + 2;
      continue;
    }
    break;
  }
  return sawLineTerminator || cursor === source.length || source[cursor] === ";";
}

function computedSourceError(path: string): Error {
  return new Error(`caveman build: computed source dependency is not lockable in ${JSON.stringify(path)}`);
}

function aliasedSourceError(path: string): Error {
  return new Error(`caveman build: aliased source loader is not lockable in ${JSON.stringify(path)}`);
}

const LEGACY_REQUIRE_ALIAS_MEMBERS = new Set(["apply", "bind", "call"]);
const GLOBAL_LOADER_MEMBERS = new Set(["Function", "URL", "eval"]);

function sourceSyntaxError(path: string, cause?: unknown): Error {
  return new Error(
    `caveman build: source module syntax is not lexable in ${JSON.stringify(path)}`,
    cause === undefined ? undefined : { cause },
  );
}

function hasCodeMatch(source: string, pattern: RegExp, code: Uint8Array): boolean {
  const flags = pattern.flags.includes("g") ? pattern.flags : `${pattern.flags}g`;
  const globalPattern = new RegExp(pattern.source, flags);
  for (const match of source.matchAll(globalPattern)) {
    if (code[match.index]) return true;
  }
  return false;
}

/** Marks executable JavaScript/TypeScript while excluding comments and literals. */
function executableCodeMask(source: string): Uint8Array {
  const code = new Uint8Array(source.length);
  const templateExpressionDepths: number[] = [];
  let mode: "code" | "single" | "double" | "template" | "line-comment" | "block-comment" = "code";
  let canStartRegex = true;
  let pendingControlParen = false;
  const controlParens: boolean[] = [];
  let index = 0;

  while (index < source.length) {
    const char = source[index]!;
    const next = source[index + 1];

    if (mode === "line-comment") {
      if (char === "\n" || char === "\r") mode = "code";
      index += 1;
      continue;
    }
    if (mode === "block-comment") {
      if (char === "*" && next === "/") {
        index += 2;
        mode = "code";
      } else {
        index += 1;
      }
      continue;
    }
    if (mode === "single" || mode === "double") {
      const quote = mode === "single" ? "'" : "\"";
      if (char === "\\") index += Math.min(2, source.length - index);
      else {
        index += 1;
        if (char === quote) {
          mode = "code";
          canStartRegex = false;
        }
      }
      continue;
    }
    if (mode === "template") {
      if (char === "\\") {
        index += Math.min(2, source.length - index);
      } else if (char === "`") {
        index += 1;
        mode = "code";
        canStartRegex = false;
      } else if (char === "$" && next === "{") {
        templateExpressionDepths.push(1);
        index += 2;
        mode = "code";
        canStartRegex = true;
      } else {
        index += 1;
      }
      continue;
    }

    if (char === "/" && next === "/") {
      mode = "line-comment";
      index += 2;
      continue;
    }
    if (char === "/" && next === "*") {
      mode = "block-comment";
      index += 2;
      continue;
    }
    if (char === "/" && canStartRegex) {
      index = skipRegexLiteral(source, index);
      canStartRegex = false;
      continue;
    }
    if (char === "'" || char === "\"") {
      mode = char === "'" ? "single" : "double";
      index += 1;
      continue;
    }
    if (char === "`") {
      mode = "template";
      index += 1;
      continue;
    }

    code[index] = 1;
    if (isIdentifierStart(char)) {
      const start = index;
      index += 1;
      while (index < source.length && isIdentifierPart(source[index]!)) {
        code[index] = 1;
        index += 1;
      }
      const identifier = source.slice(start, index);
      pendingControlParen = CONTROL_PAREN_KEYWORDS.has(identifier);
      canStartRegex = REGEX_PREFIX_KEYWORDS.has(identifier);
      continue;
    }
    if (isDecimalDigit(char)) {
      index += 1;
      while (index < source.length && /[\w.]/.test(source[index]!)) {
        code[index] = 1;
        index += 1;
      }
      canStartRegex = false;
      continue;
    }
    if (templateExpressionDepths.length > 0) {
      if (char === "{") {
        templateExpressionDepths[templateExpressionDepths.length - 1]! += 1;
      } else if (char === "}") {
        const last = templateExpressionDepths.length - 1;
        templateExpressionDepths[last]! -= 1;
        if (templateExpressionDepths[last] === 0) {
          templateExpressionDepths.pop();
          mode = "template";
          index += 1;
          continue;
        }
      }
    }
    if ((char === "+" || char === "-") && next === char) {
      // Prefix ++/-- preserve expression-start state; postfix ++/-- preserve
      // expression-end state. Treating each byte as a standalone operator
      // makes division after a postfix operator look like a regex literal and
      // can mask a computed loader from the fail-closed scan.
      code[index + 1] = 1;
      pendingControlParen = false;
      index += 2;
      continue;
    }
    if (char === "(") {
      controlParens.push(pendingControlParen);
      pendingControlParen = false;
    } else if (char === ")") {
      canStartRegex = controlParens.pop() === true;
      pendingControlParen = false;
      index += 1;
      continue;
    } else if (!/\s/.test(char)) {
      pendingControlParen = false;
    }
    if (!/\s/.test(char)) {
      canStartRegex = !")]}.".includes(char);
    }
    index += 1;
  }
  return code;
}

const REGEX_PREFIX_KEYWORDS = new Set([
  "await", "case", "delete", "do", "else", "in", "instanceof", "new", "of",
  "return", "throw", "typeof", "void", "yield",
]);
const CONTROL_PAREN_KEYWORDS = new Set(["catch", "for", "if", "switch", "while", "with"]);

function isIdentifierStart(char: string): boolean {
  return /[A-Za-z_$]/.test(char);
}

function isIdentifierPart(char: string): boolean {
  return /[\w$]/.test(char);
}

function isDecimalDigit(char: string): boolean {
  return char >= "0" && char <= "9";
}

function skipRegexLiteral(source: string, start: number): number {
  let index = start + 1;
  let characterClass = false;
  while (index < source.length) {
    const char = source[index]!;
    if (char === "\\") {
      index += Math.min(2, source.length - index);
      continue;
    }
    if (char === "[") characterClass = true;
    else if (char === "]") characterClass = false;
    else if (char === "/" && !characterClass) {
      index += 1;
      while (index < source.length && /[A-Za-z]/.test(source[index]!)) index += 1;
      return index;
    } else if (char === "\n" || char === "\r") {
      return start + 1;
    }
    index += 1;
  }
  return start + 1;
}

export async function sourceGraphManifest(
  root: string,
  initialFiles: Iterable<string>,
): Promise<SourceManifestEntry[]> {
  const canonicalRoot = await realpath(root);
  const files = await expandSourceGraph(root, initialFiles);
  const manifest: SourceManifestEntry[] = [];
  for (const path of [...files].sort()) {
    const name = isPathWithin(canonicalRoot, path)
      ? relative(canonicalRoot, path)
      : `external/${sha256(path)}/${basename(path)}`;
    manifest.push({ name, sha256: sha256(await readFile(path)) });
  }
  return manifest;
}

export async function sourceGraphSHA256(root: string, initialFiles: Iterable<string>): Promise<string> {
  return sha256(stableStringify(await sourceGraphManifest(root, initialFiles)));
}
