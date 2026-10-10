#!/usr/bin/env node
// caveman — unified cross-platform installer.
//
// One Node script replaces the old install.sh + install.ps1 + src/hooks/install.sh
// + src/hooks/install.ps1 quartet. Single source of truth. Works on macOS, Linux,
// and Windows (PowerShell or cmd) without any of the bash/PS1 quoting bugs
// that previously broke the JSON merge step (issue #249).
//
// Distribution:
//   Local clone: node installer/install.js [flags]
//   curl|bash:   delegated from install.sh shim → npx -y github:JuliusBrussee/caveman -- [flags]
//   Windows:     pwsh install.ps1 [flags] → same npx delegation
//
// Pure stdlib, zero npm runtime deps.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const child_process = require('child_process');
const readline = require('readline');
const crypto = require('crypto');

const SETTINGS = require('./lib/settings');
const OPENCLAW = require('./lib/openclaw');
const OWNED = require('./lib/owned-install');
const PROVIDER_SKILLS = require('./lib/provider-skills');
const { transformOpencodeAgentFrontmatter } = require('./lib/opencode-agent');
const CURSOR_NATIVE = require('./lib/cursor-native');
const HOST_HOOKS = require('./lib/host-hooks');
const PORTABLE = require('./lib/portable-process');
const PLATFORM_PATHS = require('./lib/platform-paths');
const { parseCommandArgs } = require('./lib/command-args');

const REPO = 'JuliusBrussee/caveman';
// Mirrors the `engines.node` floor in package.json. Hardcoded rather than read
// from disk because this file also runs detached from a checkout (the curl
// fallback path); `tests/installer/node-floor.test.mjs` fails the build if the
// two drift apart.
const MIN_NODE_MAJOR = 18;
// Pin remote fetches to a release tag, not the moving `main`
// branch (issue #261). A push to main must never silently change what a
// curl|bash / detached-script install downloads and executes. Bump this to
// the new tag on every release (CI release step) AFTER regenerating
// src/hooks/checksums.sha256 so the integrity manifest matches the ref.
// Overridable via CAVEMAN_REF for testing against a branch.
const PINNED_REF = process.env.CAVEMAN_REF || 'v3.2.0';
const OPENCLAW_SKILL_VERSION = /^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(PINNED_REF)
  ? PINNED_REF.replace(/^v/, '')
  : undefined;
const RAW_BASE = `https://raw.githubusercontent.com/${REPO}/${PINNED_REF}`;
const HOOKS_REMOTE = `${RAW_BASE}/src/hooks`;
const MCP_SHRINK_PKG = 'caveman-shrink';
// Hook files to copy. Statusline ships in both .sh (macOS/Linux) and .ps1
// (Windows) flavors — copy both regardless of host OS so a roaming
// $CLAUDE_CONFIG_DIR (e.g. dotfiles repo) keeps working across platforms.
const HOOK_FILES = [
  'package.json',
  'caveman-config.js',
  'caveman-parse.js',
  'caveman-activate.js',
  'caveman-mode-tracker.js',
  'caveman-stats.js',
  'caveman-statusline.sh',
  'caveman-statusline.ps1',
  'cavecrew-model-overrides.js',
];

// hooks/package.json is the ONE entry in HOOK_FILES with no `caveman` in its
// name, because it is not ours: it pins the module system for EVERY plugin's
// .js hooks in that directory. We used to copy it in unconditionally and delete
// it outright on uninstall, so a co-installed plugin shipping ESM hooks had its
// {"type":"module"} silently replaced with {"type":"commonjs"} — and then removed
// altogether. Install leaves a foreign manifest alone; uninstall deletes only a
// manifest whose content is exactly the one we ship.
const HOOKS_MANIFEST = 'package.json';
function hooksManifestIsOurs(p) {
  try {
    const parsed = JSON.parse(fs.readFileSync(p, 'utf8'));
    return !!parsed && parsed.type === 'commonjs' && Object.keys(parsed).length === 1;
  } catch (_) {
    return false;
  }
}

// ── Argv ───────────────────────────────────────────────────────────────────
function parseArgs(argv) {
  const opts = {
    dryRun: false, force: false,
    withHooks: 'auto', withInit: false, withMcpShrink: false,
    all: false, minimal: false, listOnly: false, noColor: false,
    only: [], uninstall: false, nonInteractive: false,
    configDir: null, help: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    // --with-mcp-shrink=<upstream cmd>  (handled before the switch so the
    // GNU-style =value form is recognized). Bare --with-mcp-shrink falls
    // through to the switch and is rejected — caveman-shrink is a proxy
    // and a stub registration just lands the user in a broken-MCP loop (#474).
    if (a.startsWith('--with-mcp-shrink=')) {
      const raw = a.slice('--with-mcp-shrink='.length);
      opts.withMcpShrink = upstreamArgs(raw);
      continue;
    }
    switch (a) {
      case '--dry-run': opts.dryRun = true; break;
      case '--force': opts.force = true; break;
      // Legacy flag: there is no longer an all-agent fallback to suppress.
      case '--skip-skills': break;
      case '--with-hooks': opts.withHooks = true; break;
      case '--no-hooks': opts.withHooks = false; break;
      case '--with-init': opts.withInit = true; break;
      case '--with-mcp-shrink': {
        const v = argv[i + 1];
        if (v && !v.startsWith('--')) {
          i++;
          opts.withMcpShrink = upstreamArgs(v);
        } else {
          die('error: --with-mcp-shrink requires an upstream command — caveman-shrink\n' +
              '  is a proxy and exits immediately without one. Pass the upstream:\n' +
              '  --with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /path"');
        }
        break;
      }
      case '--no-mcp-shrink': opts.withMcpShrink = false; break;
      case '--all': opts.all = true; break;
      case '--minimal': opts.minimal = true; break;
      case '--list': opts.listOnly = true; break;
      case '--no-color': opts.noColor = true; break;
      case '--uninstall': case '-u': opts.uninstall = true; break;
      case '--non-interactive': opts.nonInteractive = true; break;
      case '-h': case '--help': opts.help = true; break;
      // POSIX end-of-options marker. Older curl|bash flows pipe `-- --only foo`
      // through npx; some npx versions forward the literal `--`. Accept and
      // ignore so we never regress on the headline install command.
      case '--': break;
      case '--only': {
        const v = argv[++i];
        if (!v) die('error: --only requires an argument');
        opts.only.push(v === 'aider' ? 'aider-desk' : v);
        break;
      }
      case '--config-dir': {
        const v = argv[++i];
        if (!v || v.startsWith('--')) die('error: --config-dir requires a path');
        opts.configDir = expandHome(v);
        break;
      }
      default:
        die(`error: unknown flag: ${a}\nrun 'caveman --help' for usage`);
    }
  }
  if (opts.all && opts.minimal) die('error: --all and --minimal are mutually exclusive');
  if (opts.uninstall && opts.only.length) {
    die('error: --uninstall removes caveman from every agent; it cannot be limited with --only.\n  run it without --only');
  }
  // --all turns on per-repo init only. It deliberately does NOT force:
  //   • withHooks — left at 'auto' so installClaude() can skip standalone
  //     settings.json wiring when the plugin manifest already wires the hooks
  //     (duplicate registration fires both per event — issue #392).
  //   • withMcpShrink — caveman-shrink is a proxy that needs an upstream
  //     command, so there's no sensible "everything on" default (issue #474).
  //     Opt in explicitly with --with-mcp-shrink="<upstream cmd>".
  if (opts.all) { opts.withInit = true; }
  if (opts.minimal) { opts.withHooks = false; opts.withInit = false; opts.withMcpShrink = false; }
  // Validate --only ids against the provider matrix. PROVIDERS is defined later
  // in the file but is in scope by the time this function runs.
  if (opts.only.length) {
    const knownIds = new Set(PROVIDERS.map(p => p.id));
    for (const id of opts.only) {
      if (!knownIds.has(id)) {
        die(`error: unknown agent: ${id}\n  see 'caveman --list' for valid ids`);
      }
    }
  }
  return opts;
}

function die(msg) { process.stderr.write(msg + '\n'); process.exit(2); }

function upstreamArgs(value) {
  try { return parseCommandArgs(value); }
  catch (error) {
    die(`error: --with-mcp-shrink requires an upstream command: ${error.message}`);
  }
}

// ── Color helpers ──────────────────────────────────────────────────────────
function makeChalk(noColor) {
  const useColor = !noColor && process.stdout.isTTY && !process.env.NO_COLOR;
  const wrap = (codes) => (s) => useColor ? `\x1b[${codes}m${s}\x1b[0m` : s;
  return {
    orange: wrap('38;5;172'), dim: wrap('2'), red: wrap('31'),
    green: wrap('32'), yellow: wrap('33'),
  };
}

// ── Env guards ─────────────────────────────────────────────────────────────
function checkWslWindowsNode() {
  if (process.platform !== 'win32') return;
  // Windows-Node executing inside WSL has homedir like /mnt/c/Users/... which
  // breaks every config-dir resolution. Detect and abort with a clear hint.
  if (process.env.WSL_DISTRO_NAME) {
    die('caveman: detected Windows Node.js running inside WSL.\n' +
        '         Install Linux-native Node inside your WSL distro and re-run there.\n' +
        '         (WSL_DISTRO_NAME=' + process.env.WSL_DISTRO_NAME + ')');
  }
  try {
    const v = fs.readFileSync('/proc/version', 'utf8').toLowerCase();
    if (v.includes('microsoft') || v.includes('wsl')) {
      die('caveman: detected Windows Node.js running inside WSL (/proc/version).\n' +
          '         Install Linux-native Node inside your WSL distro and re-run there.');
    }
  } catch (_) { /* /proc/version absent on real Windows — fine */ }
}

function checkNodeVersion() {
  const major = parseInt(process.versions.node.split('.')[0], 10);
  if (major < 18) die(`caveman: Node ${process.versions.node} too old. Need Node ≥18. https://nodejs.org`);
}

// ── Provider matrix ────────────────────────────────────────────────────────
// Single source of truth. Replaces the 6 parallel bash arrays in old install.sh.
//
// Detection rules:
//   - `command:<bin>` — bin on PATH. Most reliable signal.
//   - `vscode-ext:<needle>` / `cursor-ext:<needle>` — extension dir name match.
//   - `jetbrains-plugin:<needle>` — JetBrains plugin dir match.
//   - `dir:<path>` / `file:<path>` — kept ONLY for agents that ship no CLI
//      and no extension marker (true dir-only signal).
//
// `soft: true` means detection is best-effort (config-dir only or no
// reliable probe). Soft providers are EXCLUDED from auto-detect and only
// install when the user passes `--only <id>`. This stops the installer from
// firing `npx skills add ...` against agents the user has never installed
// just because some other tool created `~/.foo` along the way.
const PROVIDERS = [
  { id: 'claude',     label: 'Claude Code',         mech: 'claude plugin install',         detect: 'command:claude' },
  { id: 'gemini',     label: 'Gemini CLI',          mech: 'gemini extensions install',     detect: 'command:gemini' },
  { id: 'opencode',   label: 'opencode',            mech: 'native opencode plugin',        detect: 'command:opencode' },
  { id: 'omp',        label: 'Oh My Pi (OMP)',      mech: 'native OMP plugin',             detect: 'command:omp' },
  { id: 'openclaw',   label: 'OpenClaw',            mech: 'workspace skill + SOUL.md',     detect: 'command:openclaw||dir:$HOME/.openclaw/workspace' },
  { id: 'codex',      label: 'Codex CLI',           mech: 'npx skills add (codex) + SessionStart hook', detect: 'command:codex', profile: 'codex' },

  // IDE / VS Code-family — extension probes are precise. Cursor/Windsurf also
  // ship CLI binaries; we drop the dir fallback because the dir lingers after
  // uninstall and false-positives heavily.
  { id: 'cursor',     label: 'Cursor',              mech: 'npx skills add (cursor)',       detect: 'command:cursor||macapp:Cursor', profile: 'cursor' },
  { id: 'windsurf',   label: 'Windsurf',            mech: 'npx skills add (windsurf)',     detect: 'command:windsurf||macapp:Windsurf', profile: 'windsurf' },
  { id: 'cline',      label: 'Cline',               mech: 'npx skills add (cline)',        detect: 'vscode-ext:^saoudrizwan\\.claude-dev||vscode-ext:^saoudrizwan\\.cline-nightly||command:cline', profile: 'cline' },
  { id: 'continue',   label: 'Continue',            mech: 'native skills copy',     detect: 'vscode-ext:^continue\\.continue-', profile: 'continue' },
  { id: 'kilo',       label: 'Kilo Code',           mech: 'npx skills add (kilo)',         detect: 'vscode-ext:kilocode', profile: 'kilo' },
  { id: 'roo',        label: 'Roo Code',            mech: 'npx skills add (roo)',          detect: 'vscode-ext:^rooveterinaryinc\\.roo-cline', profile: 'roo' },
  { id: 'augment',    label: 'Augment Code',        mech: 'npx skills add (augment)',      detect: 'vscode-ext:^augment\\.vscode-augment-||jetbrains-plugin:augment', profile: 'augment' },

  // GitHub Copilot: the standalone Copilot CLI (`copilot` binary, reads the
  // profile's ~/.copilot/skills) or the VS Code / Cursor extension dirs (no
  // `gh` CLI needed). The extension probes came first because Copilot used to
  // ship only as an editor extension (#336); the CLI probe is back for #1189.
  // AWS Copilot CLI also installs `copilot`, so the binary needs GitHub's
  // ~/.copilot config dir (made on first launch) or COPILOT_HOME beside it;
  // before either exists, use --only.
  { id: 'copilot',    label: 'GitHub Copilot',      mech: 'npx skills add (github-copilot)', detect: 'command:copilot&&dir:$HOME/.copilot||command:copilot&&env:COPILOT_HOME||vscode-ext:github.copilot||vscode-ext:github.copilot-chat||cursor-ext:github.copilot', profile: 'github-copilot' },

  // CLI agents — require the binary. The `||dir:~/.foo` fallbacks were the
  // main source of false positives (warp, kiro, junie etc. leave config dirs
  // behind on uninstall). goose, forge and bob also name unrelated tools (a DB
  // migrator, Foundry, a neovim manager), so those need the agent's dir too.
  { id: 'hermes',     label: 'Hermes Agent',        mech: 'native hermes skills copy',     detect: 'command:hermes' },
  { id: 'aider-desk', label: 'Aider Desk',          mech: 'native skills copy',   detect: 'command:aider-desk||macapp:aider-desk', profile: 'aider-desk' },
  { id: 'antigravity-cli', label: 'Antigravity CLI', mech: 'agy plugin install',           detect: 'command:agy' },
  { id: 'amp',        label: 'Sourcegraph Amp',     mech: 'npx skills add (amp)',          detect: 'command:amp',             profile: 'amp' },
  { id: 'bob',        label: 'IBM Bob',             mech: 'npx skills add (bob)',          detect: 'command:bob&&dir:$HOME/.bob', profile: 'bob' },
  { id: 'codebuddy',  label: 'CodeBuddy Code',      mech: 'npx skills add (codebuddy)',    detect: 'command:codebuddy', profile: 'codebuddy' },
  { id: 'crush',      label: 'Crush',               mech: 'npx skills add (crush)',        detect: 'command:crush', profile: 'crush' },
  { id: 'devin',      label: 'Devin (terminal)',    mech: 'npx skills add (devin)',        detect: 'command:devin', profile: 'devin' },
  { id: 'droid',      label: 'Droid (Factory)',     mech: 'npx skills add (droid)',        detect: 'command:droid', profile: 'droid' },
  { id: 'forgecode',  label: 'ForgeCode',           mech: 'npx skills add (forgecode)',    detect: 'command:forge&&dir:$HOME/.forge', profile: 'forgecode' },
  { id: 'goose',      label: 'Block Goose',         mech: 'npx skills add (goose)',        detect: 'command:goose&&dir:$XDG_CONFIG_HOME/goose||command:goose&&dir:$APPDATA/Block/goose', profile: 'goose' },
  { id: 'grok',       label: 'Grok Build',          mech: 'native skills copy',     detect: 'command:grok' },
  { id: 'iflow',      label: 'iFlow CLI',           mech: 'npx skills add (iflow-cli)',    detect: 'command:iflow', profile: 'iflow-cli' },
  { id: 'kiro',       label: 'Kiro CLI',            mech: 'npx skills add (kiro-cli)',     detect: 'command:kiro-cli||command:kiro', profile: 'kiro-cli' },
  { id: 'mistral',    label: 'Mistral Vibe',        mech: 'npx skills add (mistral-vibe)', detect: 'command:vibe||command:mistral', profile: 'mistral-vibe' },
  { id: 'openhands',  label: 'OpenHands',           mech: 'npx skills add (openhands)',    detect: 'command:openhands', profile: 'openhands' },
  { id: 'qwen',       label: 'Qwen Code',           mech: 'npx skills add (qwen-code)',    detect: 'command:qwen', profile: 'qwen-code' },
  { id: 'rovodev',    label: 'Atlassian Rovo Dev',  mech: 'npx skills add (rovodev)',      detect: 'command:rovodev', profile: 'rovodev' },
  { id: 'tabnine',    label: 'Tabnine CLI',         mech: 'npx skills add (tabnine-cli)',  detect: 'command:tabnine', profile: 'tabnine-cli' },
  { id: 'trae',       label: 'Trae',                mech: 'npx skills add (trae)',         detect: 'command:trae', profile: 'trae' },
  { id: 'warp',       label: 'Warp',                mech: 'npx skills add (warp)',         detect: 'command:warp', profile: 'warp' },
  { id: 'replit',     label: 'Replit Agent',        mech: 'npx skills add (replit, project)', detect: 'command:replit', profile: 'replit', skillsScope: 'project' },

  // Soft (opt-in via --only) — no reliable always-on probe.
  // junie: ships only as a JetBrains plugin; jetbrains-plugin probe walks
  //   ~/.config/JetBrains looking for "junie" — fires on stale plugin caches.
  // qoder: dir-only.
  // antigravity: lives at ~/.gemini/antigravity which is created by the
  //   gemini CLI on first use — not a reliable signal of antigravity itself.
  { id: 'junie',      label: 'JetBrains Junie',     mech: 'npx skills add (junie)',        detect: 'jetbrains-plugin:junie', profile: 'junie', soft: true },
  { id: 'qoder',      label: 'Qoder',               mech: 'npx skills add (qoder)',        detect: 'dir:$HOME/.qoder', profile: 'qoder', soft: true },
  { id: 'antigravity',label: 'Antigravity IDE',     mech: 'native skills copy',  detect: 'dir:$HOME/.gemini/antigravity', profile: 'antigravity', soft: true },
  // Antigravity 2.0 has a distinct global skills root; require explicit selection.
  { id: 'antigravity-2', label: 'Antigravity 2.0',   mech: 'native skills copy',             detect: '', soft: true },
];

