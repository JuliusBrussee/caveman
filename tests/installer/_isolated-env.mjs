// Env for running installer/install.js against a throwaway home.
//
// Uninstall walks every agent's home and runs whatever `caveman`, `claude`,
// `gemini`, `agy` and `omp` it finds on PATH. Spread from process.env alone,
// a test run removes the developer's own plugin, extension and native routing.
// So every home override the installer reads is dropped (HOME stands in), and
// PATH holds only `binDirs` plus the system directories.

import path from 'node:path';

const HOME_OVERRIDES = new Set([
  'CLAUDE_CONFIG_DIR', 'CODEX_HOME', 'COPILOT_HOME', 'GEMINI_CLI_HOME', 'GROK_HOME',
  'HERMES_HOME', 'OPENCLAW_WORKSPACE', 'CAVEMAN_HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME',
  'AIDER_DESK_DIR', 'AIDER_DESK_HOME_DIR', 'CONTINUE_GLOBAL_DIR', 'CRUSH_SKILLS_DIR', 'IFLOW_HOME',
  'PATH',
]);

export function isolatedEnv(home, binDirs = []) {
  const win = process.platform === 'win32';
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !HOME_OVERRIDES.has(key.toUpperCase())));
  const system = win ? [path.dirname(process.execPath)] : ['/usr/bin', '/bin'];
  return {
    ...env,
    HOME: home,
    USERPROFILE: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    NO_COLOR: '1',
    PATH: [...binDirs, ...system].join(win ? ';' : ':'),
  };
}
