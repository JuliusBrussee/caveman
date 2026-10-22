import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)), '..',
);

function inventory(directory, prefix = '') {
  if (!fs.existsSync(directory)) return [];

  const result = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    const absolute = path.join(directory, entry.name);

    if (entry.isSymbolicLink()) {
      throw new Error(`Refusing generated symlink: ${absolute}`);
    }
    if (entry.isDirectory()) {
      result.push(...inventory(absolute, relative));
    } else if (entry.isFile()) {
      result.push(relative);
    } else {
      throw new Error(`Unexpected generated file type: ${absolute}`);
    }
  }
  return result.sort();
}

function pruneEmptyDirectories(directory) {
  if (!fs.existsSync(directory)) return;

  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const absolute = path.join(directory, entry.name);
    if (entry.isSymbolicLink()) {
      throw new Error(`Refusing generated symlink: ${absolute}`);
    }
    if (!entry.isDirectory()) continue;

    pruneEmptyDirectories(absolute);
    if (fs.readdirSync(absolute).length === 0) {
      fs.rmdirSync(absolute);
    }
  }
}

function refuseSymlinkComponents(root, relative) {
  let current = root;
  for (const part of relative.split('/')) {
    current = path.join(current, part);
    try {
      if (fs.lstatSync(current).isSymbolicLink()) {
        throw new Error(`Refusing symlink: ${current}`);
      }
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
}

// Only human documentation needs links outside the standalone payload.
// Keep skill instructions and executable files byte-identical to their sources.
export function renderPluginDocumentation(relative, bytes) {
  const github = 'https://github.com/JuliusBrussee/caveman';
  const honestNumbers = [
    '../../docs/HONEST-NUMBERS.md',
    `${github}/blob/main/docs/HONEST-NUMBERS.md`,
  ];
  const replacements = {
    'skills/caveman-compress/README.md': [
      [
        '../../docs/assets/dancing-rock.svg',
        'https://raw.githubusercontent.com/JuliusBrussee/caveman/main/docs/assets/dancing-rock.svg',
      ],
      [
        '../../tests/caveman-compress/',
        `${github}/tree/main/tests/caveman-compress/`,
      ],
    ],
    'skills/caveman-compress/SECURITY.md': [
      ['../../SECURITY.md', `${github}/blob/main/SECURITY.md`],
    ],
    'skills/caveman-stats/README.md': [honestNumbers],
    'skills/megacave/README.md': [honestNumbers],
    'skills/ultracave/README.md': [honestNumbers],
  };

  const links = replacements[relative];
  if (!links) return bytes;

  let rendered = bytes.toString('utf8');
  for (const [source, destination] of links) {
    rendered = rendered.split(source).join(destination);
  }
  return Buffer.from(rendered, 'utf8');
}

export function buildClaudePlugin({ root = ROOT, check = false } = {}) {
  const tracked = execFileSync('git', ['ls-files', '-z'], {
    cwd: root,
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 16 * 1024 * 1024,
  }).split('\0').filter(Boolean);

  const skillDirectories = tracked
    .filter(name => /^skills\/[^/]+\/SKILL\.md$/.test(name))
    .map(name => path.posix.dirname(name));

  if (!skillDirectories.length) {
    throw new Error('No tracked canonical skills found');
  }

  const required = new Set([
    '.claude-plugin/plugin.json',
    'commands/caveman-init.md',
    'agents/cavecrew-builder.md',
    'agents/cavecrew-investigator.md',
    'agents/cavecrew-reviewer.md',
    'src/tools/caveman-init.js',
    'LICENSE',
    'LICENSE-MIT',
    'NOTICE',
    'docs/claude-plugin.md',
  ]);

  const trackedSet = new Set(tracked);
  for (const relative of required) {
    if (!trackedSet.has(relative)) {
      throw new Error(`Required source is not tracked: ${relative}`);
    }
  }

  const sources = tracked.filter(relative =>
    required.has(relative)
    || relative.startsWith('src/hooks/')
    || skillDirectories.some(directory =>
      relative.startsWith(`${directory}/`),
    ),
  ).sort();

  // Read all sources before modifying the generated directory.
  const expected = new Map();
  for (const relative of sources) {
    refuseSymlinkComponents(root, relative);
    const source = path.join(root, relative);
    const stat = fs.statSync(source);
    if (!stat.isFile()) throw new Error(`Not a source file: ${relative}`);
    const destination = relative === 'docs/claude-plugin.md'
      ? 'README.md'
      : relative;
    expected.set(destination, {
      bytes: renderPluginDocumentation(relative, fs.readFileSync(source)),
      mode: stat.mode & 0o777,
    });
  }

  const selected = [...expected.keys()].sort();
  const outputRelative = 'plugins/claude';
  refuseSymlinkComponents(root, outputRelative);
  const output = path.join(root, outputRelative);
  const actual = inventory(output);
  const actualSet = new Set(actual);

  const missing = selected.filter(relative => !actualSet.has(relative));
  const extra = actual.filter(relative => !expected.has(relative));
  const changed = selected.filter(relative => {
    if (!actualSet.has(relative)) return false;
    const destination = path.join(output, relative);
    const source = expected.get(relative);
    const contentChanged = !fs.readFileSync(destination).equals(source.bytes);
    const executableChanged = process.platform !== 'win32'
      && (fs.statSync(destination).mode & 0o111) !== (source.mode & 0o111);
    return contentChanged || executableChanged;
  });

  if (check) {
    if (missing.length || extra.length || changed.length) {
      throw new Error(
        'Claude payload is stale. Run node scripts/build-claude-plugin.mjs\n'
        + `Missing: ${missing.join(', ')}\n`
        + `Extra: ${extra.join(', ')}\n`
        + `Changed: ${changed.join(', ')}`,
      );
    }
    return { files: selected.length };
  }

  for (const relative of extra) {
    fs.unlinkSync(path.join(output, relative));
  }

  pruneEmptyDirectories(output);

  for (const relative of [...missing, ...changed]) {
    const destination = path.join(output, relative);
    const source = expected.get(relative);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.writeFileSync(destination, source.bytes);
    fs.chmodSync(destination, source.mode);
  }

  return { files: selected.length };
}

const invokedDirectly = process.argv[1]
  && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (invokedDirectly) {
  try {
    const args = process.argv.slice(2);
    if (args.some(arg => arg !== '--check')) {
      throw new Error('Usage: node scripts/build-claude-plugin.mjs [--check]');
    }
    const check = args.includes('--check');
    const result = buildClaudePlugin({ check });
    console.log(
      `${check ? 'Verified' : 'Generated'} Claude payload: ${result.files} files`,
    );
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