// ── Detection ─────────────────────────────────────────────────────────────
function hasCmd(cmd) {
  try {
    if (process.platform === 'win32') {
      return PORTABLE.resolveWindowsCommand(cmd, process.env) !== null;
    }
    const r = child_process.spawnSync('sh', ['-c', `command -v ${shellEscape(cmd)}`], { stdio: 'ignore' });
    return r.status === 0;
  } catch (_) { return false; }
}

function shellEscape(s) { return `'${String(s).replace(/'/g, `'\\''`)}'`; }

function expandHome(p) { return p.replace(/^\$HOME/, os.homedir()).replace(/^~/, os.homedir()); }

// A detect path may also start with $XDG_CONFIG_HOME (unset or relative:
// ~/.config, as XDG says) or $APPDATA (Windows; unset, it names nothing: null).
function expandDetectPath(p) {
  const xdg = process.env.XDG_CONFIG_HOME;
  const roots = { XDG_CONFIG_HOME: xdg && path.isAbsolute(xdg) ? xdg : path.join(os.homedir(), '.config'), APPDATA: process.env.APPDATA };
  const m = /^\$(XDG_CONFIG_HOME|APPDATA)(?=\/|$)/.exec(p);
  if (!m) return expandHome(p);
  return roots[m[1]] ? roots[m[1]] + p.slice(m[0].length) : null;
}

function vscodeExtPresent(needle) {
  const home = os.homedir();
  const roots = [
    path.join(home, '.vscode/extensions'),
    path.join(home, '.vscode-server/extensions'),
    path.join(home, '.cursor/extensions'),
    path.join(home, '.windsurf/extensions'),
  ];
  const re = new RegExp(needle, 'i');
  for (const r of roots) {
    if (!fs.existsSync(r)) continue;
    let entries;
    try { entries = fs.readdirSync(r); } catch (_) { continue; }
    if (entries.some(e => re.test(e))) return true;
  }
  return false;
}

function cursorExtPresent(needle) {
  const dir = path.join(os.homedir(), '.cursor/extensions');
  if (!fs.existsSync(dir)) return false;
  const re = new RegExp(needle, 'i');
  try { return fs.readdirSync(dir).some(e => re.test(e)); } catch (_) { return false; }
}

function jetbrainsPresent() {
  const home = os.homedir();
  return PLATFORM_PATHS.jetbrainsRoots(home).some(root => fs.existsSync(root));
}

function jetbrainsPluginPresent(needle) {
  const home = os.homedir();
  const roots = PLATFORM_PATHS.jetbrainsRoots(home);
  const re = new RegExp(needle, 'i');
  for (const r of roots) {
    if (!fs.existsSync(r)) continue;
    if (walkDir(r, 4).some(p => re.test(path.basename(p)))) return true;
  }
  return false;
}

function walkDir(root, depth) {
  const out = [];
  if (depth < 0) return out;
  let entries;
  try { entries = fs.readdirSync(root, { withFileTypes: true }); } catch (_) { return out; }
  for (const e of entries) {
    const full = path.join(root, e.name);
    if (e.isDirectory()) { out.push(full); out.push(...walkDir(full, depth - 1)); }
  }
  return out;
}

function macAppPresent(name) {
  if (process.platform !== 'darwin') return false;
  const candidates = [
    `/Applications/${name}.app`,
    path.join(os.homedir(), 'Applications', `${name}.app`),
  ];
  return candidates.some(p => fs.existsSync(p));
}

// `||` separates alternatives; `&&` joins terms that must all hold.
function detectMatch(spec) {
  if (!spec) return false;
  return spec.split('||').some((clause) => {
    const terms = clause.split('&&').map((t) => t.trim()).filter(Boolean);
    return terms.length > 0 && terms.every(detectTerm);
  });
}

function detectTerm(c) {
  const colon = c.indexOf(':');
  const kind = colon === -1 ? c : c.slice(0, colon);
  const val  = colon === -1 ? '' : expandDetectPath(c.slice(colon + 1));
  if (val === null) return false;
  switch (kind) {
    case 'command':           return hasCmd(val);
    case 'dir':               return safeStat(val, 'isDirectory');
    case 'env':               return !!process.env[val];
    case 'file':              return safeStat(val, 'isFile');
    case 'macapp':            return macAppPresent(val);
    case 'vscode-ext':        return vscodeExtPresent(val);
    case 'cursor-ext':        return cursorExtPresent(val);
    case 'jetbrains-config':  return jetbrainsPresent();
    case 'jetbrains-plugin':  return jetbrainsPluginPresent(val);
  }
  return false;
}

function safeStat(p, method) {
  try { return fs.statSync(p)[method](); } catch (_) { return false; }
}

// ── Repo root resolution ───────────────────────────────────────────────────
function detectRepoRoot() {
  // installer/install.js sits at <repo>/installer/install.js. Walk up one.
  const here = path.dirname(__filename);
  const root = path.resolve(here, '..');
  if (fs.existsSync(path.join(root, 'src', 'hooks')) &&
      fs.existsSync(path.join(root, 'agents')) &&
      fs.existsSync(path.join(root, 'skills'))) {
    return root;
  }
  return null;
}

// ── Run helpers ────────────────────────────────────────────────────────────
// On Windows, npm/npx/claude/gemini/codex ship as `.cmd` Node shims. Resolve
// through PATH/PATHEXT and launch their Node entrypoint directly. This keeps
// argument bytes intact without putting user-controlled paths into cmd.exe.
const IS_WIN = process.platform === 'win32';

function spawnXplat(cmd, args, opts) {
  if (IS_WIN) {
    try {
      const invocation = PORTABLE.portableInvocation(cmd, args, {
        env: (opts && opts.env) || process.env,
        allowBun: cmd === 'omp',
      });
      return child_process.spawnSync(invocation.command, invocation.args, opts || {});
    } catch (error) {
      return { status: null, signal: null, stdout: '', stderr: '', error };
    }
  }
  return child_process.spawnSync(cmd, args, opts || {});
}

function runSpawn(cmd, args, opts, dry) {
  // Display only: quote what a pasted line would mangle (`--skill *` globbed
  // to cwd). The spawn below gets the raw argv, no shell.
  const shown = args.map(a => /[^\w@%+=:,./-]/.test(a) ? shellEscape(a) : a).join(' ');
  if (dry) { process.stdout.write(`  would run: ${cmd} ${shown}\n`); return { status: 0 }; }
  process.stdout.write(`  $ ${cmd} ${shown}\n`);
  const result = spawnXplat(cmd, args, Object.assign({ stdio: 'inherit' }, opts || {}));
  if (result && result.error) process.stderr.write(`  ${result.error.message}\n`);
  return result;
}

// Create env with TMPDIR pointing to a temp dir inside configDir.
// Workaround for Claude Code plugin install EXDEV bug: it tries to rename
// from ~/.claude/plugins/cache/ to /tmp/ which fails when /tmp is on a
// different filesystem (common on Linux). Setting TMPDIR to a directory
// on the same filesystem as ~/.claude/ avoids the cross-device link error.
function sameFilesystemTmpEnv(configDir) {
  const tmpDir = path.join(configDir, 'tmp');
  try { fs.mkdirSync(tmpDir, { recursive: true }); } catch (_) {}
  return Object.assign({}, process.env, {
    TMPDIR: tmpDir,  // Unix
    TEMP: tmpDir,    // Windows
    TMP: tmpDir,     // Windows alternate
  });
}

function captureSpawn(cmd, args, opts) {
  try { return spawnXplat(cmd, args, Object.assign({ encoding: 'utf8' }, opts)); }
  catch (_) { return { status: 1, stdout: '', stderr: '' }; }
}

// spawnSync reports a missing binary as { status: null, error }, so the old
// `(r.status || 0) === 0` checks read ENOENT as success — a machine without
// the `claude` CLI got "installed: claude" with nothing installed and the
// standalone-hook fallback skipped (issue #592). Every spawn result must pass
// through here before being treated as "it worked".
function spawnOk(r) {
  return !!r && !r.error && r.status === 0;
}

// The absolute node path baked into settings.json at install time. Preferring
// the PATH entry over process.execPath is what survives a `brew upgrade node`
// (#805): Homebrew runs the installer as the versioned Cellar binary
// (/opt/homebrew/Cellar/node/26.5.0/bin/node), which stops existing on the next
// upgrade, while /opt/homebrew/bin/node is the stable symlink that follows it.
//
// The candidate has to be EXERCISED, not just located. `command -v node` will
// happily name a stale version-manager shim, a dangling symlink, or a shell
// function, and a path that is merely on PATH but does not run is strictly
// worse than what it replaces — process.execPath is by construction a working
// node, since it is the one executing this line. So a candidate is only
// preferred once it has actually reported a version; anything else falls back.
// Same reason the result is not symlink-resolved: the stable symlink IS the
// wanted answer, and realpath would walk it straight back to the Cellar path.
function absoluteNodePath() {
  if (!IS_WIN) {
    try {
      const r = child_process.spawnSync('/bin/sh', ['-c', 'command -v node'], { encoding: 'utf8' });
      const candidate = (r.stdout || '').trim().split(/\r?\n/, 1)[0];
      if (r.status === 0 && candidate && path.isAbsolute(candidate)) {
        const resolved = path.resolve(candidate);
        const probe = child_process.spawnSync(resolved, ['--version'], { encoding: 'utf8' });
        const major = /^v(\d+)\./.exec((probe.stdout || '').trim());
        // Running is necessary but not sufficient: a PATH node below the
        // package's `engines` floor would be persisted into settings.json and
        // run every hook on an unsupported runtime. process.execPath already
        // satisfies the floor, since it is running this installer.
        if (spawnOk(probe) && major && Number(major[1]) >= MIN_NODE_MAJOR) return resolved;
      }
    } catch (_) {}
  }
  return process.execPath;
}

// writeSettings re-serializes plain JSON, so comments and trailing commas go.
// When the file had them, keep the commented original once and say where.
function writeSettingsKeepingComments(settingsPath, settings, meta, warn) {
  if (meta.jsonc) {
    const bak = settingsPath + '.bak';
    if (!fs.existsSync(bak)) fs.copyFileSync(settingsPath, bak);
    warn(`  note: ${settingsPath} contains comments — rewriting it drops them.`);
    warn(`        Your comments are kept in ${bak} (that copy may still list caveman's hooks).`);
  }
  SETTINGS.validateHookFields(settings);
  SETTINGS.writeSettings(settingsPath, settings);
}

