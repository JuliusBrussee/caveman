#!/usr/bin/env node
// caveman init — drop the always-on caveman activation rule into a target
// repo for every IDE agent we support. Idempotent. Safe to re-run.
//
// Usage:
//   node src/tools/caveman-init.js [target-dir] [--dry-run] [--force] [--only <agent>]
//   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/tools/caveman-init.js | node - [args]
//
// Without args, runs in cwd. Generates the rule files for Cursor, Windsurf,
// Cline, Copilot, and AGENTS.md. Does NOT modify CLAUDE.md or compress
// existing memory files — that's the job of `/caveman:compress`.

const fs = require('fs');
const path = require('path');

// Embedded so the tool works standalone (npx-style) without the src/rules/ dir.
// Mirrors src/rules/caveman-activate.md verbatim — keep these in sync.
const RULE_BODY = `Respond terse like smart caveman. All technical substance stay. Only fluff die.

Rules:
- Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging
- Fragments OK. Short synonyms. Technical terms exact. Code unchanged.
- Pattern: [thing] [action] [reason]. [next step].
- Not: "Sure! I'd be happy to help you with that."
- Yes: "Bug in auth middleware. Fix:"

Switch level: /caveman lite|full|ultra|wenyan
Stop: "stop caveman" or "normal mode"

Auto-Clarity: drop caveman for security warnings, irreversible actions, user confused. Resume after.

Boundaries: code/commits/PRs written normal.
`;

const SENTINEL = 'Respond terse like smart caveman';

const REPO = 'JuliusBrussee/caveman';

// GitHub Copilot CLI reads a repo-level settings file at
// .github/copilot/settings.json (the same file `/settings --repo` writes).
// Registering caveman's marketplace there (extraKnownMarketplaces) and flipping
// its enabledPlugins entry is the declarative repo-wide path to the plugin — no
// manual `copilot plugin install` per contributor (petski, #584).
const COPILOT_CLI_SETTINGS_FILE = '.github/copilot/settings.json';
const COPILOT_MARKETPLACE_KEY = 'caveman';
const COPILOT_PLUGIN_KEY = 'caveman@caveman';

// OpenClaw is a global workspace tool (not per-repo) and needs two write
// targets — a skill folder + a SOUL.md bootstrap block. The shared helper
// lives at bin/lib/openclaw.js; we require it lazily so caveman-init.js
// keeps working when run standalone (curl|node) without the helper on disk.
function loadOpenclawHelper() {
  try {
    return require(path.join(__dirname, '..', '..', 'bin', 'lib', 'openclaw.js'));
  } catch (_) { return null; }
}

// bin/lib/settings.js gives a JSONC-tolerant read; lazy-loaded like the
// openclaw helper so the standalone curl|node path still works without it.
function loadSettingsHelper() {
  try {
    return require(path.join(__dirname, '..', '..', 'bin', 'lib', 'settings.js'));
  } catch (_) { return null; }
}

const AGENTS = [
  { id: 'cursor',   file: '.cursor/rules/caveman.mdc',
    frontmatter: '---\ndescription: "Caveman mode — terse communication, ~75% fewer tokens, full technical accuracy"\nalwaysApply: true\n---\n\n',
    mode: 'replace' },
  { id: 'windsurf', file: '.windsurf/rules/caveman.md',
    frontmatter: '---\ntrigger: always_on\n---\n\n',
    mode: 'replace' },
  { id: 'cline',    file: '.clinerules/caveman.md',
    frontmatter: '',
    mode: 'replace' },
  { id: 'copilot',  file: '.github/copilot-instructions.md',
    frontmatter: '',
    mode: 'append' },
  // Copilot CLI native plugin — repo-level settings, not a rule file. The
  // `installer` escape hatch hands off to processCopilotCliSettings, which
  // JSON-merges (never clobbers) the caveman marketplace + enabledPlugins keys.
  { id: 'copilot-cli', file: COPILOT_CLI_SETTINGS_FILE,
    installer: 'copilotCliSettings' },
  { id: 'opencode', file: '.opencode/AGENTS.md',
    frontmatter: '',
    mode: 'append' },
  { id: 'agents',   file: 'AGENTS.md',
    frontmatter: '',
    mode: 'append' },
  // OpenClaw — global workspace install, not per-repo. The `installer`
  // callback escape hatch bypasses the file/frontmatter/mode triple and
  // hands off to the shared helper. `description` is what `--help` prints.
  { id: 'openclaw', description: '~/.openclaw/workspace/{skills/caveman/, SOUL.md}',
    installer: 'openclaw' },
];

