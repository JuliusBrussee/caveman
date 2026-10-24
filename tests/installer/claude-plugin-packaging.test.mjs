import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)), '..', '..',
);

test('Claude marketplace payload does not expose installer dependencies (#1307)', () => {
  const marketplace = JSON.parse(
    fs.readFileSync(path.join(ROOT, '.claude-plugin/marketplace.json'), 'utf8'),
  );
  const plugin = marketplace.plugins.find(entry => entry.name === 'caveman');
  assert.ok(plugin, 'caveman marketplace entry is missing');
  assert.equal(typeof plugin.source, 'string');

  const payload = path.resolve(ROOT, plugin.source);
  const manifestPath = path.join(payload, 'package.json');
  const manifest = fs.existsSync(manifestPath)
    ? JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
    : {};

  assert.deepEqual(
    manifest.dependencies ?? {},
    {},
    'Claude plugin root exposes runtime dependencies belonging to the installer',
  );

  for (const lockfile of ['pnpm-lock.yaml', 'yarn.lock']) {
    assert.equal(
      fs.existsSync(path.join(payload, lockfile)),
      false,
      `Claude plugin root contains unsupported ${lockfile}`,
    );
  }
});

test('Claude marketplace payload preserves skills, agents, command and hooks', () => {
  const marketplace = JSON.parse(
    fs.readFileSync(path.join(ROOT, '.claude-plugin/marketplace.json'), 'utf8'),
  );
  const plugin = marketplace.plugins.find(entry => entry.name === 'caveman');
  assert.ok(plugin, 'caveman marketplace entry is missing');
  const payload = path.resolve(ROOT, plugin.source);

  const skillNames = directory => fs.readdirSync(directory).filter(name =>
    fs.existsSync(path.join(directory, name, 'SKILL.md')),
  ).sort();

  const canonicalSkills = skillNames(path.join(ROOT, 'skills'));
  assert.ok(canonicalSkills.length > 0, 'canonical skills are missing');
  assert.deepEqual(
    skillNames(path.join(payload, 'skills')),
    canonicalSkills,
    'installed skill inventory differs from canonical skills',
  );

  const required = [
    '.claude-plugin/plugin.json',
    'commands/caveman-init.md',
    'agents/cavecrew-builder.md',
    'agents/cavecrew-investigator.md',
    'agents/cavecrew-reviewer.md',
    'src/hooks/caveman-activate.js',
    'src/hooks/caveman-mode-tracker.js',
    'src/hooks/caveman-stats.js',
    'src/hooks/caveman-config.js',
    'src/hooks/caveman-parse.js',
    'src/hooks/cavecrew-model-overrides.js',
    'src/hooks/caveman-statusline.sh',
    'src/hooks/caveman-statusline.ps1',
    'src/hooks/package.json',
    ...canonicalSkills.map(name => `skills/${name}/SKILL.md`),
  ];

  for (const relative of required) {
    assert.deepEqual(
      fs.readFileSync(path.join(payload, relative)),
      fs.readFileSync(path.join(ROOT, relative)),
      `Missing or changed plugin component: ${relative}`,
    );
  }
});

