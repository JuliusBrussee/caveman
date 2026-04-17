#!/usr/bin/env node
// caveman — shared configuration resolver
//
// Resolution order for default mode:
//   1. CAVEMAN_DEFAULT_MODE environment variable
//   2. Per-project config: $CLAUDE_PROJECT_DIR/.claude/caveman.local.md
//      (YAML frontmatter with `defaultMode` field)
//   3. Global config file defaultMode field:
//      - $XDG_CONFIG_HOME/caveman/config.json (any platform, if set)
//      - ~/.config/caveman/config.json (macOS / Linux fallback)
//      - %APPDATA%\caveman\config.json (Windows fallback)
//   4. 'full'

const fs = require('fs');
const path = require('path');
const os = require('os');

const VALID_MODES = [
  'off', 'lite', 'full', 'ultra',
  'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra',
  'commit', 'review', 'compress'
];

function getConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'caveman');
  }
  if (process.platform === 'win32') {
    return path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'caveman'
    );
  }
  return path.join(os.homedir(), '.config', 'caveman');
}

function getConfigPath() {
  return path.join(getConfigDir(), 'config.json');
}

// Per-project settings path. Follows the Claude Code plugin convention
// of `.claude/<plugin-name>.local.md`. $CLAUDE_PROJECT_DIR is set by Claude
// Code for every hook invocation — falls back to process.cwd() for direct
// CLI invocation or tests.
function getProjectConfigPath() {
  const projectDir = process.env.CLAUDE_PROJECT_DIR || process.cwd();
  if (!projectDir) return null;
  return path.join(projectDir, '.claude', 'caveman.local.md');
}

// Read `defaultMode` from YAML frontmatter in the project config file.
// Hard-capped at 4096 bytes to limit attack surface; rejects symlinks for
// the same reason as readFlag (local attacker could redirect to ~/.ssh/id_rsa
// etc.). Returns a validated mode string or null.
//
// Only the `defaultMode:` line is parsed — no full YAML parser is needed
// because the plugin has zero npm dependencies and we deliberately keep
// the config surface minimal. Markdown body is ignored.
const MAX_PROJECT_CONFIG_BYTES = 4096;