function loadRuleBody() {
  // Prefer the in-repo source-of-truth when available.
  try {
    const local = path.join(__dirname, '..', 'rules', 'caveman-activate.md');
    if (fs.existsSync(local)) return fs.readFileSync(local, 'utf8').trimEnd() + '\n';
  } catch (e) {}
  return RULE_BODY;
}

function processAgent(agent, targetDir, ruleBody, opts) {
  if (agent.installer === 'openclaw') {
    return processOpenclaw(opts);
  }
  if (agent.installer === 'copilotCliSettings') {
    return processCopilotCliSettings(targetDir, opts);
  }
  const fullPath = path.join(targetDir, agent.file);
  const exists = fs.existsSync(fullPath);

  if (!exists) {
    if (!opts.dryRun) {
      fs.mkdirSync(path.dirname(fullPath), { recursive: true });
      fs.writeFileSync(fullPath, agent.frontmatter + ruleBody, { mode: 0o644 });
    }
    return { status: 'added', label: '+' };
  }

  const existing = fs.readFileSync(fullPath, 'utf8');
  if (existing.includes(SENTINEL)) {
    return { status: 'skipped-already-installed', label: '=' };
  }

  if (agent.mode === 'append') {
    if (!opts.dryRun) {
      const sep = existing.endsWith('\n\n') ? '' : (existing.endsWith('\n') ? '\n' : '\n\n');
      fs.writeFileSync(fullPath, existing + sep + ruleBody, { mode: 0o644 });
    }
    return { status: 'appended', label: '~' };
  }

  if (opts.force) {
    if (!opts.dryRun) {
      fs.writeFileSync(fullPath, agent.frontmatter + ruleBody, { mode: 0o644 });
    }
    return { status: 'overwritten', label: '!' };
  }

  return { status: 'skipped-exists', label: '?' };
}

function processOpenclaw(opts) {
  const helper = loadOpenclawHelper();
  if (!helper) {
    return {
      status: 'unsupported-standalone',
      label: 'x',
      detail: '~/.openclaw/workspace (helper unavailable in standalone curl|node mode — use `npx -y github:JuliusBrussee/caveman -- --only openclaw`)',
    };
  }
  const repoRoot = path.resolve(__dirname, '..', '..');
  const log = {
    write: (_) => {},
    note: (_) => {},
    warn: (_) => {},
  };
  const r = helper.installOpenclaw({
    workspace: process.env.OPENCLAW_WORKSPACE || undefined,
    repoRoot,
    dryRun: opts.dryRun,
    force: opts.force,
    log,
  });
  if (!r.ok) {
    return { status: 'skipped-' + (r.reason || 'failed'), label: '?', detail: helper.resolveWorkspace ? helper.resolveWorkspace() : '~/.openclaw/workspace' };
  }
  if (r.dryRun) return { status: 'would-add', label: '+', detail: helper.resolveWorkspace() };
  return { status: 'installed', label: '+', detail: helper.resolveWorkspace() };
}