test('generator detects drift without writing and repairs it explicitly', async () => {
  const { mkdtempSync, rmSync } = await import('node:fs');
  const { tmpdir } = await import('node:os');
  const { execFileSync } = await import('node:child_process');
  const { buildClaudePlugin } = await import(
    '../../scripts/build-claude-plugin.mjs'
  );

  const fixture = mkdtempSync(path.join(tmpdir(), 'caveman-generator-'));
  const write = (relative, content) => {
    const destination = path.join(fixture, relative);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.writeFileSync(destination, content);
  };
  const git = (...args) => execFileSync('git', args, {
    cwd: fixture,
    encoding: 'utf8',
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  try {
    const sources = [
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
      'skills/example/SKILL.md',
      'skills/example/scripts/helper.py',
      'src/hooks/package.json',
      'src/hooks/example.js',
    ];

    for (const relative of sources) {
      write(relative, `fixture content: ${relative}\n`);
    }
    write('package.json', '{"dependencies":{"installer-only":"1.0.0"}}\n');
    write('pnpm-lock.yaml', 'lockfileVersion: 9\n');

    git('init', '--quiet');
    git('add', '.');

    const output = path.join(fixture, 'plugins/claude');
    assert.throws(
      () => buildClaudePlugin({ root: fixture, check: true }),
      /payload is stale/,
    );
    assert.equal(fs.existsSync(output), false, '--check created output');

    buildClaudePlugin({ root: fixture });
    buildClaudePlugin({ root: fixture, check: true });

    for (const relative of sources) {
      const destination = relative === 'docs/claude-plugin.md'
        ? 'README.md'
        : relative;
      assert.deepEqual(
        fs.readFileSync(path.join(output, destination)),
        fs.readFileSync(path.join(fixture, relative)),
        relative,
      );
    }
    assert.equal(fs.existsSync(path.join(output, 'package.json')), false);
    assert.equal(fs.existsSync(path.join(output, 'pnpm-lock.yaml')), false);

    const readme = path.join(output, 'README.md');
    const before = fs.statSync(readme).mtimeMs;
    buildClaudePlugin({ root: fixture });
    assert.equal(
      fs.statSync(readme).mtimeMs, before,
      'unchanged generation rewrote a file',
    );

    fs.unlinkSync(path.join(output, 'skills/example/scripts/helper.py'));
    fs.writeFileSync(readme, 'stale content\n');
    write('plugins/claude/obsolete.txt', 'obsolete\n');

    assert.throws(
      () => buildClaudePlugin({ root: fixture, check: true }),
      /payload is stale/,
    );
    assert.equal(fs.readFileSync(readme, 'utf8'), 'stale content\n');
    assert.equal(fs.existsSync(path.join(output, 'obsolete.txt')), true);
    assert.equal(
      fs.existsSync(path.join(output, 'skills/example/scripts/helper.py')),
      false,
      '--check repaired a missing file',
    );

    buildClaudePlugin({ root: fixture });
    buildClaudePlugin({ root: fixture, check: true });
    assert.equal(fs.existsSync(path.join(output, 'obsolete.txt')), false);
    assert.deepEqual(
      fs.readFileSync(readme),
      fs.readFileSync(path.join(fixture, 'docs/claude-plugin.md')),
    );
    assert.equal(
      fs.existsSync(path.join(output, 'skills/example/scripts/helper.py')),
      true,
    );

    // Newly staged skills and their supporting files must ship.
    write('skills/added/SKILL.md', 'New skill instructions\n');
    write('skills/added/assets/example.txt', 'Supporting content\n');
    git('add', '--', 'skills/added');

    assert.throws(
      () => buildClaudePlugin({ root: fixture, check: true }),
      /payload is stale/,
    );
    buildClaudePlugin({ root: fixture });
    assert.equal(
      fs.readFileSync(path.join(output, 'skills/added/SKILL.md'), 'utf8'),
      'New skill instructions\n',
    );
    assert.equal(
      fs.readFileSync(path.join(output, 'skills/added/assets/example.txt'), 'utf8'),
      'Supporting content\n',
    );

    // A canonical directory can become a file at the same path.
    git('rm', '-r', '-f', '--', 'skills/added/assets');
    write('skills/added/assets', 'Replacement file\n');
    git('add', '--', 'skills/added/assets');

    assert.throws(
      () => buildClaudePlugin({ root: fixture, check: true }),
      /payload is stale/,
    );
    assert.equal(
      fs.statSync(path.join(output, 'skills/added/assets')).isDirectory(),
      true,
      '--check modified the old directory',
    );

    buildClaudePlugin({ root: fixture });
    assert.equal(
      fs.readFileSync(path.join(output, 'skills/added/assets'), 'utf8'),
      'Replacement file\n',
    );
    buildClaudePlugin({ root: fixture, check: true });

    // Removed canonical skills must not survive in the shipped payload.
    git('rm', '-r', '-f', '--', 'skills/example');
    assert.throws(
      () => buildClaudePlugin({ root: fixture, check: true }),
      /payload is stale/,
    );
    buildClaudePlugin({ root: fixture });
    assert.equal(
      fs.existsSync(path.join(output, 'skills/example/SKILL.md')),
      false,
    );
    assert.equal(
      fs.existsSync(path.join(output, 'skills/example/scripts/helper.py')),
      false,
    );
    buildClaudePlugin({ root: fixture, check: true });
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('documentation links adapt without changing skill instructions', async () => {
  const { renderPluginDocumentation } = await import(
    '../../scripts/build-claude-plugin.mjs'
  );
  const render = (file, text) =>
    renderPluginDocumentation(file, Buffer.from(text)).toString('utf8');

  assert.equal(
    render(
      'skills/caveman-compress/SECURITY.md',
      '[Policy](../../SECURITY.md#scanner-warnings)',
    ),
    '[Policy](https://github.com/JuliusBrussee/caveman/blob/main/SECURITY.md#scanner-warnings)',
  );

  assert.equal(
    render(
      'skills/caveman-compress/README.md',
      '<img src="../../docs/assets/dancing-rock.svg"/>\n'
      + '[Fixtures](../../tests/caveman-compress/)',
    ),
    '<img src="https://raw.githubusercontent.com/JuliusBrussee/caveman/main/docs/assets/dancing-rock.svg"/>\n'
      + '[Fixtures](https://github.com/JuliusBrussee/caveman/tree/main/tests/caveman-compress/)',
  );

  for (const skill of ['caveman-stats', 'megacave', 'ultracave']) {
    assert.equal(
      render(`skills/${skill}/README.md`, '[Evidence](../../docs/HONEST-NUMBERS.md)'),
      '[Evidence](https://github.com/JuliusBrussee/caveman/blob/main/docs/HONEST-NUMBERS.md)',
    );
  }

  const instruction = Buffer.from('[Evidence](../../docs/HONEST-NUMBERS.md)\n');
  assert.deepEqual(
    renderPluginDocumentation('skills/ultracave/SKILL.md', instruction),
    instruction,
  );
  assert.equal(
    render('skills/cavecrew/README.md', '[Agent](../../agents/cavecrew-builder.md)'),
    '[Agent](../../agents/cavecrew-builder.md)',
  );
});