function readProjectDefaultMode() {
  const projectPath = getProjectConfigPath();
  if (!projectPath) return null;

  try {
    let st;
    try {
      st = fs.lstatSync(projectPath);
    } catch (e) {
      return null;
    }
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (st.size > MAX_PROJECT_CONFIG_BYTES) return null;

    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let content;
    try {
      fd = fs.openSync(projectPath, flags);
      const buf = Buffer.alloc(MAX_PROJECT_CONFIG_BYTES);
      const n = fs.readSync(fd, buf, 0, MAX_PROJECT_CONFIG_BYTES, 0);
      content = buf.slice(0, n).toString('utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }

    // Extract YAML frontmatter block: content between opening --- and closing ---.
    const fmMatch = content.match(/^---\r?\n([\s\S]*?)\r?\n---/);
    if (!fmMatch) return null;
    const frontmatter = fmMatch[1];

    // Extract `defaultMode:` line. Strip surrounding quotes if present.
    const fieldMatch = frontmatter.match(/^\s*defaultMode\s*:\s*(.+?)\s*$/m);
    if (!fieldMatch) return null;
    let value = fieldMatch[1].trim();
    value = value.replace(/^(["'])(.*)\1$/, '$2');

    const mode = value.toLowerCase();
    if (!VALID_MODES.includes(mode)) return null;
    return mode;
  } catch (e) {
    return null;
  }
}

function getDefaultMode() {
  // 1. Environment variable (highest priority — session override)
  const envMode = process.env.CAVEMAN_DEFAULT_MODE;
  if (envMode && VALID_MODES.includes(envMode.toLowerCase())) {
    return envMode.toLowerCase();
  }

  // 2. Per-project config at $CLAUDE_PROJECT_DIR/.claude/caveman.local.md
  const projectMode = readProjectDefaultMode();
  if (projectMode) {
    return projectMode;
  }

  // 3. Global config file
  try {
    const configPath = getConfigPath();
    const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
    if (config.defaultMode && VALID_MODES.includes(config.defaultMode.toLowerCase())) {
      return config.defaultMode.toLowerCase();
    }
  } catch (e) {
    // Config file doesn't exist or is invalid — fall through
  }

  // 4. Default
  return 'full';
}

// Symlink-safe flag file write.
// Refuses symlinks at the target file and at the immediate parent directory,
// uses O_NOFOLLOW where available, writes atomically via temp + rename with
// 0600 permissions. Protects against local attackers replacing the predictable
// flag path (~/.claude/.caveman-active) with a symlink to clobber other files.
//
// Does NOT walk the full ancestor chain — macOS has /tmp -> /private/tmp and
// many legitimate setups route through symlinked home dirs, so a full walk
// produces false positives. The attack surface requires write access to the
// immediate parent, which is what we check.
//
// Silent-fails on any filesystem error — the flag is best-effort.
function safeWriteFlag(flagPath, content) {
  try {
    const flagDir = path.dirname(flagPath);
    fs.mkdirSync(flagDir, { recursive: true });

    // Refuse if the parent directory itself is a symlink (attacker redirect).
    try {
      if (fs.lstatSync(flagDir).isSymbolicLink()) return;
    } catch (e) {
      return;
    }

    // Refuse if the target already exists as a symlink.
    try {
      if (fs.lstatSync(flagPath).isSymbolicLink()) return;
    } catch (e) {
      if (e.code !== 'ENOENT') return;
    }

    const tempPath = path.join(flagDir, `.caveman-active.${process.pid}.${Date.now()}`);
    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | O_NOFOLLOW;
    let fd;
    try {
      fd = fs.openSync(tempPath, flags, 0o600);
      fs.writeSync(fd, String(content));
      try { fs.fchmodSync(fd, 0o600); } catch (e) { /* best-effort on Windows */ }
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    fs.renameSync(tempPath, flagPath);
  } catch (e) {
    // Silent fail — flag is best-effort
  }
}

// Symlink-safe, size-capped, whitelist-validated flag file read.
// Symmetric with safeWriteFlag: refuses symlinks at the target, caps the read,
// and rejects anything that isn't a known mode. Returns null on any anomaly.
//
// Without this, a local attacker with write access to ~/.claude/ could replace
// the flag with a symlink to ~/.ssh/id_rsa (or any user-readable secret). Every
// reader — statusline, per-turn reinforcement — would slurp that content and
// either echo it to the terminal or inject it into model context.
//
// MAX_FLAG_BYTES is a hard cap. The longest legitimate value is "wenyan-ultra"
// (12 bytes); 64 leaves slack without enabling exfil.
const MAX_FLAG_BYTES = 64;

function readFlag(flagPath) {
  try {
    let st;
    try {
      st = fs.lstatSync(flagPath);
    } catch (e) {
      return null;
    }
    if (st.isSymbolicLink() || !st.isFile()) return null;
    if (st.size > MAX_FLAG_BYTES) return null;

    const O_NOFOLLOW = typeof fs.constants.O_NOFOLLOW === 'number' ? fs.constants.O_NOFOLLOW : 0;
    const flags = fs.constants.O_RDONLY | O_NOFOLLOW;
    let fd;
    let out;
    try {
      fd = fs.openSync(flagPath, flags);
      const buf = Buffer.alloc(MAX_FLAG_BYTES);
      const n = fs.readSync(fd, buf, 0, MAX_FLAG_BYTES, 0);
      out = buf.slice(0, n).toString('utf8');
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }

    const raw = out.trim().toLowerCase();
    if (!VALID_MODES.includes(raw)) return null;
    return raw;
  } catch (e) {
    return null;
  }
}

module.exports = {
  getDefaultMode,
  getConfigDir,
  getConfigPath,
  getProjectConfigPath,
  readProjectDefaultMode,
  VALID_MODES,
  safeWriteFlag,
  readFlag,
};