// ── Per-provider installers ────────────────────────────────────────────────
// `claude plugin list` names each plugin by its full id. A bare /caveman/ also
// matched caveman-browse@caveman-browse, so the install was skipped.
function claudeHasCaveman() {
  const r = captureSpawn('claude', ['plugin', 'list']);
  const out = (r.stdout || '').replace(/\x1b\[[0-9;]*m/g, '');
  return r.status === 0 && /(^|\s)caveman@caveman(\s|$)/m.test(out);
}

// `plugin list` also names a turned-off plugin, which runs no hooks. Only
// `--json` saying it is on at user scope counts; a claude without it says no.
function claudeCavemanEnabled() {
  const r = captureSpawn('claude', ['plugin', 'list', '--json']);
  try {
    return spawnOk(r) && JSON.parse(r.stdout).some((p) => p && p.id === 'caveman@caveman' && p.scope === 'user' && p.enabled === true);
  } catch (_) { return false; }
}

async function installClaude(ctx) {
  const { say, note, warn, ok, opts, results, configDir } = ctx;
  results.detected++;
  say('→ Claude Code detected');

  // Plugin install (idempotent unless --force)
  let alreadyInstalled = false;
  if (!opts.force) alreadyInstalled = claudeHasCaveman();
  let pluginInstallSucceeded = false;
  if (alreadyInstalled) {
    note('  caveman plugin already installed (use --force to reinstall)');
    results.skipped.push(['claude', 'plugin already installed']);
    pluginInstallSucceeded = true;
  } else {
    // Use a temp dir on the same filesystem as configDir to avoid EXDEV errors
    // when Claude Code's plugin installer tries to rename across filesystems (#585).
    // A dry run makes none; a dir this run made goes again once it is empty.
    const tmpExisted = fs.existsSync(path.join(configDir, 'tmp'));
    const pluginEnv = opts.dryRun ? null : sameFilesystemTmpEnv(configDir);
    const r1 = runSpawn('claude', ['plugin', 'marketplace', 'add', REPO], { env: pluginEnv }, opts.dryRun);
    const r2 = runSpawn('claude', ['plugin', 'install', 'caveman@caveman'], { env: pluginEnv }, opts.dryRun);
    if (pluginEnv && !tmpExisted) { try { fs.rmdirSync(pluginEnv.TMPDIR); } catch (_) {} }
    if (spawnOk(r1) && spawnOk(r2)) {
      results.installed.push('claude');
      pluginInstallSucceeded = true;
    } else {
      if (r1.error || r2.error) {
        warn('  claude CLI not found on PATH (or could not be spawned)');
      }
      results.failed.push(['claude', 'claude plugin install failed']);
    }
  }

  // Self-heal: drop managed settings.json hook/statusLine entries whose target
  // script no longer exists (issue #471). Migrating an old manual install to
  // the plugin leaves settings.json pointing at removed ~/.claude/hooks/
  // caveman-*.js scripts, so Claude Code crashes every SessionStart /
  // UserPromptSubmit with `loader:1478 — Cannot find module …`. Runs
  // unconditionally so it repairs an already-dirty config even when we then
  // skip standalone wiring because the plugin manifest handles hooks.
  {
    const settingsPath = path.join(configDir, 'settings.json');
    const settings = SETTINGS.readSettings(settingsPath);
    if (settings) {
      const pruned = SETTINGS.pruneOrphanedManagedHooks(settings, configDir);
      if (pruned > 0) {
        note(`  removed ${pruned} orphaned caveman hook entr${pruned === 1 ? 'y' : 'ies'} from settings.json (target script missing)`);
        if (!opts.dryRun) {
          SETTINGS.validateHookFields(settings);
          SETTINGS.writeSettings(settingsPath, settings);
        }
      }
    }
  }

  // Hook wiring decision matrix (issue #392 — avoid double-firing):
  //   --no-hooks       → skip
  //   --with-hooks     → wire (warn if the plugin manifest also wires them)
  //   default / --all  → wire only if the plugin install did NOT succeed.
  // The plugin manifest already wires SessionStart + UserPromptSubmit +
  // SessionEnd when the plugin install succeeds; wiring them again in
  // settings.json fires both per event (two CAVEMAN MODE blocks, two
  // reinforcement lines).
  let shouldWireHooks;
  if (opts.withHooks === false) {
    shouldWireHooks = false;
  } else if (opts.withHooks === true) {
    shouldWireHooks = true;
    if (pluginInstallSucceeded) {
      warn('  --with-hooks wires hooks in settings.json alongside the plugin manifest.');
      warn('  Both will fire on every event. Pass --no-hooks to keep only the plugin path.');
    }
  } else {
    // 'auto'
    shouldWireHooks = !pluginInstallSucceeded;
    if (!shouldWireHooks) {
      note('  hooks: plugin manifest handles SessionStart + SubagentStart + UserPromptSubmit + SessionEnd');
      note('  (pass --with-hooks to also wire standalone hooks in settings.json)');
      results.skipped.push(['claude-hooks', 'plugin manifest handles hooks']);
      // An earlier run whose plugin install failed wired standalone hooks. The
      // plugin runs the same scripts, so keeping them fires each hook twice.
      // The statusline stays: the plugin has none. `claude` reads the profile
      // in CLAUDE_CONFIG_DIR, not --config-dir: another profile keeps its hooks.
      const settingsPath = path.join(configDir, 'settings.json');
      const settingsMeta = {};
      const pluginProfile = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
      const settings = path.resolve(configDir) === path.resolve(pluginProfile)
        ? SETTINGS.readSettings(settingsPath, settingsMeta) : null;
      const removed = settings ? SETTINGS.removeCavemanHooks(settings) : 0;
      const what = `${removed} standalone caveman hook entr${removed === 1 ? 'y' : 'ies'} from settings.json; the plugin runs them now`;
      try {
        // A plugin that was already there may be turned off: then these hooks
        // are the only caveman left. One just installed is on.
        if (removed > 0 && alreadyInstalled && !claudeCavemanEnabled()) {
          note(`  kept ${removed} standalone caveman hook entr${removed === 1 ? 'y' : 'ies'} in settings.json: Claude Code does not report the caveman plugin as turned on`);
        } else if (removed > 0 && opts.dryRun) note(`  would remove ${what}`);
        else if (removed > 0) {
          writeSettingsKeepingComments(settingsPath, settings, settingsMeta, warn);
          note(`  removed ${what}`);
        }
      } catch (e) {
        warn(`  could not update ${settingsPath}: ${e && e.message || e}`);
      }
    } else {
      note('  hooks: plugin install did not succeed; falling back to standalone wiring');
    }
  }

  if (shouldWireHooks) {
    say('  → installing hooks');
    // A throw (an event shape addCommandHook will not overwrite, a failed
    // write) is a failed agent, not the end of the run. settings.json is
    // written last and atomically, so it is unchanged.
    let r;
    try { r = await installHooks(ctx); }
    catch (e) { r = `${e && e.message || e}; settings.json left untouched`; }
    if (r === 'ok') results.installed.push('claude-hooks');
    else if (r === 'skip') results.skipped.push(['claude-hooks', 'already wired']);
    else results.failed.push(['claude-hooks', r]);
  }

  if (opts.withMcpShrink) {
    say('  → wiring caveman-shrink MCP proxy (--with-mcp-shrink)');
    const r = installMcpShrink(ctx);
    if (r.kind === 'ok')   results.installed.push('caveman-shrink');
    if (r.kind === 'skip') results.skipped.push(['caveman-shrink', r.why]);
    if (r.kind === 'fail') results.failed.push(['caveman-shrink', r.why]);
  }

  process.stdout.write('\n');
}

// Gemini CLI writes `extensions list` to stderr, and 0.40 writes nothing
// without --debug, so a stdout probe never saw caveman. Every version installs
// to <GEMINI_CLI_HOME or ~>/.gemini/extensions/<name>; a linked extension
// holds only the install record.
function geminiHasCaveman() {
  const dir = path.join(process.env.GEMINI_CLI_HOME || os.homedir(), '.gemini', 'extensions', 'caveman');
  return ['gemini-extension.json', '.gemini-extension-install.json'].some((f) => fs.existsSync(path.join(dir, f)));
}

// `gemini extensions disable` records a `!<path>` override here. Uninstalling
// drops the record, so a reinstall would turn caveman back on.
function geminiCavemanTurnedOff() {
  const file = path.join(process.env.GEMINI_CLI_HOME || os.homedir(), '.gemini', 'extensions', 'extension-enablement.json');
  try {
    const entry = JSON.parse(fs.readFileSync(file, 'utf8')).caveman;
    return !!entry && Array.isArray(entry.overrides) && entry.overrides.some((o) => String(o).startsWith('!'));
  } catch (_) { return false; }
}

function installGemini(ctx) {
  const { say, note, warn, opts, results } = ctx;
  results.detected++;
  say('→ Gemini CLI detected');

  const installed = geminiHasCaveman();
  if (installed && !opts.force) {
    note('  caveman extension already installed (use --force to reinstall)');
    results.skipped.push(['gemini', 'extension already installed']);
    process.stdout.write('\n');
    return;
  }
  if (installed && geminiCavemanTurnedOff()) {
    note('  caveman extension is turned off in Gemini CLI. Reinstalling would turn it back on, so it is left as is.');
    note('  To reinstall anyway: gemini extensions uninstall caveman, then run this installer again.');
    results.skipped.push(['gemini', 'extension turned off in Gemini CLI']);
    process.stdout.write('\n');
    return;
  }
  // Gemini CLI refuses `extensions install` for an installed extension
  // ("Please uninstall it first"), and `extensions update` leaves one already at
  // the latest release alone. So --force reinstalls: uninstall, then install.
  const uninstallFirst = (spawnOpts) => {
    if (!installed || spawnOk(runSpawn('gemini', ['extensions', 'uninstall', 'caveman'], spawnOpts, opts.dryRun))) return true;
    results.failed.push(['gemini', 'gemini extensions uninstall failed']);
    return false;
  };
  // Under `curl | bash`, stdin is the script, not a terminal. Gemini CLI reads
  // its workspace-trust and extension-consent answers from stdin, so the
  // install never ends. See issues #400 and #676. Two parts remove the two
  // prompts.
  //   GEMINI_CLI_TRUST_WORKSPACE=true trusts cwd for this process only. It is
  //     what `--skip-trust` sets. That flag belongs to the root chat command
  //     and never reaches the `extensions` subcommand.
  //   --consent answers the extension-install warning. Do not pass it without
  //     the variable. Alone, it also answers the trust prompt with yes, and
  //     that writes cwd into trustedFolders.json permanently.
  // Gemini CLI v0.41.0 adds `--skip-trust` and GEMINI_CLI_TRUST_WORKSPACE in
  // one commit, upstream PR #25814. An older CLI ignores the variable. So the
  // installer probes the root help one time. `--skip-trust` in the text shows
  // that this CLI reads the variable. Without the flag, the installer runs the
  // same command as before this change: the caller directory, no variable, and
  // no `--consent`. On an older CLI, `--consent` answers the trust prompt with
  // yes, and that trusts the caller directory permanently.
  // Gemini CLI v0.11.0 adds `--consent`, which is older than `--skip-trust`.
  // So a CLI with `--skip-trust` also has `--consent`. This is an assumption
  // about the upstream release order.
  const help = captureSpawn('gemini', ['--help']);
  const sessionTrust = help.status === 0
    && /--skip-trust\b/.test(`${help.stdout || ''}${help.stderr || ''}`);
  const url = `https://github.com/${REPO}`;
  let r;
  if (!sessionTrust) {
    const tail = "Installing from the current directory with the CLI's own trust and consent prompts.";
    if (help.status === 0) {
      note(`  this Gemini CLI has no --skip-trust (added in v0.41.0). ${tail}`);
    } else {
      const code = help.error ? (help.error.code || 'spawn error')
        : (help.status === null ? 'spawn error' : help.status);
      note(`  could not read \`gemini --help\` (exit ${code}). ${tail}`);
    }
    if (uninstallFirst(null)) r = runSpawn('gemini', ['extensions', 'install', url], null, opts.dryRun);
  } else {
    // A trusted cwd lets Gemini CLI load .gemini/ config and .env files. Gemini
    // CLI also searches every parent directory up to the root for those files.
    // So run the install from a new empty scratch directory below the user's
    // own ~/.caveman/tmp. Every parent then belongs to the user or to root, and
    // trust covers nothing. A directory below /tmp is not safe, because another
    // local user can plant /tmp/.env. A directory below ~/.gemini/tmp is not
    // safe, because Gemini CLI keeps per-project state there under the cwd
    // name. The scratch directory comes from mkdtemp, so this run owns it and
    // removes only it. Never remove a fixed path, because it can hold data from
    // an earlier process. If the directory cannot be made, report a failure and
    // do not start Gemini CLI. A GitHub source does not use cwd.
    const env = Object.assign({}, process.env, { GEMINI_CLI_TRUST_WORKSPACE: 'true' });
    const scratchParent = path.join(os.homedir(), '.caveman', 'tmp');
    note(`  env: GEMINI_CLI_TRUST_WORKSPACE=true. Trust applies to this process only, in a new empty scratch directory below ${scratchParent}. trustedFolders.json stays unchanged.`);
    let cwd;
    if (!opts.dryRun) {
      try {
        fs.mkdirSync(scratchParent, { recursive: true });
        cwd = fs.mkdtempSync(path.join(scratchParent, 'gemini-install-'));
      } catch (e) {
        results.failed.push(['gemini', `could not create scratch directory below ${scratchParent}: ${e.message}`]);
        process.stdout.write('\n');
        return;
      }
    }
    try {
      if (uninstallFirst({ env, cwd })) r = runSpawn('gemini', ['extensions', 'install', url, '--consent'], { env, cwd }, opts.dryRun);
    } finally {
      if (cwd) { try { fs.rmSync(cwd, { recursive: true, force: true }); } catch (_) {} }
    }
  }
  if (spawnOk(r)) results.installed.push('gemini');
  else if (r) {
    results.failed.push(['gemini', 'gemini extensions install failed']);
    if (installed) warn(`  the old caveman extension is already removed. Install it again with: gemini extensions install ${url}`);
  }
  process.stdout.write('\n');
}

function installViaSkills(ctx, prov) {
  const { say, note, opts, results } = ctx;
  results.detected++;
  say(`→ ${prov.label} ${prov.soft ? 'selected' : 'detected'}`);
  if (PROVIDER_SKILLS.usesNativeSkills(prov.id)) {
    try {
      const installed = PROVIDER_SKILLS.install({
        provider: prov.id,
        repoRoot: ctx.repoRoot,
        force: opts.force,
        dryRun: opts.dryRun,
        note,
        ref: PINNED_REF,
        run: (command, args, options) => runSpawn(command, args, options, false),
      });
      if (!opts.dryRun) note(`  copied ${installed.count} skills into ${installed.root}`);
      if (prov.id === 'aider-desk') note('  Enable Skills Tools for the AiderDesk agent profile to use these skills.');
      if (prov.id === 'grok') installGrokAgentsBlock(ctx);
      results.installed.push(prov.id);
    } catch (error) {
      ctx.warn(`  ${prov.label} skill installation failed: ${error.message}`);
      results.failed.push([prov.id, error.message]);
    }
    process.stdout.write('\n');
    return;
  }
  // --skill '*' --yes: skip the upstream skill-selection TUI and confirmation
  // prompts. Without --skill, `curl|bash` (no TTY on stdin) renders an empty
  // checkbox list the user can't interact with, then exits 0 with zero skills
  // installed — and our installer happily reports success. See issue #370.
  //
  // We pass `--skill '*'` rather than `--all` because the upstream `skills` CLI
  // interprets `--all` as "all skills from the source to *all* agents", which
  // ignores the `-a prov.profile` selection and writes every skill through
  // every agent adapter (see issue #389). `--skill '*' -a <agent>` is the
  // documented form for "install every skill into a specific agent".
  // Use the vendor's supported scope. Replit reads project-local skills; its
  // workspace-wide library is managed in the UI, not a home-directory scan.
  // Other adapters resolve their user directories and supported home overrides.
  // `#ref` pins skill content to this release, like the hook downloads.
  const args = ['-y', 'skills', 'add', `${REPO}#${PINNED_REF}`, '--skill', '*', '-a', prov.profile, '--yes'];
  if (prov.skillsScope === 'project') note(`  Installing into this project: ${process.cwd()}`);
  else args.push('-g');
  const r = runSpawn('npx', args, null, opts.dryRun);
  if (spawnOk(r)) {
    results.installed.push(prov.id);
    if (prov.id === 'codex' && opts.withHooks !== false) installCodexHook(ctx);
    if (prov.id === 'cursor') installCursorNative(ctx);
    if (prov.id === 'copilot') installCopilotCliHook(ctx);
  } else results.failed.push([prov.id, `npx skills add (${prov.profile}) failed`]);
  process.stdout.write('\n');
}

// ── Codex always-on SessionStart hook (#573) ───────────────────────────────
// Codex runs user hooks from $CODEX_HOME/hooks.json (the `hooks` feature is
// stable and on by default; Codex asks the user to trust a new hook once via
// /hooks — never pre-trust it). The owned payload mirrors the layout the
// hook's path math expects: hooks/codex-sessionstart.js loads caveman-config.js
// beside it and reads ../skills/<mode>/SKILL.md. Our hooks.json entry is the
// one whose command runs our script; foreign hooks and the caveman CLI's
// native-hook entries are never touched. --no-hooks / --minimal skip it.
const CODEX_PAYLOAD_DIR = 'caveman';
const CODEX_HOOK_SKILLS = ['caveman', 'ultracave', 'megacave'];

function codexHome() {
  return process.env.CODEX_HOME || path.join(os.homedir(), '.codex');
}

function codexHookScript(home) {
  return path.join(home, CODEX_PAYLOAD_DIR, 'hooks', 'codex-sessionstart.js');
}

// Drop every SessionStart handler that runs our script, and any group it
// leaves empty. Returns the index of the first group that held one, or -1.
// Match the quoted path exactly as installCodexHook writes it: hookCommand
// escapes $ " ` \ and turns Windows separators into /.
function stripCodexHook(doc, script) {
  const list = doc.hooks && doc.hooks.SessionStart;
  if (!Array.isArray(list)) return -1;
  const needle = PLATFORM_PATHS.hookCommand(script, []);
  const ours = (h) => h && typeof h.command === 'string' && h.command.includes(needle);
  const at = list.findIndex((e) => e && Array.isArray(e.hooks) && e.hooks.some(ours));
  if (at === -1) return -1;
  doc.hooks.SessionStart = list.filter((e) => {
    if (!e || !Array.isArray(e.hooks) || !e.hooks.some(ours)) return true;
    e.hooks = e.hooks.filter((h) => !ours(h));
    return e.hooks.length > 0;
  });
  if (doc.hooks.SessionStart.length === 0) delete doc.hooks.SessionStart;
  if (Object.keys(doc.hooks).length === 0) delete doc.hooks;
  return at;
}

function codexHooksDocOk(doc) {
  if (!doc || typeof doc !== 'object' || Array.isArray(doc)) return false;
  if (doc.hooks === undefined) return true;
  if (!doc.hooks || typeof doc.hooks !== 'object' || Array.isArray(doc.hooks)) return false;
  return doc.hooks.SessionStart === undefined || Array.isArray(doc.hooks.SessionStart);
}

function installCodexHook(ctx) {
  const { note, warn, opts, repoRoot, results } = ctx;
  const home = codexHome();
  const hooksPath = path.join(home, 'hooks.json');
  const script = codexHookScript(home);
  if (!repoRoot) {
    note(`  skipped Codex always-on hook: needs the full caveman package (npx -y github:${REPO} -- --only codex)`);
    results.skipped.push(['codex-hooks', 'needs the full caveman package']);
    return;
  }
  if (opts.dryRun) {
    note(`  would install the owned caveman hook payload under ${path.join(home, CODEX_PAYLOAD_DIR)}`);
    note(`  would merge a caveman SessionStart entry into ${hooksPath}`);
    return;
  }
  // Read before writing anything: an unreadable hooks.json leaves no orphan payload.
  const doc = SETTINGS.readSettings(hooksPath);
  if (!codexHooksDocOk(doc)) {
    warn(`  ${hooksPath} is unparseable or has an unexpected shape; left untouched.`);
    results.failed.push(['codex-hooks', `${hooksPath} unparseable; nothing changed`]);
    return;
  }
  try {
    OWNED.installOwned({
      root: home, integration: 'codex-hooks', force: opts.force, note,
      operations: [{
        relativePath: CODEX_PAYLOAD_DIR,
        write: (stage) => {
          OWNED.copyPath(path.join(repoRoot, '.codex', 'codex-sessionstart.js'), path.join(stage, 'hooks', 'codex-sessionstart.js'));
          for (const f of ['caveman-config.js', 'package.json']) {
            OWNED.copyPath(path.join(repoRoot, 'src', 'hooks', f), path.join(stage, 'hooks', f));
          }
          for (const id of CODEX_HOOK_SKILLS) {
            OWNED.copyPath(path.join(repoRoot, 'skills', id, 'SKILL.md'), path.join(stage, 'skills', id, 'SKILL.md'));
          }
        },
      }],
    });
    // Replace in place, so a re-run leaves an unchanged entry byte-identical
    // (Codex keys hook trust to the definition).
    const before = JSON.stringify(doc);
    const at = stripCodexHook(doc, script);
    if (!doc.hooks) doc.hooks = {};
    const list = doc.hooks.SessionStart || [];
    list.splice(at === -1 ? list.length : Math.min(at, list.length), 0, {
      // compact: Codex 0.160 sends it, and compaction prunes the injected ruleset.
      matcher: 'startup|resume|clear|compact',
      hooks: [{ type: 'command', command: 'node ' + PLATFORM_PATHS.hookCommand(script, []), timeout: 5, statusMessage: 'Loading caveman mode' }],
    });
    doc.hooks.SessionStart = list;
    if (JSON.stringify(doc) === before) {
      note(`  ${hooksPath} already has the caveman SessionStart hook`);
    } else {
      SETTINGS.validateHookFields(doc);
      SETTINGS.writeSettings(hooksPath, doc);
      note(`  merged caveman SessionStart hook into ${hooksPath}`);
    }
    note('  first Codex launch: run /hooks and trust the caveman hook so it can run');
    results.installed.push('codex-hooks');
  } catch (error) {
    warn(`  Codex hook install failed: ${error.message}`);
    results.failed.push(['codex-hooks', error.message]);
  }
}

// ── Grok Build always-on (#754) ────────────────────────────────────────────
// Grok Build loads global rules from $GROK_HOME/AGENTS.md (default ~/.grok).
// A marker-fenced ruleset block there makes caveman always-on. The append/strip
// helpers are OpenClaw's SOUL.md ones: generic marker fence, atomic,
// symlink-refusing, tolerant of damaged markers.
function grokAgentsMdPath() {
  return path.join(path.dirname(PROVIDER_SKILLS.skillsRoot('grok')), 'AGENTS.md');
}

function installGrokAgentsBlock(ctx) {
  const { note, warn, opts, repoRoot, results } = ctx;
  const target = grokAgentsMdPath();
  if (!repoRoot) {
    note(`  skipped always-on block in ${target}: needs the full caveman package (npx -y github:${REPO} -- --only grok)`);
    return;
  }
  if (opts.dryRun) {
    note(`  would add the caveman ruleset block to ${target}`);
    return;
  }
  try {
    const rule = fs.readFileSync(path.join(repoRoot, 'src', 'rules', 'caveman-activate.md'), 'utf8').trimEnd();
    const r = OPENCLAW.appendBootstrapToSoul(target, `${OPENCLAW.MARK_BEGIN}\n${rule}\n${OPENCLAW.MARK_END}\n`);
    note(r.changed ? `  ${r.refreshed ? 'refreshed' : 'wrote'} caveman ruleset block in ${target}` : `  ${target} already has the current caveman ruleset`);
  } catch (error) {
    // Skills are installed either way; a symlinked or unreadable AGENTS.md only
    // costs the always-on block, and is never written through.
    warn(`  could not add the caveman ruleset block to ${target}: ${error.message}`);
    results.failed.push(['grok-always-on', `${target} left untouched`]);
  }
}


// Cursor extras beyond the upstream skill profile: cavecrew subagents and a
// user sessionStart hook (installer/lib/cursor-native.js). `--no-hooks` keeps the
// agents only.
function installCursorNative(ctx) {
  const { note, warn, opts, results, repoRoot } = ctx;
  if (!repoRoot) {
    note('  Cursor agents and hook need the caveman package files; skipped.');
    return;
  }
  try {
    CURSOR_NATIVE.installCursorNative({
      repoRoot,
      node: absoluteNodePath(),
      withHooks: opts.withHooks !== false,
      force: opts.force,
      dryRun: opts.dryRun,
      note,
    });
  } catch (error) {
    warn(`  Cursor agents/hook were not installed: ${error.message}`);
    results.failed.push(['cursor (agents + hook)', error.message]);
  }
}

// GitHub Copilot CLI always-on. User hooks load from $COPILOT_HOME/hooks/*.json
// (default ~/.copilot/hooks/) and sessionStart consumes a top-level
// additionalContext. Both files are owned whole, so nothing is merged.
// https://docs.github.com/en/copilot/reference/hooks-configuration
const COPILOT_HOOK_FILE = 'hooks/caveman.json';

function copilotHome() {
  return process.env.COPILOT_HOME || path.join(os.homedir(), '.copilot');
}

function installCopilotCliHook(ctx) {
  const { note, warn, opts, results, repoRoot } = ctx;
  const root = copilotHome();
  // No bare ~/.copilot probe: `npx skills add -a github-copilot -g` has just
  // created ~/.copilot/skills, and VS Code uses that folder too.
  if (opts.withHooks === false || !(hasCmd('copilot') || process.env.COPILOT_HOME)) return;
  if (!repoRoot) {
    note('  Copilot CLI hook needs the caveman package files; skipped.');
    return;
  }
  const node = absoluteNodePath();
  const hook = {
    version: 1,
    hooks: {
      sessionStart: [{
        type: 'command',
        bash: HOST_HOOKS.hookCommand(root, 'copilot', node, 'posix'),
        powershell: HOST_HOOKS.hookCommand(root, 'copilot', node, 'win32'),
        timeoutSec: 10,
      }],
    },
  };
  if (opts.dryRun) {
    note(`  would install the Copilot CLI sessionStart hook at ${path.join(root, COPILOT_HOOK_FILE)}`);
    return;
  }
  try {
    OWNED.installOwned({
      root, integration: 'copilot-cli', force: opts.force, note,
      operations: [
        HOST_HOOKS.payloadOperation(repoRoot),
        { relativePath: COPILOT_HOOK_FILE, write: (stage) => fs.writeFileSync(stage, JSON.stringify(hook, null, 2) + '\n') },
      ],
    });
    note('  Copilot CLI: new sessions start in caveman mode');
  } catch (error) {
    warn(`  Copilot CLI hook was not installed: ${error.message}`);
    results.failed.push(['copilot (CLI hook)', error.message]);
  }
}

// ── hermes native install ──────────────────────────────────────────────────
// Drops the caveman skills into ~/.hermes/skills/productivity/ (or HERMES_HOME if set).
const HERMES_SKILL_DIRS = ['caveman', 'ultracave', 'megacave', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];

function hermesConfigDir() {
  // Hermes uses ~/.hermes by default, or HERMES_HOME env var. Hermes 0.21.5 reads
  // the override as Path(expanduser(expandvars(value.strip()))); same as hermesHome()
  // in packages/cli. ponytail: no `~user` or Windows quote/escape forms.
  const override = (process.env.HERMES_HOME || '').trim();
  if (!override) return path.join(os.homedir(), '.hermes', 'skills');
  const lookup = (whole, name) => (process.env[name] !== undefined ? process.env[name] : whole);
  let home = override.replace(/\$(\w+)|\$\{([^{}$]*)\}/g, (whole, bare, braced) => lookup(whole, bare !== undefined ? bare : braced));
  if (process.platform === 'win32') home = home.replace(/%([^%]+)%/g, lookup);
  return path.join(home.replace(/^~(?=$|[\\/])/, os.homedir()), 'skills');
}

function installHermes(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ Hermes Agent detected');

  if (!repoRoot) {
    warn('  Hermes native install requires a local clone of the caveman repo.');
    note('  Re-run from a clone: git clone https://github.com/' + REPO + ' && cd caveman && node installer/install.js --only hermes');
    results.failed.push(['hermes', 'native install requires local repo clone']);
    process.stdout.write('\n');
    return;
  }

  const skillsRoot = path.join(hermesConfigDir(), 'productivity');

  if (opts.dryRun) {
    note(`  would mkdir ${skillsRoot}/`);
    note(`  would copy ${HERMES_SKILL_DIRS.length} skill dirs into ${skillsRoot}/`);
    results.installed.push('hermes');
    process.stdout.write('\n');
    return;
  }

  try {
    const operations = [];
    for (const skillDir of HERMES_SKILL_DIRS) {
      const srcDir = path.join(repoRoot, 'skills', skillDir);
      if (!fs.existsSync(srcDir)) {
        warn(`  skill dir not found: ${srcDir}`);
        continue;
      }
      operations.push({
        relativePath: skillDir,
        write: (stage) => OWNED.copyPath(srcDir, stage),
      });
    }
    OWNED.installOwned({
      root: skillsRoot,
      integration: 'hermes',
      operations,
      force: opts.force,
      note,
    });

    results.installed.push('hermes');
  } catch (err) {
    results.failed.push(['hermes', 'copy failed: ' + err.message]);
  }

  process.stdout.write('\n');
}

// ── Antigravity CLI (agy) plugin ───────────────────────────────────────────
// `agy plugin install <dir>` copies a plugin into agy's own plugin root, so the
// staging copy is temporary and agy owns the lifecycle. Always-on comes from
// the plugin's rules/AGENTS.md, which agy merges into the active rule set while
// the plugin is enabled; agy has no session-start hook (PreInvocation runs
// before every model call). Checked live with agy 1.2.17: caveman voice with
// the rule, normal prose without it or with the plugin disabled.
const AGY_PLUGIN_NAME = 'caveman';
const AGY_SKILL_DIRS = HERMES_SKILL_DIRS;

function installAntigravityCli(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ Antigravity CLI detected');
  if (!repoRoot) {
    warn('  Antigravity CLI install needs the caveman package files.');
    results.failed.push(['antigravity-cli', 'native install requires local repo clone']);
    process.stdout.write('\n');
    return;
  }
  if (opts.dryRun) {
    note(`  would stage a ${AGY_PLUGIN_NAME} plugin (${AGY_SKILL_DIRS.length} skills + rules/AGENTS.md)`);
    runSpawn('agy', ['plugin', 'install', `<staging>/${AGY_PLUGIN_NAME}`], null, true);
    results.installed.push('antigravity-cli');
    process.stdout.write('\n');
    return;
  }
  const staging = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-agy-'));
  try {
    const pluginDir = path.join(staging, AGY_PLUGIN_NAME);
    fs.mkdirSync(path.join(pluginDir, 'rules'), { recursive: true });
    fs.writeFileSync(path.join(pluginDir, 'plugin.json'), JSON.stringify({
      name: AGY_PLUGIN_NAME,
      description: 'Caveman: terse replies, every technical fact kept',
    }, null, 2) + '\n');
    fs.copyFileSync(path.join(repoRoot, 'src', 'rules', 'caveman-activate.md'), path.join(pluginDir, 'rules', 'AGENTS.md'));
    for (const name of AGY_SKILL_DIRS) {
      fs.cpSync(path.join(repoRoot, 'skills', name), path.join(pluginDir, 'skills', name), { recursive: true });
    }
    if (spawnOk(runSpawn('agy', ['plugin', 'install', pluginDir], null, false))) {
      results.installed.push('antigravity-cli');
      note('  new agy sessions start in caveman mode; `agy plugin disable caveman` turns it off');
    } else {
      results.failed.push(['antigravity-cli', 'agy plugin install failed']);
    }
  } catch (error) {
    warn(`  Antigravity CLI install failed: ${error.message}`);
    results.failed.push(['antigravity-cli', error.message]);
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
  process.stdout.write('\n');
}

// ── opencode native install ───────────────────────────────────────────────
// Drops the in-repo plugin (src/plugins/opencode/) plus skills, agents,
// commands, and an AGENTS.md ruleset into ~/.config/opencode/. Patches
// opencode.json with a "plugin" array entry. Mirrors the Claude Code hook
// architecture as closely as opencode allows — only the statusline is missing
// (opencode's TUI exposes no plugin-writable badge).
const OPENCODE_SKILL_DIRS  = ['caveman', 'ultracave', 'megacave', 'caveman-commit', 'caveman-review', 'caveman-help', 'caveman-stats', 'caveman-compress', 'cavecrew'];
const OPENCODE_AGENT_FILES = ['cavecrew-investigator.md', 'cavecrew-builder.md', 'cavecrew-reviewer.md'];
const OPENCODE_COMMAND_FILES = ['caveman.md', 'ultracave.md', 'megacave.md', 'caveman-commit.md', 'caveman-review.md', 'caveman-compress.md', 'caveman-stats.md', 'caveman-help.md'];
const OPENCODE_PLUGIN_REL = './plugins/caveman/plugin.js';
const OPENCODE_AGENTS_MD_SENTINEL = 'Respond terse like smart caveman';
// Marker fence for the opencode AGENTS.md ruleset block. Same convention as
// installer/lib/openclaw.js for SOUL.md — lets us strip our block cleanly even when
// the user has authored content above AND below it.
const OPENCODE_AGENTS_MD_BEGIN = '<!-- caveman-begin -->';
const OPENCODE_AGENTS_MD_END = '<!-- caveman-end -->';

function countOccurrences(haystack, needle) {
  let n = 0;
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + needle.length)) n++;
  return n;
}