// JSON-merge the caveman marketplace + enabledPlugins keys into a repo's
// .github/copilot/settings.json. Preserves every other key (the CLI writes
// model defaults, other plugins, etc. into the same file), so we only ever
// touch our two entries. Idempotent; reports skipped when both already point
// at us, and refuses to clobber a file we can't parse.
function processCopilotCliSettings(targetDir, opts) {
  const fullPath = path.join(targetDir, COPILOT_CLI_SETTINGS_FILE);
  const existed = fs.existsSync(fullPath);
  const settings = readCopilotSettings(fullPath);
  if (settings === null) {
    return { status: 'skipped-unparseable', label: '?',
      detail: `${COPILOT_CLI_SETTINGS_FILE} (not valid JSON — left untouched)` };
  }

  const marketplaces = (settings.extraKnownMarketplaces && typeof settings.extraKnownMarketplaces === 'object')
    ? settings.extraKnownMarketplaces : {};
  const plugins = (settings.enabledPlugins && typeof settings.enabledPlugins === 'object')
    ? settings.enabledPlugins : {};

  const mkt = marketplaces[COPILOT_MARKETPLACE_KEY];
  const marketplaceSet = mkt && mkt.source && mkt.source.repo === REPO;
  const pluginSet = plugins[COPILOT_PLUGIN_KEY] === true;
  if (marketplaceSet && pluginSet) {
    return { status: 'skipped-already-installed', label: '=' };
  }

  if (!opts.dryRun) {
    marketplaces[COPILOT_MARKETPLACE_KEY] = { source: { source: 'github', repo: REPO } };
    plugins[COPILOT_PLUGIN_KEY] = true;
    settings.extraKnownMarketplaces = marketplaces;
    settings.enabledPlugins = plugins;
    fs.mkdirSync(path.dirname(fullPath), { recursive: true });
    fs.writeFileSync(fullPath, JSON.stringify(settings, null, 2) + '\n', { mode: 0o644 });
  }
  return existed
    ? { status: 'appended', label: '~' }
    : { status: 'added', label: '+' };
}

// Tolerant read of .github/copilot/settings.json. Prefers bin/lib/settings.js
// (strips JSONC comments) when present; falls back to plain JSON.parse in the
// standalone curl|node path. Returns {} for a missing/empty file, null when the
// existing content can't be parsed (so the caller leaves it untouched).
function readCopilotSettings(p) {
  const helper = loadSettingsHelper();
  if (helper && typeof helper.readSettings === 'function') return helper.readSettings(p);
  if (!fs.existsSync(p)) return {};
  let raw;
  try { raw = fs.readFileSync(p, 'utf8'); } catch (_) { return null; }
  if (!raw.trim()) return {};
  try { return JSON.parse(raw); } catch (_) { return null; }
}

function parseArgs(argv) {
  const opts = { dryRun: false, force: false, only: null, target: process.cwd() };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--force' || a === '-f') opts.force = true;
    else if (a === '--only') { opts.only = argv[++i]; }
    else if (a === '-h' || a === '--help') opts.help = true;
    else if (!a.startsWith('-')) opts.target = path.resolve(a);
  }
  return opts;
}

function help() {
  console.log(`caveman init — drop always-on caveman rule into a target repo

Usage: caveman-init.js [target-dir] [--dry-run] [--force] [--only <agent>]

Defaults to current working directory. Idempotent — safe to re-run.

Targets installed:
${AGENTS.map(a => `  ${a.id.padEnd(10)} ${a.file || a.description || ''}`).join('\n')}

Flags:
  --dry-run   show what would change, do not write
  --force     overwrite existing rule files (default: skip)
  --only <id> only install for one agent (id from list above)
`);
}

function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) { help(); return; }

  console.log(`🪨 caveman init — ${opts.target}${opts.dryRun ? ' (dry run)' : ''}\n`);

  const ruleBody = loadRuleBody();
  const counts = { added: 0, appended: 0, overwritten: 0, skipped: 0 };

  for (const agent of AGENTS) {
    if (opts.only && opts.only !== agent.id) continue;
    const result = processAgent(agent, opts.target, ruleBody, opts);
    const target = agent.file || result.detail || agent.description || agent.id;
    console.log(`  ${result.label} ${target} (${result.status})`);
    if (result.status === 'added' || result.status === 'installed' || result.status === 'would-add') counts.added++;
    else if (result.status === 'appended') counts.appended++;
    else if (result.status === 'overwritten') counts.overwritten++;
    else counts.skipped++;
  }

  console.log(`\n${counts.added} added, ${counts.appended} appended, ` +
              `${counts.overwritten} overwritten, ${counts.skipped} skipped`);
  if (opts.dryRun) console.log('(dry run — no files were written)');
}

if (require.main === module) main();

module.exports = { processAgent, processCopilotCliSettings, loadRuleBody, AGENTS, SENTINEL, RULE_BODY };
