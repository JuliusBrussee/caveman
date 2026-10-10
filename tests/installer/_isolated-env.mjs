// Env for running installer/install.js against a throwaway home.
//
// Uninstall walks every agent's home and runs whatever `caveman`, `claude`,
// `gemini`, `agy` and `omp` it finds on PATH. Spread from process.env alone,
// a test run removes the developer's own plugin, extension and native routing.
// So every home the installer reads is pointed into `home` (set, not just
// dropped, so a caller spreading process.env first cannot bring one back), and
// PATH holds only `binDirs` plus the system directories. CLAUDE_CONFIG_DIR is
// dropped: callers pass --config-dir and set it to match.

import path from 'node:path';

export function isolatedEnv(home, binDirs = []) {
  const win = process.platform === 'win32';
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !['PATH', 'CLAUDE_CONFIG_DIR'].includes(key.toUpperCase())));
  const system = win ? [path.dirname(process.execPath)] : ['/usr/bin', '/bin'];
  return {
    ...env,
    HOME: home,
    USERPROFILE: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    XDG_DATA_HOME: path.join(home, '.local', 'share'),
    CODEX_HOME: path.join(home, '.codex'),
    COPILOT_HOME: path.join(home, '.copilot'),
    GEMINI_CLI_HOME: home,
    GROK_HOME: path.join(home, '.grok'),
    HERMES_HOME: path.join(home, '.hermes'),
    OPENCLAW_WORKSPACE: path.join(home, '.openclaw', 'workspace'),
    CAVEMAN_HOME: path.join(home, '.caveman'),
    AIDER_DESK_DIR: path.join(home, '.aider-desk'),
    AIDER_DESK_HOME_DIR: path.join(home, '.aider-desk'),
    CONTINUE_GLOBAL_DIR: path.join(home, '.continue'),
    CRUSH_SKILLS_DIR: path.join(home, '.config', 'crush', 'skills'),
    IFLOW_HOME: path.join(home, '.iflow'),
    NO_COLOR: '1',
    PATH: [...binDirs, ...system].join(win ? ';' : ':'),
  };
}