// ── Oh My Pi native install ────────────────────────────────────────────────
// OMP local-path installs intentionally route through its plugin manager:
// `omp plugin install <path>` symlinks the package into ~/.omp/plugins/
// node_modules and records runtime state in omp-plugins.lock.json. We prepare
// a stable managed plugin package under ~/.omp/ so the symlink never points at
// an npx cache directory that may disappear after install.
const OMP_PLUGIN_NAME = 'caveman';
const OMP_PLUGIN_DIRNAME = 'caveman-plugin';
// OMP transpiles .js as ESM; preserve .cjs so module.exports loads as a factory.
const OMP_EXTENSION_ENTRY = './index.cjs';
const OMP_SKILL_DIRS = OPENCODE_SKILL_DIRS;
const OMP_AGENT_FILES = OPENCODE_AGENT_FILES;
const OMP_COMMAND_FILES = OPENCODE_COMMAND_FILES;
const OMP_RULE_FILE = 'caveman.md';
const OMP_PACKAGE_FILE = 'package.json';
const OMP_INDEX_FILE = 'index.cjs';
const OMP_PACKAGE_VERSION = '0.1.0';
const OMP_PLUGIN_DESCRIPTION = 'Caveman terse communication mode for Oh My Pi';
const OMP_RULE_COUNT = 1;
// OMP has no hook files for plugins; persistence (issue #743 — caveman must
// stay active beyond the first prompt) comes from re-appending the ruleset to
// the system prompt on every before_agent_start, mirroring packages/pi-extension.
// Rule text is embedded at install time: no runtime fs reads, no second source.
function ompExtensionSource(ruleBodyText) {
  const ruleLiteral = JSON.stringify(ruleBodyText);
  return `'use strict';

const STATUS_LABEL = 'caveman';
const STATUS_TEXT = 'CAVEMAN';
const RULE = ${ruleLiteral};

module.exports = function cavemanPlugin(pi) {
  if (!pi || typeof pi.on !== 'function') return;
  pi.on('session_start', async (_event, ctx) => {
    if (ctx && ctx.ui && typeof ctx.ui.setStatus === 'function') {
      ctx.ui.setStatus(STATUS_LABEL, STATUS_TEXT);
    }
  });
  pi.on('before_agent_start', async (event) => {
    const base = event && Array.isArray(event.systemPrompt) ? event.systemPrompt : [];
    const entry = 'CAVEMAN MODE ACTIVE - session ruleset applies.' + String.fromCharCode(10, 10) + RULE;
    if (base.indexOf(entry) !== -1) return undefined;
    return { systemPrompt: base.concat([entry]) };
  });
};
`;
}

function ompPluginDir() {
  return path.join(os.homedir(), '.omp', OMP_PLUGIN_DIRNAME);
}

function assertOmpRoot(root) {
  try {
    const stat = fs.lstatSync(root);
    if (stat.isSymbolicLink() || !stat.isDirectory()) throw new Error('OMP integration root must be a real directory: ' + root);
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
}

// Query the host instead of guessing its profile/XDG/plugin directory. A name
// match is not ownership: OMP link() removes the old path even without --force.
function inspectOmpRegistration(pluginDir) {
  function readJSON(command) {
    const result = captureSpawn('omp', ['plugin', command, '--json']);
    if (!spawnOk(result)) throw new Error(`cannot inspect OMP plugin ${command}; leaving registration untouched`);
    try { return JSON.parse(result.stdout); }
    catch (_) { throw new Error(`invalid OMP plugin ${command} response; leaving registration untouched`); }
  }
  const checks = readJSON('doctor');
  const directory = Array.isArray(checks) && checks.find(check => check.name === 'plugins_directory');
  let linked = false;
  if (directory?.status === 'ok' && directory.message?.startsWith('Found at ')) {
    const root = directory.message.slice('Found at '.length);
    if (!path.isAbsolute(root)) throw new Error('invalid OMP plugin directory');
    const target = path.join(root, 'node_modules', OMP_PLUGIN_NAME);
    let stat;
    try { stat = fs.lstatSync(target); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (stat) {
      let matches = false;
      try { matches = stat.isSymbolicLink() && fs.realpathSync(target) === fs.realpathSync(pluginDir); }
      catch (_) { /* dangling or unreadable links are not proof of ownership */ }
      if (!matches) throw new Error('OMP plugin name conflict: caveman points to unowned content; remove it through OMP first');
      linked = true;
    }
  } else if (!(directory?.status === 'warning' && directory.message === 'Not created yet')) {
    throw new Error('cannot resolve OMP plugin directory; leaving registration untouched');
  }
  const plugins = readJSON('list');
  if (!Array.isArray(plugins?.npm) || !Array.isArray(plugins?.marketplace)) {
    throw new Error('invalid OMP plugin list response; leaving registration untouched');
  }
  if (plugins.marketplace.some(plugin => plugin.id === OMP_PLUGIN_NAME || plugin.id?.startsWith(OMP_PLUGIN_NAME + '@'))) {
    throw new Error('OMP plugin name conflict: caveman is installed from a marketplace; remove it through OMP first');
  }
  const registered = plugins.npm.filter(plugin => plugin.name === OMP_PLUGIN_NAME);
  if (registered.length && !linked) throw new Error('OMP plugin name conflict: caveman registration is not owned');
  return { linked, registered: registered.length > 0 };
}

function packageVersion(repoRoot) {
  if (!repoRoot) return OMP_PACKAGE_VERSION;
  try {
    const raw = fs.readFileSync(path.join(repoRoot, OMP_PACKAGE_FILE), 'utf8');
    const pkg = JSON.parse(raw);
    return typeof pkg.version === 'string' && pkg.version ? pkg.version : OMP_PACKAGE_VERSION;
  } catch (_) {
    return OMP_PACKAGE_VERSION;
  }
}

function writeOmpPluginPackage(ctx, pluginDir) {
  const { repoRoot } = ctx;
  if (!repoRoot) throw new Error('native install requires local repo clone');

  fs.mkdirSync(pluginDir, { recursive: true });

  const pkg = {
    name: OMP_PLUGIN_NAME,
    version: packageVersion(repoRoot),
    description: OMP_PLUGIN_DESCRIPTION,
    omp: {
      description: OMP_PLUGIN_DESCRIPTION,
      extensions: [OMP_EXTENSION_ENTRY],
    },
  };
  fs.writeFileSync(path.join(pluginDir, OMP_PACKAGE_FILE), JSON.stringify(pkg, null, 2) + '\n');
  const ruleBody = fs.readFileSync(path.join(repoRoot, 'src', 'rules', 'caveman-activate.md'), 'utf8');
  fs.writeFileSync(path.join(pluginDir, OMP_INDEX_FILE), ompExtensionSource(ruleBody));

  const skillsRoot = path.join(pluginDir, 'skills');
  for (const name of OMP_SKILL_DIRS) {
    const src = path.join(repoRoot, 'skills', name);
    if (fs.existsSync(src)) fs.cpSync(src, path.join(skillsRoot, name), { recursive: true });
  }

  const commandsRoot = path.join(pluginDir, 'commands');
  fs.mkdirSync(commandsRoot, { recursive: true });
  const commandSrcRoot = path.join(repoRoot, 'src', 'plugins', 'opencode', 'commands');
  for (const name of OMP_COMMAND_FILES) {
    const src = path.join(commandSrcRoot, name);
    if (fs.existsSync(src)) fs.copyFileSync(src, path.join(commandsRoot, name));
  }

  const agentsRoot = path.join(pluginDir, 'agents');
  fs.mkdirSync(agentsRoot, { recursive: true });
  const agentSrcRoot = path.join(repoRoot, 'agents');
  for (const name of OMP_AGENT_FILES) {
    const src = path.join(agentSrcRoot, name);
    if (fs.existsSync(src)) {
      fs.writeFileSync(path.join(agentsRoot, name), transformOpencodeAgentFrontmatter(fs.readFileSync(src, 'utf8')));
    }
  }

  const rulesRoot = path.join(pluginDir, 'rules');
  fs.mkdirSync(rulesRoot, { recursive: true });
  fs.writeFileSync(path.join(rulesRoot, OMP_RULE_FILE), ruleBody);
}

function installOmp(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ Oh My Pi (OMP) detected');

  if (!repoRoot) {
    warn('  OMP native install requires a local clone of the caveman repo.');
    note('  Re-run from a clone: git clone https://github.com/' + REPO + ' && cd caveman && node installer/install.js --only omp');
    results.failed.push(['omp', 'native install requires local repo clone']);
    process.stdout.write('\n');
    return;
  }

  const pluginDir = ompPluginDir();
  if (opts.dryRun) {
    note(`  would prepare OMP plugin package at ${pluginDir}/`);
    note(`  would copy ${OMP_SKILL_DIRS.length} skill dirs, ${OMP_COMMAND_FILES.length} commands, ${OMP_AGENT_FILES.length} agents, and ${OMP_RULE_COUNT} rule`);
    runSpawn('omp', ['plugin', 'install', pluginDir], null, true);
    results.installed.push('omp');
    process.stdout.write('\n');
    return;
  }

  try {
    const root = path.dirname(pluginDir);
    assertOmpRoot(root);
    const operations = [{ relativePath: OMP_PLUGIN_DIRNAME }];
    OWNED.preflightOwnedInstall({ root, integration: 'omp', operations, force: opts.force });
    inspectOmpRegistration(pluginDir);
    OWNED.installOwned({
      root, integration: 'omp', force: opts.force, note,
      operations: [{
        relativePath: OMP_PLUGIN_DIRNAME,
        write: (stage) => writeOmpPluginPackage(ctx, stage),
        register: (target) => {
          const result = runSpawn('omp', ['plugin', 'install', target], null, false);
          if (!spawnOk(result)) throw new Error('omp plugin install failed; owned package and backups retained for retry or uninstall');
        },
      }],
    });
    results.installed.push('omp');
  } catch (e) {
    warn('  OMP install failed: ' + (e && e.message || e));
    results.failed.push(['omp', (e && e.message) || 'unknown error']);
  }
  process.stdout.write('\n');
}

function opencodeConfigDir() {
  // opencode uses ~/.config/opencode on every platform (on Windows that's
  // %USERPROFILE%\.config\opencode via os.homedir()), NOT %APPDATA% (#376).
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  return path.join(os.homedir(), '.config', 'opencode');
}

function opencodeConfigPath(dir) {
  const jsonc = path.join(dir, 'opencode.jsonc');
  const json  = path.join(dir, 'opencode.json');
  if (fs.existsSync(jsonc)) return jsonc;
  if (fs.existsSync(json))  return json;
  return jsonc;
}

function installOpencode(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ opencode detected');

  if (!repoRoot) {
    warn('  opencode native install requires a local clone of the caveman repo.');
    note('  Re-run from a clone: git clone https://github.com/' + REPO + ' && cd caveman && node installer/install.js --only opencode');
    results.failed.push(['opencode', 'native install requires local repo clone']);
    process.stdout.write('\n');
    return;
  }

  const dir = opencodeConfigDir();
  const pluginDir   = path.join(dir, 'plugins', 'caveman');
  const commandsDir = path.join(dir, 'commands');
  const agentsDir   = path.join(dir, 'agents');
  const skillsDir   = path.join(dir, 'skills');
  const opencodeJson = opencodeConfigPath(dir);
  const agentsMd     = path.join(dir, 'AGENTS.md');

  if (opts.dryRun) {
    note(`  would mkdir ${pluginDir}/, ${commandsDir}/, ${agentsDir}/, ${skillsDir}/`);
    note(`  would copy plugin.js + server.js + package.json + caveman-config.cjs + caveman-parse.cjs into ${pluginDir}/`);
    note(`  would copy ${OPENCODE_COMMAND_FILES.length} command files into ${commandsDir}/`);
    note(`  would copy ${OPENCODE_AGENT_FILES.length} cavecrew agents into ${agentsDir}/`);
    note(`  would copy ${OPENCODE_SKILL_DIRS.length} skill dirs into ${skillsDir}/`);
    note(`  would patch ${opencodeJson} with "plugin" entry${opts.withMcpShrink ? ' + caveman-shrink MCP' : ''}`);
    note(`  would write Tier-3 ruleset to ${agentsMd}`);
    results.installed.push('opencode');
    process.stdout.write('\n');
    return;
  }

  try {
    const pluginSrc = path.join(repoRoot, 'src', 'plugins', 'opencode');
    // Preflight every same-named path before writing anything. The ownership
    // journal records installed digests and force backups, so uninstall can
    // remove only bytes this installer still owns.
    const operations = [{
      relativePath: 'plugins/caveman',
      write: (stage) => {
        fs.mkdirSync(stage, { recursive: true });
        fs.copyFileSync(path.join(pluginSrc, 'plugin.js'), path.join(stage, 'plugin.js'));
        // opencode 2.x ignores the plugin.js config entry and loads this dir's
        // server.js; 1.x never scans inside plugin directories.
        fs.copyFileSync(path.join(pluginSrc, 'server.js'), path.join(stage, 'server.js'));
        fs.copyFileSync(path.join(pluginSrc, 'package.json'), path.join(stage, 'package.json'));
        // Plugin dir is ESM; the CommonJS config bridge needs .cjs.
        fs.copyFileSync(path.join(repoRoot, 'src', 'hooks', 'caveman-config.js'), path.join(stage, 'caveman-config.cjs'));
        // Shared mode parser keeps opencode and Claude hook behavior identical.
        fs.copyFileSync(path.join(repoRoot, 'src', 'hooks', 'caveman-parse.js'), path.join(stage, 'caveman-parse.cjs'));
      },
    }];

    const cmdSrcDir = path.join(pluginSrc, 'commands');
    for (const f of OPENCODE_COMMAND_FILES) {
      const src = path.join(cmdSrcDir, f);
      if (!fs.existsSync(src)) continue; // defense-in-depth: skip a missing command file rather than crash (#434)
      operations.push({
        relativePath: `commands/${f}`,
        write: (stage) => fs.copyFileSync(src, stage),
      });
    }

    // Subagents target Claude Code's `tools: [...]` schema; strip that line for
    // opencode and materialize transformed bytes directly into the owned path.
    //    YAML array); opencode rejects that form and refuses to boot until the
    //    file is removed. Strip the `tools:` line on copy — opencode falls back
    //    to its default tool set, and subagent prompts already self-restrict in
    //    the body. Issue #386.
    const agentSrcDir = path.join(repoRoot, 'agents');
    for (const f of OPENCODE_AGENT_FILES) {
      const src = path.join(agentSrcDir, f);
      if (!fs.existsSync(src)) continue;
      const body = transformOpencodeAgentFrontmatter(fs.readFileSync(src, 'utf8'), { subagent: true });
      operations.push({
        relativePath: `agents/${f}`,
        write: (stage) => fs.writeFileSync(stage, body, { mode: 0o600, flag: 'wx' }),
      });
    }

    // Skills are journaled as whole directories. Any unjournaled same-named
    // directory is user content and blocks install unless --force backs it up.
    const skillSrcDir = path.join(repoRoot, 'skills');
    for (const name of OPENCODE_SKILL_DIRS) {
      const src = path.join(skillSrcDir, name);
      if (!fs.existsSync(src)) continue;
      operations.push({
        relativePath: `skills/${name}`,
        write: (stage) => OWNED.copyPath(src, stage),
      });
    }
    OWNED.installOwned({
      root: dir,
      integration: 'opencode',
      operations,
      force: opts.force,
      note,
    });
    process.stdout.write(`  installed owned opencode payload under: ${dir}\n`);

    // 5. AGENTS.md — Tier-3 always-on ruleset. Wrapped in begin/end markers so
    //    a later --uninstall can strip our block cleanly even if the user has
    //    authored content above AND below it. Idempotency check uses the begin
    //    marker (the legacy sentinel still matches old installs).
    const ruleBody = fs.readFileSync(path.join(repoRoot, 'src', 'rules', 'caveman-activate.md'), 'utf8').trimEnd() + '\n';
    const fencedBlock = `${OPENCODE_AGENTS_MD_BEGIN}\n${ruleBody}${OPENCODE_AGENTS_MD_END}\n`;
    if (fs.existsSync(agentsMd)) {
      const existing = fs.readFileSync(agentsMd, 'utf8');
      // A CRLF file gets a CRLF block, so its line endings stay one style.
      const eol = existing.includes('\r\n') ? '\r\n' : '\n';
      const block = fencedBlock.replace(/\r?\n/g, eol);
      // Both markers present is not enough: they must be exactly one matched
      // pair, in order. An END above a BEGIN (or an orphan BEGIN) made the
      // slice arithmetic below run on end === -1, which re-appended the whole
      // file from byte 19 and compounded on every re-run.
      const begin = existing.indexOf(OPENCODE_AGENTS_MD_BEGIN);
      const end = begin === -1 ? -1 : existing.indexOf(OPENCODE_AGENTS_MD_END, begin);
      const alreadyFenced = begin !== -1 && end > begin
        && countOccurrences(existing, OPENCODE_AGENTS_MD_BEGIN) === 1
        && countOccurrences(existing, OPENCODE_AGENTS_MD_END) === 1;
      const damagedFence = !alreadyFenced
        && (existing.includes(OPENCODE_AGENTS_MD_BEGIN) || existing.includes(OPENCODE_AGENTS_MD_END));
      const alreadyByLegacySentinel = !alreadyFenced && !damagedFence
        && existing.includes(OPENCODE_AGENTS_MD_SENTINEL);
      if (damagedFence) {
        note(`  ${agentsMd} has unmatched caveman markers — leaving it untouched`);
        note('  remove the stray <!-- caveman-begin/end --> marker, then re-run');
      } else if (alreadyFenced) {
        // Refresh in place when the shipped ruleset has changed. The old
        // presence-only check meant a ruleset edit never reached anyone who
        // had already installed — the block went stale forever. Only the
        // bytes between our own markers are touched; user content around
        // them is preserved exactly.
        const currentBlock = existing.slice(begin, end + OPENCODE_AGENTS_MD_END.length);
        if (currentBlock === block.slice(0, -eol.length)) {
          note(`  ${agentsMd} already contains the current caveman ruleset`);
        } else {
          const next = existing.slice(0, begin) + block.slice(0, -eol.length)
            + existing.slice(end + OPENCODE_AGENTS_MD_END.length);
          fs.writeFileSync(agentsMd, next, { mode: 0o644 });
          process.stdout.write(`  refreshed caveman ruleset in ${agentsMd}\n`);
        }
      } else if (alreadyByLegacySentinel) {
        if (!opts.force) {
          note(`  ${agentsMd} contains a legacy (un-fenced) caveman block — leaving as-is`);
          note('  re-run with --force to migrate it to a fenced block');
        }
        if (opts.force) {
          // Migrate, don't wipe (issue #594): the old code replaced the whole
          // file, destroying any user-authored content around the legacy
          // block. Back up once, then remove only the legacy block: exact
          // match of the current rule body when possible, otherwise cut from
          // the sentinel's paragraph start to EOF (the legacy path APPENDED
          // the block, so user content precedes it; anything after lives on
          // in the backup).
          const agentsBak = agentsMd + '.bak';
          if (!fs.existsSync(agentsBak)) {
            try { fs.copyFileSync(agentsMd, agentsBak); } catch (_) {}
          }
          const bodyTrim = ruleBody.trimEnd().replace(/\r?\n/g, eol);
          let userPart;
          const exact = existing.indexOf(bodyTrim);
          if (exact !== -1) {
            userPart = (existing.slice(0, exact) + existing.slice(exact + bodyTrim.length)).trim();
          } else {
            const sentinelAt = existing.indexOf(OPENCODE_AGENTS_MD_SENTINEL);
            const cutAt = existing.lastIndexOf('\n\n', sentinelAt);
            userPart = cutAt === -1 ? '' : existing.slice(0, cutAt).trim();
            note(`  legacy block did not match the current ruleset — everything from the sentinel down was replaced; original kept at ${agentsBak}`);
          }
          const next = (userPart ? userPart + eol + eol : '') + block;
          fs.writeFileSync(agentsMd, next, { mode: 0o644 });
          process.stdout.write(`  migrated ${agentsMd} legacy block to fenced (backup: ${agentsBak})\n`);
        }
      } else {
        const sep = existing.endsWith(eol + eol) ? '' : (existing.endsWith('\n') ? eol : eol + eol);
        fs.writeFileSync(agentsMd, existing + sep + block, { mode: 0o644 });
        process.stdout.write(`  appended caveman ruleset to ${agentsMd}\n`);
      }
    } else {
      fs.writeFileSync(agentsMd, fencedBlock, { mode: 0o644 });
      process.stdout.write(`  installed: ${agentsMd}\n`);
    }

    // 6. opencode.json — add plugin entry; optional caveman-shrink MCP.
    const ocMeta = {};
    let cfg = SETTINGS.readSettings(opencodeJson, ocMeta);
    if (cfg === null) {
      warn(`  ${opencodeJson} unparseable; will not touch it. Edit manually then re-run.`);
      results.failed.push(['opencode', 'opencode.json unparseable']);
      process.stdout.write('\n');
      return;
    }
    // Preserve the original on first install only — repeat installs would
    // otherwise overwrite the only known-good copy with an already-merged file.
    const opencodeBak = opencodeJson + '.bak';
    if (fs.existsSync(opencodeJson) && !fs.existsSync(opencodeBak)) {
      try { fs.copyFileSync(opencodeJson, opencodeBak); } catch (_) {}
    }
    // opencode.jsonc legitimately carries comments; we re-serialize plain JSON.
    if (ocMeta.jsonc) {
      warn(`  note: ${opencodeJson} contains comments — rewriting it drops them.`);
      warn(`        Your original (with comments) is preserved at ${opencodeBak}`);
    }
    if (!Array.isArray(cfg.plugin)) cfg.plugin = [];
    if (!cfg.plugin.includes(OPENCODE_PLUGIN_REL)) {
      cfg.plugin.push(OPENCODE_PLUGIN_REL);
    }
    if (opts.withMcpShrink) {
      // opts.withMcpShrink is the array of upstream-cmd tokens parseArgs
      // produced. caveman-shrink is a proxy — it crashes without an upstream,
      // so we always wire one through.
      if (!cfg.mcp || typeof cfg.mcp !== 'object') cfg.mcp = {};
      if (!cfg.mcp['caveman-shrink']) {
        cfg.mcp['caveman-shrink'] = {
          type: 'local',
          command: ['npx', '-y', MCP_SHRINK_PKG, ...opts.withMcpShrink],
          enabled: true,
        };
        process.stdout.write(`  registered caveman-shrink MCP server (wraps: ${opts.withMcpShrink.join(' ')})\n`);
      }
    }
    SETTINGS.writeSettings(opencodeJson, cfg);
    process.stdout.write(`  patched: ${opencodeJson}\n`);

    results.installed.push('opencode');
  } catch (e) {
    warn('  opencode install failed: ' + (e && e.message || e));
    results.failed.push(['opencode', (e && e.message) || 'unknown error']);
  }
  process.stdout.write('\n');
}

// ── OpenClaw native install ───────────────────────────────────────────────
// Drops skills/caveman/ into the OpenClaw workspace and appends a small
// auto-injected bootstrap block to the workspace SOUL.md. Always-on behavior
// comes from SOUL.md (auto-injected each turn); the skill folder makes
// caveman discoverable via `openclaw skills list`. See installer/lib/openclaw.js
// for the actual file writes.
function installOpenclaw(ctx) {
  const { say, note, warn, opts, repoRoot, results } = ctx;
  results.detected++;
  say('→ OpenClaw detected');

  const log = {
    write: (s) => process.stdout.write(s),
    note: (s) => note(s),
    warn: (s) => warn(s),
  };

  const r = OPENCLAW.installOpenclaw({
    workspace: process.env.OPENCLAW_WORKSPACE || undefined,
    repoRoot,
    dryRun: opts.dryRun,
    force: opts.force,
    version: OPENCLAW_SKILL_VERSION,
    log,
  });

  if (r.ok) results.installed.push('openclaw');
  else results.failed.push(['openclaw', r.reason || 'install failed']);

  process.stdout.write('\n');
}

// ── Hooks installer ────────────────────────────────────────────────────────
// Replaces src/hooks/install.sh + src/hooks/install.ps1.
async function installHooks(ctx) {
  const { note, warn, opts, repoRoot, configDir } = ctx;
  const hooksDir = path.join(configDir, 'hooks');
  const settingsPath = path.join(configDir, 'settings.json');
  const sourceDir = repoRoot ? path.join(repoRoot, 'src', 'hooks') : null;

  // Node gives every .js in hooks/ the module type of hooks/package.json. If
  // another plugin's manifest says "module", caveman's CommonJS hooks crash on
  // every event, so wiring them would only add errors.
  const manifest = path.join(hooksDir, HOOKS_MANIFEST);
  let foreignType = null;
  try { if (!hooksManifestIsOurs(manifest)) foreignType = JSON.parse(fs.readFileSync(manifest, 'utf8')).type; } catch (_) {}
  if (foreignType === 'module') {
    warn(`  ${manifest} belongs to another plugin and says "type":"module", so caveman's hooks cannot run there.`);
    return `${manifest} says "type":"module"; nothing changed. Use the plugin install instead`;
  }

  if (opts.dryRun) {
    note(`  would mkdir -p ${hooksDir}`);
    for (const f of HOOK_FILES) note(`  would install ${path.join(hooksDir, f)}`);
    note(`  would merge SessionStart + SubagentStart + UserPromptSubmit + SessionEnd + statusline into ${settingsPath}`);
    return 'ok';
  }

  fs.mkdirSync(hooksDir, { recursive: true });

  // Copy or download each hook file. Local-clone-first for offline installs;
  // local copies are trusted — they come from the same package as this script.
  // Downloaded files (the detached-script / curl fallback) must match the
  // SHA-256 manifest published at the pinned release ref (#262). They are
  // staged and all verified before any lands in hooksDir, so a missing
  // manifest or one bad file aborts with the previous install intact.
  const plan = [];
  for (const f of HOOK_FILES) {
    const dest = path.join(hooksDir, f);
    if (f === HOOKS_MANIFEST && fs.existsSync(dest) && !hooksManifestIsOurs(dest)) {
      warn(`  ${dest} belongs to another plugin — left untouched.`);
      continue;
    }
    const local = sourceDir && path.join(sourceDir, f);
    plan.push({ f, dest, src: local && fs.existsSync(local) ? local : null });
  }
  const remote = plan.filter((item) => !item.src);
  const scratch = remote.length ? privateTmpDir() : null;
  try {
    if (remote.length) {
      const checksums = await loadRemoteHookChecksums();
      if (!checksums) {
        return `no hook integrity manifest at ${PINNED_REF} (${HOOKS_REMOTE}/checksums.sha256) — ` +
               'refusing to install unverified hooks; nothing changed. Retry, or install from a clone: node installer/install.js';
      }
      for (const item of remote) {
        item.src = path.join(scratch, item.f);
        try { await downloadTo(`${HOOKS_REMOTE}/${item.f}`, item.src); }
        catch (e) { return `download ${item.f} failed: ${e.message}; nothing changed`; }
        const want = checksums.get(item.f);
        const got = sha256File(item.src);
        if (!want || want !== got) {
          return `integrity check failed for ${item.f} (expected ${want || '<not in manifest>'}, got ${got}) — ` +
                 `refusing to install a hook that doesn't match pinned release ${PINNED_REF}; nothing changed`;
        }
      }
    }
    // Stage every file beside its destination, then rename each over it: an
    // ENOSPC/EACCES while copying leaves the previous hooks untouched instead
    // of a mix of old and new. COPYFILE_EXCL refuses a pre-planted temp name
    // (symlink included), and rename replaces a symlinked dest rather than
    // writing through it.
    const staged = [];
    try {
      for (const item of plan) {
        const tmp = `${item.dest}.tmp-${process.pid}`;
        staged.push({ tmp, dest: item.dest });
        fs.copyFileSync(item.src, tmp, fs.constants.COPYFILE_EXCL);
      }
    } catch (e) {
      for (const { tmp } of staged) try { fs.unlinkSync(tmp); } catch (_) { /* best effort */ }
      return `copying hooks into ${hooksDir} failed: ${e.message}; nothing changed`;
    }
    for (const { tmp, dest } of staged) {
      fs.renameSync(tmp, dest);
      process.stdout.write(`  installed: ${dest}\n`);
    }
  } finally {
    if (scratch) try { fs.rmSync(scratch, { recursive: true, force: true }); } catch (_) { /* best effort */ }
  }

  // chmod statusline (no-op on Windows)
  try { fs.chmodSync(path.join(hooksDir, 'caveman-statusline.sh'), 0o755); } catch (_) {}

  // Merge into settings.json
  const settingsMeta = {};
  let settings = SETTINGS.readSettings(settingsPath, settingsMeta);
  if (settings === null) {
    warn('  settings.json unparseable; will not touch it. Edit manually then re-run.');
    return 'settings.json unparseable';
  }
  // Backup once, preserved across reinstalls. Without the !fs.existsSync(bak)
  // guard, the second install would overwrite the only known-good copy with
  // the already-merged file, destroying recovery. A file already holding our
  // hooks is our own earlier merge, not the user's original: restoring it
  // would bring back hooks uninstall deleted. Comments still get kept.
  const bak = settingsPath + '.bak';
  const ours = SETTINGS.removeCavemanHooks(structuredClone(settings)) > 0;
  if (fs.existsSync(settingsPath) && !fs.existsSync(bak) && (settingsMeta.jsonc || !ours)) {
    try { fs.copyFileSync(settingsPath, bak); } catch (_) {}
  }
  // We re-serialize plain JSON, so // and /* */ comments do not survive. Say
  // so rather than deleting a user's annotations silently.
  if (settingsMeta.jsonc) {
    warn(`  note: ${settingsPath} contains comments — rewriting it drops them.`);
    warn(`        Your original (with comments) is preserved at ${bak}`);
  }

  const node = absoluteNodePath();
  const activate = path.join(hooksDir, 'caveman-activate.js');
  const tracker  = path.join(hooksDir, 'caveman-mode-tracker.js');
  const stats = path.join(hooksDir, 'caveman-stats.js');
  const statusline = path.join(hooksDir, 'caveman-statusline.sh');

  // Migrate any legacy bare-`node` invocations of our managed scripts.
  SETTINGS.rewriteLegacyManagedHookCommands(settings, node);

  SETTINGS.addCommandHook(settings, 'SessionStart', {
    command: PLATFORM_PATHS.hookCommand(node, [activate]),
    marker: 'caveman-activate',
    timeout: 30,
    statusMessage: 'Loading caveman mode...',
  });

  // #621: subagents inherit this session's mode. Same script, read-only path.
  SETTINGS.addCommandHook(settings, 'SubagentStart', {
    command: `${PLATFORM_PATHS.hookCommand(node, [activate])} --subagent`,
    marker: 'caveman-activate',
    timeout: 30,
    statusMessage: 'Loading caveman mode for subagent...',
  });

  SETTINGS.addCommandHook(settings, 'UserPromptSubmit', {
    command: PLATFORM_PATHS.hookCommand(node, [tracker]),
    marker: 'caveman-mode-tracker',
    timeout: 30,
    statusMessage: 'Tracking caveman mode...',
  });

  // SessionEnd — silently record a lifetime stats snapshot. Keep the `--record`
  // flag unquoted (path only is quoted) so the command matches the plugin
  // manifest and standalone installers byte-for-byte.
  SETTINGS.addCommandHook(settings, 'SessionEnd', {
    command: `${PLATFORM_PATHS.hookCommand(node, [stats])} --record`,
    marker: 'caveman-stats',
    timeout: 5,
    statusMessage: 'Recording caveman stats...',
  });

  // Statusline — set if absent or already pointing at our script.
  // Windows: prefer pwsh (PowerShell 7+, cross-platform), fall back to
  // powershell.exe (Windows PowerShell 5.1, ships with every Windows install).
  // Use -ExecutionPolicy Bypass so users without RemoteSigned policy can run.
  const psHost = IS_WIN && hasCmd('pwsh') ? 'pwsh' : (IS_WIN ? 'powershell' : null);
  const slCmd = IS_WIN
    ? PLATFORM_PATHS.hookCommand(psHost, ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', path.join(hooksDir, 'caveman-statusline.ps1')])
    : PLATFORM_PATHS.hookCommand('bash', [statusline]);
  if (!settings.statusLine) {
    settings.statusLine = { type: 'command', command: slCmd };
    process.stdout.write('  statusline badge configured.\n');
  } else {
    const existing = typeof settings.statusLine === 'string'
      ? settings.statusLine
      : (settings.statusLine.command || '');
    if (existing.includes(statusline) || existing.includes('caveman-statusline')) {
      process.stdout.write('  statusline badge already configured.\n');
    } else {
      process.stdout.write('  NOTE: existing statusline detected — caveman badge NOT added.\n');
      process.stdout.write('        See src/hooks/README.md to add the badge to your existing statusline.\n');
    }
  }

  // Defensive validation before write — Claude Code Zod will discard the
  // entire settings.json if any single hook is malformed (#249-class footgun).
  SETTINGS.validateHookFields(settings);
  SETTINGS.writeSettings(settingsPath, settings);
  process.stdout.write(`  hooks wired in ${settingsPath}\n`);
  return 'ok';
}

// ── MCP shrink wiring ─────────────────────────────────────────────────────
function installMcpShrink(ctx) {
  const { note, warn, opts } = ctx;
  // Probe npm first — registry outage = clean skip with manual snippet.
  const probe = captureSpawn('npm', ['view', MCP_SHRINK_PKG, 'name']);
  if (probe.status !== 0) {
    warn(`    'npm view ${MCP_SHRINK_PKG}' returned no metadata — registry unreachable or package missing.`);
    note('    Skipping registration. Re-run --with-mcp-shrink when the registry is reachable.');
    return { kind: 'skip', why: 'npm registry probe failed' };
  }
  // Detect modern `claude mcp add`
  const help = captureSpawn('claude', ['mcp', '--help']);
  if (help.status !== 0) {
    note("    'claude mcp add' not available on this CLI. Add the snippet from");
    note('    src/hooks/README.md to your Claude Code MCP config manually.');
    return { kind: 'skip', why: 'manual config required' };
  }
  // opts.withMcpShrink is always an array of upstream-cmd tokens by the
  // time we get here; parseArgs rejects bare --with-mcp-shrink. The proxy
  // gets `npx -y caveman-shrink <upstream tokens...>` so it has something
  // to wrap.
  const upstream = opts.withMcpShrink;
  const r = runSpawn(
    'claude',
    ['mcp', 'add', 'caveman-shrink', '--', 'npx', '-y', MCP_SHRINK_PKG, ...upstream],
    null, opts.dryRun
  );
  if (spawnOk(r)) {
    note(`    registered, wrapping: ${upstream.join(' ')}`);
    note(`    Edit ~/.claude.json mcpServers["caveman-shrink"] to change the upstream,`);
    note('    or `claude mcp remove caveman-shrink` to drop it.');
    note(`    Docs: https://github.com/${REPO}/tree/main/src/mcp-servers/caveman-shrink`);
    return { kind: 'ok' };
  }
  return { kind: 'fail', why: 'claude mcp add failed' };
}

// ── Init writers (per-repo rule files) ────────────────────────────────────
async function runInit(ctx) {
  const { warn, opts, repoRoot } = ctx;
  const local = repoRoot && path.join(repoRoot, 'src/tools/caveman-init.js');
  const args = [process.cwd()];
  if (opts.dryRun) args.push('--dry-run');
  if (opts.force)  args.push('--force');
  if (local && fs.existsSync(local)) {
    const r = runSpawn(process.execPath, [local, ...args], null, opts.dryRun);
    return spawnOk(r);
  }
  // No remote fallback (#627). A lone installer/install.js used to download
  // caveman-init.js and EXECUTE it with no integrity check. Every supported
  // install (a clone, or npx github:...) ships src/tools/caveman-init.js, and
  // pinning it in the hooks manifest would go stale on every SKILL.md change
  // (skills/compile.mjs rewrites its RULE_BODY).
  warn('  per-repo init needs the full caveman package — run: npx -y github:' + REPO + ' -- --with-init');
  warn('  (or node installer/install.js --with-init from a clone)');
  return false;
}

// privateTmpDir returns a fresh 0700 directory with an unguessable name. The old
// predictable paths (`caveman-init-<pid>.js`, `caveman-checksums-<pid>-<ms>`)
// could be pre-planted as symlinks by any local user, and `curl -o` follows a
// symlink — so the installer wrote through it and, for the init script, then
// EXECUTED what landed there.
function privateTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-'));
}

// ── HTTPS download via stdlib ─────────────────────────────────────────────
function downloadTo(url, dest) {
  // Prefer curl/wget when available (better proxy + cert handling on legacy
  // systems); fall back to Node https.
  if (hasCmd('curl')) {
    const r = child_process.spawnSync('curl', ['-fsSL', '-o', dest, url], { stdio: 'inherit' });
    if (r.status === 0) return;
    throw new Error(`curl failed for ${url}`);
  }
  const https = require('https');
  return new Promise((resolve, reject) => {
    const req = https.get(url, (res) => {
      if (res.statusCode && res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        resolve(downloadTo(res.headers.location, dest));
        return;
      }
      if (res.statusCode !== 200) { reject(new Error(`HTTP ${res.statusCode} for ${url}`)); return; }
      const out = fs.createWriteStream(dest);
      res.pipe(out);
      out.on('finish', () => out.close(resolve));
      out.on('error', reject);
    });
    req.on('error', reject);
  });
}

// ── Integrity verification for downloaded hooks (#262) ─────────────────────
function sha256File(p) {
  return crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
}

// Download + parse the hook integrity manifest from the pinned release ref.
// Returns Map<basename, sha256hex>, or null when the manifest is unavailable
// or empty — the caller then refuses to install downloaded hooks. Parses
// the standard `sha256sum` text format: "<64-hex>  <path>" (two spaces, or
// " *<path>" binary marker).
async function loadRemoteHookChecksums() {
  const scratch = privateTmpDir();
  const tmp = path.join(scratch, 'checksums.sha256');
  try {
    await downloadTo(`${HOOKS_REMOTE}/checksums.sha256`, tmp);
    const txt = fs.readFileSync(tmp, 'utf8');
    const map = new Map();
    for (const line of txt.split('\n')) {
      const m = line.trim().match(/^([0-9a-fA-F]{64})\s+\*?(.+)$/);
      if (m) map.set(path.basename(m[2].trim()), m[1].toLowerCase());
    }
    return map.size ? map : null;
  } catch (_) {
    return null;
  } finally {
    try { fs.rmSync(scratch, { recursive: true, force: true }); } catch (_) { /* best effort */ }
  }
}

// ── Uninstall ─────────────────────────────────────────────────────────────

// Agents whose `caveman enable` journal is still on disk. `CAVEMAN_HOME` is the
// same override the CLI itself honors, so a non-default home is not read as a
// clean machine. Read-only and silent-failing: a machine that never had the CLI
// has no such directory, and an unreadable one must not fail an uninstall.
function remainingNativeIntegrations() {
  const dir = path.join(process.env.CAVEMAN_HOME || path.join(os.homedir(), '.caveman'), 'integrations');
  try {
    return fs.readdirSync(dir)
      // A journal is exactly `<agent>.json`. `.pending-<agent>.json` is an
      // interrupted transaction; `<agent>.voice-skills.json`,
      // `<agent>.agent-native-bundle*.json` and `claude-profiles.json` are
      // records `disable` keeps on purpose, not routes.
      .filter((name) => /^[^.]+\.json$/.test(name) && name !== 'claude-profiles.json')
      .map((name) => name.slice(0, -'.json'.length))
      .sort();
  } catch (_) {
    return [];
  }
}

// The CLI this package depends on, when it is newer than the `caveman` first on
// PATH. An older CLI cannot undo state a newer one wrote: 1.x removed the route
// but left Claude Code on the caveman-auto model it never knew. Under npx the
// bundled CLI is already first on PATH; this covers a clone or global install.
function newerBundledCli() {
  try {
    const manifest = require.resolve('@caveman-ai/cli/package.json');
    const pkg = JSON.parse(fs.readFileSync(manifest, 'utf8'));
    if (!pkg.bin || typeof pkg.bin.caveman !== 'string') return null;
    const bin = path.join(path.dirname(manifest), pkg.bin.caveman);
    const want = /^(\d+)\.(\d+)\.(\d+)/.exec(pkg.version || '');
    // A foreign `caveman` that never answers must not stall uninstall.
    const probe = captureSpawn('caveman', ['--version'], { timeout: 10000, killSignal: 'SIGKILL' });
    const have = /(\d+)\.(\d+)\.(\d+)/.exec(probe.stdout || '');
    if (!want || !have || !spawnOk(probe) || !fs.existsSync(bin)) return null;
    for (let i = 1; i <= 3; i++) {
      if (Number(want[i]) !== Number(have[i])) return Number(want[i]) > Number(have[i]) ? bin : null;
    }
  } catch (_) { /* no bundled CLI: use the one on PATH */ }
  return null;
}

// The `engines.node` floor of the CLI this package depends on, when this Node
// is below it — `>=22.13` for 2.x, above the installer's own floor. Under npx
// that CLI is also the `caveman` first on PATH. Null when there is no bundled
// CLI or its floor is met.
function bundledCliNodeFloorUnmet() {
  try {
    const pkg = JSON.parse(fs.readFileSync(require.resolve('@caveman-ai/cli/package.json'), 'utf8'));
    const floor = /^>=\s*(\d+)(?:\.(\d+))?(?:\.(\d+))?\s*$/.exec((pkg.engines && pkg.engines.node) || '');
    if (!floor) return null;
    const have = process.versions.node.split('.').map(Number);
    for (let i = 0; i < 3; i++) {
      const want = Number(floor[i + 1] || 0);
      if (have[i] !== want) return have[i] < want ? floor[0].replace(/^>=\s*/, '').trim() : null;
    }
  } catch (_) { /* no bundled CLI */ }
  return null;
}

function uninstall(ctx) {
  const { say, note, warn, ok, opts, configDir } = ctx;
  let cleanupFailed = false;
  say('🪨 caveman uninstall');

  if (opts.dryRun) note('  (dry run — nothing will be removed)');

  // Native integrations (`caveman enable <agent>`) journal their prior state
  // at ~/.caveman/integrations/<agent>.json; restore it through the CLI's own
  // `disable --all` rather than re-deriving that logic here. Setup puts the CLI
  // under $CAVEMAN_HOME/cli when the global npm prefix is not writable, so an
  // older caveman can stay first on PATH; only the newer one undoes all it
  // wrote, and a second run finds nothing left, so both run. A caveman on PATH
  // older than the bundled CLI hands its turn to the bundled one. Never on a
  // Node the bundled CLI does not support: it crashes or runs unsupported
  // there, so the routes go to the guidance below instead.
  const cliNodeFloor = bundledCliNodeFloorUnmet();
  const privateCli = path.join(process.env.CAVEMAN_HOME || path.join(os.homedir(), '.caveman'), 'cli',
    ...(process.platform === 'win32' ? ['caveman.cmd'] : ['bin', 'caveman']));
  const disables = [];
  if (!cliNodeFloor && fs.existsSync(privateCli)) disables.push([privateCli, ['disable', '--all']]);
  if (!cliNodeFloor && hasCmd('caveman')) {
    const bundled = newerBundledCli();
    // The bundled CLI may not start at all: then the PATH caveman takes its turn.
    disables.push(bundled ? [process.execPath, [bundled, 'disable', '--all'], 'caveman'] : ['caveman', ['disable', '--all']]);
  }
  const failedDisables = disables.filter(([cmd, args, fallback]) => !spawnOk(runSpawn(cmd, args, null, opts.dryRun))
    && !(fallback && spawnOk(runSpawn(fallback, ['disable', '--all'], null, opts.dryRun))));
  if (failedDisables.length > 0) {
    cleanupFailed = true;
    warn('  `caveman disable --all` failed (see above); native Caveman routing may still be installed.');
  } else if (disables.length > 0 && !opts.dryRun) {
    ok('  disabled native agent integrations');
  }

  // ...and say so when one survived. `disable` removes the journal it restored
  // from, so a journal still sitting here after the call above is exact evidence
  // that a caveman route (ANTHROPIC_BASE_URL + _CLAUDE_CODE_ASSUME_FIRST_PARTY_
  // BASE_URL for Claude) is still in the host's settings — the CLI was already
  // npm-uninstalled, or `disable --all` failed. Silence there leaves the user
  // with a dead route and the Remote Control breakage of #947, with nothing in
  // the uninstall output pointing at the cause (#1040). Reading the journal
  // directory is not re-deriving the restore logic: it never writes.
  if (!opts.dryRun) {
    const stranded = remainingNativeIntegrations();
    if (stranded.length > 0) cleanupFailed = true;
    for (const agent of stranded) warn(`  ${agent}: native Caveman routing is still installed and was not removed here.`);
    if (stranded.length > 0 && cliNodeFloor) warn(`  The Caveman CLI needs Node ${cliNodeFloor} or newer; this is ${process.version}. Upgrade Node first.`);
    if (stranded.length > 0) warn('  Run `caveman disable --all` (reinstall @caveman-ai/cli first if needed) to restore the host settings.');
  }

  // Hooks: remove from settings.json + delete hook files.
  const hooksDir = path.join(configDir, 'hooks');
  const settingsPath = path.join(configDir, 'settings.json');
  // settingsClean gates the file deletion below. Deleting the hook scripts while
  // settings.json still points at them is issue #471's exact shape: Claude Code
  // dies with `Cannot find module …caveman-activate.js` on EVERY session start
  // afterwards. So a settings.json we could not read (malformed) or could not
  // write is a hard stop for the deletion, not a warning to continue past.
  let settingsClean = true;
  if (fs.existsSync(settingsPath)) {
    const settingsMeta = {};
    const settings = SETTINGS.readSettings(settingsPath, settingsMeta);
    if (!settings) {
      settingsClean = false;
      cleanupFailed = true;
      warn(`  could not parse ${settingsPath} — leaving the hook files in place.`);
      warn('  Remove the caveman entries from it by hand, then re-run --uninstall.');
    }
    if (settings) {
      // Rewriting drops comments, so write only when something of ours
      // came out.
      const removed = SETTINGS.removeCavemanHooks(settings);
      let statusRemoved = false;
      // Drop our statusline if it points at our script
      if (settings.statusLine) {
        const cmd = typeof settings.statusLine === 'string' ? settings.statusLine : (settings.statusLine.command || '');
        if (cmd.includes('caveman-statusline')) { delete settings.statusLine; statusRemoved = true; }
      }
      SETTINGS.validateHookFields(settings);
      const changed = removed > 0 || statusRemoved;
      const entries = removed > 0
        ? `${removed} caveman hook entr${removed === 1 ? 'y' : 'ies'} from settings.json`
        : 'the caveman statusline from settings.json';
      try {
        if (changed && opts.dryRun) note(`  would remove ${entries}`);
        else if (changed) {
          writeSettingsKeepingComments(settingsPath, settings, settingsMeta, warn);
          ok(`  removed ${entries}`);
        }
      } catch (e) {
        settingsClean = false;
        cleanupFailed = true;
        warn(`  could not update ${settingsPath}: ${e && e.message || e}`);
        warn('  leaving the hook files in place so the entries it still holds keep resolving.');
      }
    }
  }

  if (settingsClean && fs.existsSync(hooksDir)) {
    for (const f of HOOK_FILES) {
      const p = path.join(hooksDir, f);
      if (!fs.existsSync(p)) continue;
      if (f === HOOKS_MANIFEST && !hooksManifestIsOurs(p)) {
        note(`  left ${p} (another plugin's)`);
        continue;
      }
      if (opts.dryRun) {
        note(`  would remove ${p}`);
      } else {
        try { fs.unlinkSync(p); } catch (_) {}
        note(`  removed ${p}`);
      }
    }
    // Don't rmdir hooksDir — other plugins may use it.
  }

  // Plugin uninstall on Claude. Probe `plugin list` first so a re-run on a
  // machine where caveman was never installed (or was already removed) doesn't
  // print "Plugin not installed" stderr noise.
  if (hasCmd('claude')) {
    if (claudeHasCaveman()) {
      const r = runSpawn('claude', ['plugin', 'uninstall', 'caveman@caveman'], null, opts.dryRun);
      if (spawnOk(r)) ok('  removed claude plugin');
    } else {
      note('  claude plugin not installed — skipping');
    }

    // caveman-shrink MCP — only run if `claude mcp` subcommand exists. Output
    // is captured: most machines never registered the server, and the CLI
    // says so on stderr every time.
    const mcpHelp = captureSpawn('claude', ['mcp', '--help']);
    if (mcpHelp.status === 0) {
      const args = ['mcp', 'remove', 'caveman-shrink'];
      const r = opts.dryRun ? runSpawn('claude', args, null, true) : captureSpawn('claude', args);
      const said = `${r.stdout || ''}${r.stderr || ''}`.trim();
      if (!opts.dryRun && !/^No MCP server named/i.test(said)) {
        if (spawnOk(r)) ok('  removed the caveman-shrink MCP server');
        else warn(`  claude mcp remove caveman-shrink failed: ${said.split('\n')[0] || 'no output'}`);
      }
    }
  }

  // Gemini extension. Same idempotency probe as claude.
  if (hasCmd('gemini')) {
    if (geminiHasCaveman()) {
      runSpawn('gemini', ['extensions', 'uninstall', 'caveman'], null, opts.dryRun);
    } else {
      note('  gemini extension not installed — skipping');
    }
  }

  // Cursor agents + sessionStart hook. The ownership journal is the only
  // authority for which files this installer may delete; the hooks.json entry
  // goes with the hook script it names.
  try {
    const removed = CURSOR_NATIVE.uninstallCursorNative({ dryRun: opts.dryRun, note, warn });
    if (removed.hadJournal && removed.changed.length === 0) ok('  pruned owned caveman agents and hook from Cursor');
    if (removed.changed.length) cleanupFailed = true;
  } catch (error) {
    cleanupFailed = true;
    warn(`  Cursor cleanup incomplete; left integration untouched: ${error.message}`);
  }

  // Antigravity CLI plugin. agy owns its copy; same idempotency probe as gemini.
  if (hasCmd('agy')) {
    const probe = captureSpawn('agy', ['plugin', 'list']);
    if (spawnOk(probe) && /"name":\s*"caveman"/.test(probe.stdout || '')) {
      const r = runSpawn('agy', ['plugin', 'uninstall', AGY_PLUGIN_NAME], null, opts.dryRun);
      if (spawnOk(r)) ok('  removed the Antigravity CLI plugin');
      else cleanupFailed = true;
    }
  }

  // Copilot CLI sessionStart hook — same journal/digest contract.
  try {
    const removed = OWNED.uninstallOwned({ root: copilotHome(), integration: 'copilot-cli', dryRun: opts.dryRun, note, warn });
    if (removed.hadJournal && removed.changed.length === 0) ok('  removed the caveman Copilot CLI hook');
    if (removed.changed.length) cleanupFailed = true;
  } catch (error) {
    cleanupFailed = true;
    warn(`  Copilot CLI cleanup incomplete; left integration untouched: ${error.message}`);
  }

  // opencode native install — ownership journal is authority. Never infer
  // ownership from a matching path name; pre-existing user files may use it.
  const ocDir = opencodeConfigDir();
  let ocOwnership = { hadJournal: false };
  try {
    ocOwnership = OWNED.uninstallOwned({
      root: ocDir,
      integration: 'opencode',
      dryRun: opts.dryRun,
      note,
      warn,
    });
    if (ocOwnership.changed.length) cleanupFailed = true;
  } catch (error) {
    cleanupFailed = true;
    warn(`  opencode ownership journal invalid; left integration untouched: ${error.message}`);
  }
  if (ocOwnership.hadJournal) {
    const ocJson = opencodeConfigPath(ocDir);
    if (fs.existsSync(ocJson)) {
      const cfg = SETTINGS.readSettings(ocJson);
      if (cfg) {
        if (Array.isArray(cfg.plugin)) {
          cfg.plugin = cfg.plugin.filter(p => p !== OPENCODE_PLUGIN_REL);
          if (cfg.plugin.length === 0) delete cfg.plugin;
        }
        if (cfg.mcp && typeof cfg.mcp === 'object' && cfg.mcp['caveman-shrink']) {
          delete cfg.mcp['caveman-shrink'];
          if (Object.keys(cfg.mcp).length === 0) delete cfg.mcp;
        }
        // Install backs up a config that was already there, so an emptied one
        // without a backup is the one install created: it goes too.
        const created = Object.keys(cfg).length === 0 && !fs.existsSync(ocJson + '.bak');
        if (!opts.dryRun) {
          if (created) fs.unlinkSync(ocJson);
          else SETTINGS.writeSettings(ocJson, cfg);
        }
        ok(`  pruned caveman entries from ${ocJson}`);
      }
    }
    // AGENTS.md — strip the fenced caveman block (preserves user content
    // above and below). If the file is empty after the strip, remove it.
    // Falls back to legacy unfenced-sentinel handling for installs that
    // pre-date the marker fence.
    const ocAgentsMd = path.join(ocDir, 'AGENTS.md');
    if (fs.existsSync(ocAgentsMd)) {
      const body = fs.readFileSync(ocAgentsMd, 'utf8');
      const begin = body.indexOf(OPENCODE_AGENTS_MD_BEGIN);
      const end = body.indexOf(OPENCODE_AGENTS_MD_END);
      if (begin !== -1 && end !== -1 && end > begin) {
        // One line ending kept on each side of the cut, LF or CRLF.
        const before = body.slice(0, begin).replace(/(\r?\n)+$/, '$1');
        const after = body.slice(end + OPENCODE_AGENTS_MD_END.length).replace(/^(\r?\n)+/, '$1');
        let next = (before + after).trimEnd();
        next = next ? next + (body.includes('\r\n') ? '\r\n' : '\n') : '';
        if (!opts.dryRun) {
          if (next === '') {
            try { fs.unlinkSync(ocAgentsMd); } catch (_) {}
          } else {
            fs.writeFileSync(ocAgentsMd, next, { mode: 0o644 });
          }
        }
        note(next === '' ? `  removed ${ocAgentsMd}` : `  stripped caveman block from ${ocAgentsMd}`);
      } else if (body.includes(OPENCODE_AGENTS_MD_SENTINEL)) {
        // Legacy install (no marker fence). Remove only if the file is ours.
        if (body.trim() === '' || body.trim().startsWith(OPENCODE_AGENTS_MD_SENTINEL)) {
          if (!opts.dryRun) { try { fs.unlinkSync(ocAgentsMd); } catch (_) {} }
          note(`  removed ${ocAgentsMd}`);
        } else {
          note(`  left ${ocAgentsMd} in place (legacy mixed content — strip caveman block manually)`);
        }
      }
    }
    // opencode mode state. Both files, matching the Claude-side stateFiles
    // sweep below: the plugin writes `.prev` for the one-shot restore, and a
    // stale one is not inert — a reinstall's first /caveman-commit would read
    // it as that session's return target.
    for (const name of ['.caveman-active', '.caveman-active.prev']) {
      const ocFlag = path.join(ocDir, name);
      if (fs.existsSync(ocFlag) && !opts.dryRun) { try { fs.unlinkSync(ocFlag); } catch (_) {} }
    }
    OWNED.removeEmptyDirs(ocOwnership.createdDirs || []);
  }

  // OpenClaw native install — strip skill folder + SOUL.md marker block.
  // Probed by the skill folder we own; if absent, skip silently.
  const ocwWs = process.env.OPENCLAW_WORKSPACE || path.join(os.homedir(), '.openclaw', 'workspace');
  if (fs.existsSync(path.join(ocwWs, 'skills', 'caveman')) || fs.existsSync(path.join(ocwWs, 'SOUL.md'))) {
    const log = {
      write: (s) => process.stdout.write(s),
      note: (s) => note(s),
      warn: (s) => warn(s),
    };
    try {
      const r = OPENCLAW.uninstallOpenclaw({ workspace: ocwWs, dryRun: opts.dryRun, log });
      if (r.touched) ok('  pruned caveman entries from OpenClaw workspace');
    } catch (error) {
      cleanupFailed = true;
      warn(`  OpenClaw cleanup failed; continuing other cleanup: ${error.message}`);
    }
  }

  // Only deregister an unchanged package this installer owns. The shared
  // cleanup keeps both journal and payload if OMP refuses deregistration.
  const ompDir = ompPluginDir();
  try {
    const root = path.dirname(ompDir);
    assertOmpRoot(root);
    const removed = OWNED.uninstallOwned({
      root, integration: 'omp', dryRun: opts.dryRun, note, warn,
      unregister: () => {
        if (!hasCmd('omp')) throw new Error('omp is unavailable; keeping registered package');
        const registration = inspectOmpRegistration(ompDir);
        if (!registration.linked && !registration.registered) return;
        if (!registration.registered) throw new Error('OMP link exists without registration; repair it through OMP before uninstalling');
        const result = runSpawn('omp', ['plugin', 'uninstall', OMP_PLUGIN_NAME], null, false);
        if (!spawnOk(result)) throw new Error('omp plugin uninstall failed; keeping registered package');
      },
    });
    if (!removed.hadJournal && fs.existsSync(ompDir)) note(`  left unowned ${ompDir}`);
    if (removed.changed.length) cleanupFailed = true;
  } catch (error) {
    cleanupFailed = true;
    warn(`  OMP cleanup incomplete: ${error.message}`);
  }

  // Hermes native install — same journal/digest contract as opencode.
  const hermesRoot = path.join(hermesConfigDir(), 'productivity');
  try {
    const hermesOwnership = OWNED.uninstallOwned({
      root: hermesRoot,
      integration: 'hermes',
      dryRun: opts.dryRun,
      note,
      warn,
    });
    if (hermesOwnership.hadJournal && hermesOwnership.changed.length === 0) ok('  pruned owned caveman skills from Hermes');
    if (hermesOwnership.changed.length) cleanupFailed = true;
  } catch (error) {
    cleanupFailed = true;
    warn(`  Hermes ownership journal invalid; left integration untouched: ${error.message}`);
  }

  let grokSkillDirs = [];
  for (const prov of PROVIDERS.filter(prov => PROVIDER_SKILLS.usesNativeSkills(prov.id))) {
    try {
      const removed = PROVIDER_SKILLS.uninstall({ provider: prov.id, dryRun: opts.dryRun, note, warn });
      if (prov.id === 'grok') grokSkillDirs = removed.createdDirs || [];
      if (removed.hadJournal && removed.changed.length === 0) ok(`  pruned owned caveman skills from ${prov.label}`);
      if (removed.changed.length) cleanupFailed = true;
    } catch (error) {
      cleanupFailed = true;
      warn(`  ${prov.label} ownership cleanup failed; left integration untouched: ${error.message}`);
    }
  }

  // Codex always-on hook. Unmerge first: hooks.json must never point at a
  // removed payload, so an unreadable or unwritable file keeps the payload.
  const cxHome = codexHome();
  const cxHooks = path.join(cxHome, 'hooks.json');
  let cxClean = true;
  // The entry is written only after the payload journal, so no journal means
  // caveman never installed the hook: leave a user's hooks.json alone, even broken.
  if (fs.existsSync(OWNED.journalPaths(cxHome, 'codex-hooks').journalPath) && fs.existsSync(cxHooks)) {
    const doc = SETTINGS.readSettings(cxHooks);
    if (!doc || typeof doc !== 'object' || Array.isArray(doc)) {
      cxClean = false;
      cleanupFailed = true;
      warn(`  could not parse ${cxHooks} — leaving the caveman Codex hook in place.`);
    } else if (stripCodexHook(doc, codexHookScript(cxHome)) !== -1) {
      try {
        if (opts.dryRun) note(`  would remove the caveman SessionStart entry from ${cxHooks}`);
        else if (Object.keys(doc).length === 0) {
          fs.unlinkSync(cxHooks);
          note(`  removed ${cxHooks}`);
        } else {
          SETTINGS.validateHookFields(doc);
          SETTINGS.writeSettings(cxHooks, doc);
          note(`  removed the caveman SessionStart entry from ${cxHooks}`);
        }
      } catch (error) {
        cxClean = false;
        cleanupFailed = true;
        warn(`  could not update ${cxHooks}: ${error.message}`);
      }
    }
  }
  if (cxClean) {
    try {
      const removed = OWNED.uninstallOwned({ root: cxHome, integration: 'codex-hooks', dryRun: opts.dryRun, note, warn });
      if (removed.changed.length) cleanupFailed = true;
    } catch (error) {
      cleanupFailed = true;
      warn(`  Codex hook payload cleanup failed: ${error.message}`);
    }
  }

  // Grok Build always-on block. The marker fence is the ownership signal;
  // user text around it stays.
  const grokAgentsMd = grokAgentsMdPath();
  if (fs.existsSync(grokAgentsMd)) {
    try {
      if (opts.dryRun) {
        if (fs.readFileSync(grokAgentsMd, 'utf8').includes(OPENCLAW.MARK_BEGIN)) note(`  would strip caveman block from ${grokAgentsMd}`);
      } else {
        const r = OPENCLAW.stripBootstrapFromSoul(grokAgentsMd);
        if (r.changed) note(r.removed ? `  removed ${grokAgentsMd}` : `  stripped caveman block from ${grokAgentsMd}`);
      }
    } catch (error) {
      cleanupFailed = true;
      warn(`  could not strip caveman block from ${grokAgentsMd}: ${error.message}`);
    }
  }
  // GROK_HOME itself goes once the AGENTS.md beside the skills is gone too.
  OWNED.removeEmptyDirs(grokSkillDirs);

  // Per-session state. Keep lifetime savings history unless user removes it.
  const stateFiles = [
    '.caveman-active',
    '.caveman-active.prev',
    '.caveman-mode-log.jsonl',
    '.caveman-statusline-suffix',
    '.caveman-nudge-shown',
    '.caveman-statusline-stale',
  ];
  for (const file of stateFiles) {
    const statePath = path.join(configDir, file);
    if (!fs.existsSync(statePath)) continue;
    if (opts.dryRun) {
      note(`  would remove ${statePath}`);
    } else {
      try { fs.unlinkSync(statePath); } catch (_) {}
      note(`  removed ${statePath}`);
    }
  }
  // Per-session mode files (one <session_id>.mode / .prev per window). Keep in
  // sync with the uninstall blocks in src/hooks/uninstall.sh and uninstall.ps1.
  const sessionsDir = path.join(configDir, '.caveman-sessions');
  if (fs.existsSync(sessionsDir)) {
    if (opts.dryRun) {
      note(`  would remove ${sessionsDir}`);
    } else {
      try {
        const sessionsState = fs.lstatSync(sessionsDir);
        if (sessionsState.isDirectory() && !sessionsState.isSymbolicLink()) {
          fs.rmSync(sessionsDir, { recursive: true, force: true });
        } else {
          // Never recurse through a path swapped for a link or special file.
          fs.unlinkSync(sessionsDir);
        }
        note(`  removed ${sessionsDir}`);
      } catch (error) {
        cleanupFailed = true;
        warn(`  could not remove ${sessionsDir}: ${error.message}`);
      }
    }
  }
  const historyPath = path.join(configDir, '.caveman-history.jsonl');
  if (fs.existsSync(historyPath)) {
    note(`  kept ${historyPath} (lifetime history — delete manually if unwanted)`);
  }

  process.stdout.write('\n');
  if (cleanupFailed) {
    warn('uninstall incomplete. Review cleanup warnings above.');
  } else {
    ok('uninstall done.');
  }
  // Not run for the user: `skills remove` deletes same-named skill folders in
  // every agent's directory, and generic names (migration, lean-build) can be
  // the user's own. Run by hand it lists the names and asks first.
  ok('skills added by `npx skills` (Cursor/Windsurf/Cline/etc.) stay. To remove them:');
  ok('  npx skills remove JuliusBrussee/caveman -g   (lists the skills and asks first)');
  ok('per-repo init files (.cursor/, .windsurf/, AGENTS.md) — remove with your editor');
  return cleanupFailed ? 1 : 0;
}

// ── Interactive prompt (TTY-only) ─────────────────────────────────────────
async function promptForOnly(detected) {
  if (!process.stdin.isTTY || !process.stdout.isTTY) return null;
  if (detected.length === 0) return null;
  process.stdout.write('\nDetected agents:\n');
  detected.forEach((p, i) => process.stdout.write(`  [${i + 1}] ${p.label}\n`));
  process.stdout.write('  [a] all   [q] quit\n');
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const ans = await new Promise(res => rl.question('Install which? (default: all) ', res));
  rl.close();
  const t = (ans || '').trim().toLowerCase();
  if (t === 'q') process.exit(0);
  if (t === '' || t === 'a' || t === 'all') return null;
  const picks = t.split(/[\s,]+/).map(s => parseInt(s, 10)).filter(n => n >= 1 && n <= detected.length);
  if (picks.length === 0) return null;
  return picks.map(n => detected[n - 1].id);
}

// ── --list ─────────────────────────────────────────────────────────────────
function printList(noColor) {
  const c = makeChalk(noColor);
  process.stdout.write(c.orange('🪨 caveman provider matrix') + '\n\n');
  process.stdout.write(`  ${pad('ID', 15)} ${pad('AGENT', 22)} INSTALL MECHANISM\n`);
  process.stdout.write(`  ${pad('--', 15)} ${pad('-----', 22)} -----------------\n`);
  for (const p of PROVIDERS) {
    const tag = p.soft ? ' (soft)' : '';
    process.stdout.write(`  ${pad(p.id, 15)} ${pad(p.label, 22)} ${p.mech}${tag}\n`);
  }
  process.stdout.write('\n');
  process.stdout.write(c.dim('  Defaults: --with-hooks ON, --with-init OFF, --with-mcp-shrink OFF.\n'));
  process.stdout.write(c.dim('  --all = hooks + init (mcp-shrink needs an upstream — opt in explicitly).\n'));
  process.stdout.write(c.dim('  --minimal turns hooks + init + mcp-shrink off.\n'));
}

function pad(s, n) { s = String(s); return s + ' '.repeat(Math.max(0, n - s.length)); }

// ── Help ───────────────────────────────────────────────────────────────────
function printHelp() {
  process.stdout.write(`caveman installer — detects your agents and installs caveman for each one.

USAGE
  npx -y github:JuliusBrussee/caveman -- [flags]
  node installer/install.js [flags]
  bash install.sh [flags]              # shim → npx
  pwsh install.ps1 [flags]             # shim → npx

FLAGS
  --dry-run             Print what would run, do nothing.
  --force               Re-run even if a target reports already installed.
  --only <agent>        Install only for the named agent. Repeatable.
                        See --list for valid ids.
  --all                 Turn on hooks + init. (mcp-shrink needs an upstream;
                        pass --with-mcp-shrink="<cmd>" to add it.)
  --minimal             Just the plugin/extension install.
  --with-hooks          Claude Code: install SessionStart/SubagentStart/
                        UserPromptSubmit/SessionEnd hooks + statusline badge.
                        Codex: SessionStart hook in \$CODEX_HOME/hooks.json.
                        Cursor sessionStart hook, Copilot CLI session hook.
                        (Default ON.)
  --no-hooks            Skip the hooks installer.
  --with-init           Write per-repo IDE rule files into \$PWD.
  --with-mcp-shrink="<upstream cmd>"
                        Claude Code (and opencode): register caveman-shrink MCP
                        proxy wrapping the given upstream. Default OFF.
                        caveman-shrink crashes without an upstream, so a value
                        is required. Quotes group paths containing spaces;
                        backslashes stay literal. A JSON argv array also works.
                        Example: --with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /tmp"
  --no-mcp-shrink       Skip MCP shrink. (Default.)
  --uninstall, -u       Remove caveman from this machine.
  --config-dir <path>   Claude Code config dir for hook files + settings.json.
                        Default: \$CLAUDE_CONFIG_DIR or ~/.claude. Does NOT
                        scope \`claude plugin install\`, \`gemini extensions
                        install\`, Codex (CODEX_HOME), OMP (~/.omp/), opencode
                        (XDG_CONFIG_HOME), or openclaw (OPENCLAW_WORKSPACE) —
                        those use their own paths.
  --non-interactive     Never prompt; use defaults. (Auto when stdin is not a TTY.)
  --list                Print provider matrix and exit.
  --no-color            Disable ANSI colors.
  -h, --help            Show this help.

EXAMPLES
  npx -y github:JuliusBrussee/caveman                        # default install
  npx -y github:JuliusBrussee/caveman -- --all               # all the trimmings
  npx -y github:JuliusBrussee/caveman -- --only claude --no-mcp-shrink
  npx -y github:JuliusBrussee/caveman -- --uninstall

  Issues: https://github.com/${REPO}/issues
`);
}

// ── Main ───────────────────────────────────────────────────────────────────
async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const c = makeChalk(opts.noColor);
  if (opts.help) { printHelp(); return 0; }
  if (opts.listOnly) { printList(opts.noColor); return 0; }

  checkWslWindowsNode();
  checkNodeVersion();

  const configDir = opts.configDir || process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
  const repoRoot = detectRepoRoot();

  const ctx = {
    opts, configDir, repoRoot,
    say:  (s) => process.stdout.write(c.orange(s) + '\n'),
    note: (s) => process.stdout.write(c.dim(s) + '\n'),
    warn: (s) => process.stderr.write(c.red(s) + '\n'),
    ok:   (s) => process.stdout.write(c.green(s) + '\n'),
    results: { installed: [], skipped: [], failed: [], detected: 0 },
  };

  if (opts.uninstall) return uninstall(ctx);

  ctx.say('🪨 caveman installer');
  ctx.note(`  ${REPO}`);
  if (opts.dryRun) ctx.note('  (dry run — nothing will be written)');
  process.stdout.write('\n');

  // Detect everything once
  const detected = PROVIDERS.filter(p => detectMatch(p.detect));

  // TTY-only multi-select prompt when no --only and no --non-interactive.
  if (opts.only.length === 0 && !opts.nonInteractive) {
    const picks = await promptForOnly(detected);
    if (picks) opts.only = picks;
  }

  const want = (id) => opts.only.length === 0 || opts.only.includes(id);
  const explicit = (id) => opts.only.includes(id);

  // Run installs in declared order. Soft providers (no reliable detect probe)
  // are auto-skipped — user must opt in via `--only <id>`. Stops the installer
  // from firing `npx skills add ...` against agents the user never installed
  // just because some other tool created `~/.foo` along the way.
  for (const prov of PROVIDERS) {
    if (!want(prov.id)) continue;
    if (prov.soft && !explicit(prov.id)) continue;
    // Auto-detect mode: skip providers we can't see. With --only <id> the user
    // is explicitly opting in, so trust them and let the per-provider installer
    // bail itself if its preconditions aren't met (e.g. opencode bails when
    // no repo clone is available; openclaw bails when the workspace dir is
    // missing without --force).
    if (!explicit(prov.id) && !detectMatch(prov.detect)) continue;
    if (prov.id === 'claude')   { await installClaude(ctx); continue; }
    if (prov.id === 'gemini')   { installGemini(ctx); continue; }
    if (prov.id === 'opencode') { installOpencode(ctx); continue; }
    if (prov.id === 'omp')      { installOmp(ctx); continue; }
    if (prov.id === 'openclaw') { installOpenclaw(ctx); continue; }
    if (prov.id === 'hermes')   { installHermes(ctx); continue; }
    if (prov.id === 'antigravity-cli') { installAntigravityCli(ctx); continue; }
    if (prov.profile || PROVIDER_SKILLS.usesNativeSkills(prov.id)) { installViaSkills(ctx, prov); continue; }
  }

  // No detected target means no skill installation. Upstream --all (and --yes
  // with no detected agents) installs into every profile, contradicting our
  // promise to skip agents the user does not have. --only remains the explicit
  // way to select an agent that cannot be detected.

  // Per-repo init
  if (opts.withInit) {
    ctx.say(`→ writing per-repo IDE rule files into ${process.cwd()} (--with-init)`);
    if (await runInit(ctx)) ctx.results.installed.push(`caveman-init (${process.cwd()})`);
    else                    ctx.results.failed.push(['caveman-init', 'src/tools/caveman-init.js failed']);
    process.stdout.write('\n');
  } else if (ctx.results.installed.length || ctx.results.skipped.length) {
    ctx.note('  tip: re-run inside a repo with --all (or --with-init) to also write per-repo');
    ctx.note('       Cursor/Windsurf/Cline/Copilot/AGENTS.md rule files.');
  }

  // Summary
  process.stdout.write('\n');
  ctx.say('🪨 done');
  if (ctx.results.installed.length) {
    ctx.ok('  installed:');
    for (const a of ctx.results.installed) process.stdout.write(`    • ${a}\n`);
  }
  if (ctx.results.skipped.length) {
    process.stdout.write('  skipped:\n');
    for (const [id, why] of ctx.results.skipped) process.stdout.write(`    • ${id} — ${why}\n`);
  }
  if (ctx.results.failed.length) {
    ctx.warn('  failed:');
    for (const [id, why] of ctx.results.failed) process.stderr.write(`    • ${id} — ${why}\n`);
  }
  if (!ctx.results.installed.length && !ctx.results.skipped.length && !ctx.results.failed.length) {
    process.stdout.write('  no agents detected. run with --list to see all 30+ supported agents,\n');
    process.stdout.write('  or pass --only <agent> to force a specific target.\n');
  }
  process.stdout.write('\n');
  ctx.note("  start any session and say 'caveman mode', or run /caveman in Claude Code");
  ctx.note('  measure what caveman save you: run /caveman-stats');
  ctx.note('  verified team savings coming soon — join waitlist: https://caveman.so');
  ctx.note(`  uninstall: npx -y github:${REPO} -- --uninstall`);

  // Exit code: nonzero only if every detected agent failed
  if (ctx.results.detected > 0 && !ctx.results.installed.length && !ctx.results.skipped.length) return 1;
  return 0;
}

main().then(code => process.exit(code || 0))
      .catch(err => { process.stderr.write((err && err.stack || String(err)) + '\n'); process.exit(1); });
