#!/usr/bin/env node
// caveman — Copilot CLI extension
//
// Native extension providing full caveman mode support in GitHub Copilot CLI.
// Feature parity with Claude Code hooks: auto-activation, mode switching,
// intensity levels, one-shot skills, stop/deactivate.
//
// Discovery: Copilot CLI auto-discovers .github/extensions/caveman/extension.mjs
// SDK: @github/copilot-sdk/extension (auto-resolved, no npm install needed)

import { joinSession } from '@github/copilot-sdk/extension';
import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import configModule from './config.js';

import {
  parseSlashCommand,
  isStopCommand,
  VALID_MODES,
  ONE_SHOT_SKILLS,
  INDEPENDENT_MODES,
} from './parser.mjs';

import {
  CAVEMAN_SKILL,
  CAVEMAN_COMMIT_SKILL,
  CAVEMAN_REVIEW_SKILL,
  CAVEMAN_COMPRESS_SKILL,
  CAVEMAN_HELP_SKILL,
} from './rules.mjs';

const { getDefaultMode } = configModule;

// ---------------------------------------------------------------------------
// SKILL.md loading — dev override + bundled fallback
// ---------------------------------------------------------------------------

const __dirname = dirname(fileURLToPath(import.meta.url));

/** Try to read a workspace SKILL.md (dev mode in caveman repo). */
function tryReadWorkspaceSkill(workspacePath, relativePath) {
  try {
    if (workspacePath) {
      const p = join(workspacePath, relativePath);
      if (existsSync(p)) {
        const raw = readFileSync(p, 'utf8');
        // Strip YAML frontmatter
        return raw.replace(/^---[\s\S]*?---\s*/, '');
      }
    }
  } catch (_) {}
  return null;
}

/** Get rules for a given skill, preferring workspace over bundled. */
function getSkillRules(workspacePath, skill) {
  const paths = {
    caveman: 'skills/caveman/SKILL.md',
    commit: 'skills/caveman-commit/SKILL.md',
    review: 'skills/caveman-review/SKILL.md',
    compress: 'caveman-compress/SKILL.md',
    help: 'skills/caveman-help/SKILL.md',
  };
  const bundled = {
    caveman: CAVEMAN_SKILL,
    commit: CAVEMAN_COMMIT_SKILL,
    review: CAVEMAN_REVIEW_SKILL,
    compress: CAVEMAN_COMPRESS_SKILL,
    help: CAVEMAN_HELP_SKILL,
  };

  if (paths[skill]) {
    return tryReadWorkspaceSkill(workspacePath, paths[skill]) || bundled[skill] || '';
  }
  return bundled[skill] || '';
}

// ---------------------------------------------------------------------------
// Intensity filter — extracts only the active level from full rules
// ---------------------------------------------------------------------------

function filterRulesToLevel(fullRules, modeLabel) {
  return fullRules.split('\n').reduce((acc, line) => {
    // Intensity table rows start with | **level** |
    const tableRowMatch = line.match(/^\|\s*\*\*(\S+?)\*\*\s*\|/);
    if (tableRowMatch) {
      if (tableRowMatch[1] === modeLabel) acc.push(line);
      return acc;
    }

    // Example lines start with "- level:"
    const exampleMatch = line.match(/^- (\S+?):\s/);
    if (exampleMatch) {
      if (exampleMatch[1] === modeLabel) acc.push(line);
      return acc;
    }

    acc.push(line);
    return acc;
  }, []).join('\n');
}

// ---------------------------------------------------------------------------
// State — module-level, reset on /clear (extension reload)
// ---------------------------------------------------------------------------

// undefined = not yet initialized (lazy reinit after /clear)
// null = explicitly deactivated (stop caveman)
// string = active mode name
let currentMode = undefined;
let currentWorkspacePath = undefined;

// Cache filtered rules per level to avoid re-parsing
const rulesCache = new Map();
let pendingOneShotSkill = null;
let lastHandledSlashPrompt = null;

function shouldRegisterSlashCommands() {
  try {
    const projectExtensionDir = join(process.cwd(), '.github', 'extensions', 'caveman');
    const projectExtensionPath = join(projectExtensionDir, 'extension.mjs');
    if (existsSync(projectExtensionPath)) {
      return __dirname === projectExtensionDir;
    }
  } catch (_) {
    // Fall through to permissive default
  }
  return true;
}

function getFilteredRules(workspacePath, mode) {
  const modeLabel = mode === 'wenyan' ? 'wenyan-full' : mode;

  if (rulesCache.has(modeLabel)) return rulesCache.get(modeLabel);

  const fullRules = getSkillRules(workspacePath, 'caveman');
  const filtered = filterRulesToLevel(fullRules, modeLabel);
  const result = `CAVEMAN MODE ACTIVE — level: ${modeLabel}\n\n${filtered}`;
  rulesCache.set(modeLabel, result);
  return result;
}

function initializeMode() {
  const defaultMode = getDefaultMode();

  if (defaultMode === 'off') {
    currentMode = null;
    return { mode: null, configuredDefault: defaultMode };
  }

  if (INDEPENDENT_MODES.includes(defaultMode)) {
    currentMode = 'full';
    return { mode: currentMode, configuredDefault: defaultMode };
  }

  currentMode = defaultMode;
  return { mode: currentMode, configuredDefault: defaultMode };
}

function stripOneShotPrefix(prompt) {
  return prompt.replace(
    /^(?:\/caveman[-:]\w+|caveman-(?:commit|review|compress|help)|caveman\s+(?:commit|review|compress|help))\s*/i,
    ''
  ).trim();
}

function applyModeSwitch(mode) {
  pendingOneShotSkill = null;

  if (mode === 'off') {
    currentMode = null;
    return '[CAVEMAN] Disabled. Next prompt responds normally.';
  }

  currentMode = mode;
  return `[CAVEMAN] Switched to ${currentMode} mode. Next prompt uses caveman ${currentMode}.`;
}

function buildModeSwitchResponse(mode, cwd) {
  const logMessage = applyModeSwitch(mode);
  session.log(logMessage, { level: 'info' });

  if (mode === 'off') {
    return {
      additionalContext: 'Caveman mode OFF. Respond in normal prose from this turn onward.',
      modifiedPrompt: 'Caveman mode deactivated. Respond normally from now on.',
    };
  }

  const rules = getFilteredRules(cwd, currentMode);
  return {
    additionalContext: rules,
    modifiedPrompt: `Switched to caveman ${currentMode} mode. Acknowledge briefly and await next instruction.`,
  };
}

function buildOneShotResponse(skillName, prompt, cwd) {
  pendingOneShotSkill = null;
  const skillRules = getSkillRules(cwd, skillName);
  session.log(`[CAVEMAN] One-shot: caveman-${skillName}`, { level: 'info', ephemeral: true });
  return {
    additionalContext: `[caveman one-shot skill: ${skillName}]\n\n${skillRules}`,
    modifiedPrompt: `Apply caveman-${skillName} rules to this interaction only. ${stripOneShotPrefix(prompt) || 'Await instructions.'}`,
  };
}

async function handleRegisteredCommand(context) {
  const normalizedCommand = `/${context.commandName}${context.args ? ` ${context.args}` : ''}`;
  lastHandledSlashPrompt = normalizedCommand;

  if (currentMode === undefined) {
    const { mode, configuredDefault } = initializeMode();
    if (mode === null) {
      session.log('[CAVEMAN] Re-initialized — off mode', { level: 'info' });
    } else if (configuredDefault && INDEPENDENT_MODES.includes(configuredDefault)) {
      session.log(`[CAVEMAN] Re-initialized — full mode (configured default '${configuredDefault}' is a one-shot skill, using full)`, { level: 'info' });
    } else {
      session.log(`[CAVEMAN] Re-initialized — ${mode} mode`, { level: 'info' });
    }
  }

  if (context.commandName === 'caveman') {
    const bareDefault = currentMode || 'full';
    const parsed = parseSlashCommand(normalizedCommand, bareDefault);
    if (parsed.type === 'mode') {
      const logMessage = applyModeSwitch(parsed.mode);
      session.log(logMessage, { level: 'info', ephemeral: true });
      return;
    }
    session.log('[CAVEMAN] Usage: /caveman [lite|full|ultra|wenyan-lite|wenyan|wenyan-ultra|off]', { level: 'info', ephemeral: true });
    return;
  }

  const skillName = context.commandName.replace(/^caveman-/, '');
  if (!ONE_SHOT_SKILLS.includes(skillName)) return;

  if (skillName === 'help' && !context.args.trim()) {
    session.log('[CAVEMAN] Commands: /caveman [lite|full|ultra|wenyan-lite|wenyan|wenyan-ultra|off], /caveman-commit, /caveman-review, /caveman-compress, /caveman-help, stop caveman', { level: 'info' });
    return;
  }

  pendingOneShotSkill = skillName;
  session.log(`[CAVEMAN] Next prompt uses caveman-${skillName}`, { level: 'info', ephemeral: true });

  if (context.args.trim()) {
    await session.send({
      prompt: context.args.trim(),
      mode: 'immediate',
    });
  }
}

// ---------------------------------------------------------------------------
// Extension entry point
// ---------------------------------------------------------------------------

const session = await joinSession({
  commands: shouldRegisterSlashCommands() ? [
    {
      name: 'caveman',
      description: 'Switch Caveman mode: lite, full, ultra, wenyan-lite, wenyan, wenyan-ultra, off',
      handler: handleRegisteredCommand,
    },
    {
      name: 'caveman-commit',
      description: 'Apply Caveman commit-message rules to next prompt or inline args',
      handler: handleRegisteredCommand,
    },
    {
      name: 'caveman-review',
      description: 'Apply Caveman code-review rules to next prompt or inline args',
      handler: handleRegisteredCommand,
    },
    {
      name: 'caveman-compress',
      description: 'Apply Caveman compression rules to next prompt or inline args',
      handler: handleRegisteredCommand,
    },
    {
      name: 'caveman-help',
      description: 'Show Caveman command help or apply help skill to inline args',
      handler: handleRegisteredCommand,
    },
  ] : [],
  hooks: {
    onSessionStart: ({ cwd }) => {
      currentWorkspacePath = cwd;

      // Preserve state already established by registered slash commands before
      // the first normal prompt in a fresh session.
      if (currentMode !== undefined) {
        if (currentMode === null) {
          session.log('[CAVEMAN] Disabled', { level: 'info' });
          return {};
        }

        session.log(`[CAVEMAN] Active — ${currentMode} mode`, { level: 'info' });
        const rules = getFilteredRules(cwd, currentMode);
        return { additionalContext: rules };
      }

      const { mode, configuredDefault } = initializeMode();
      pendingOneShotSkill = null;

      if (mode === null) {
        session.log('[CAVEMAN] Disabled by config (mode: off)', { level: 'info' });
        return {};
      }

      if (configuredDefault && INDEPENDENT_MODES.includes(configuredDefault)) {
        session.log(`[CAVEMAN] Active — full mode (configured default '${configuredDefault}' is a one-shot skill, using full)`, { level: 'info' });
      } else {
        session.log(`[CAVEMAN] Active — ${currentMode} mode`, { level: 'info' });
      }

      const rules = getFilteredRules(cwd, currentMode);
      return { additionalContext: rules };
    },

    onUserPromptSubmitted: ({ prompt, cwd }) => {
      currentWorkspacePath = cwd;
      const trimmedPrompt = (prompt || '').trim();

      if (lastHandledSlashPrompt && trimmedPrompt === lastHandledSlashPrompt) {
        lastHandledSlashPrompt = null;
        return {};
      }

      lastHandledSlashPrompt = null;
      let justReinitialized = false;
      if (currentMode === undefined) {
        initializeMode();
        justReinitialized = true;
        session.log(`[CAVEMAN] Re-initialized — ${currentMode || 'off'} mode`, { level: 'info' });
      }

      const bareDefault = currentMode || 'full';
      const parsed = parseSlashCommand(prompt, bareDefault);

      if (parsed.type === 'stop') {
        currentMode = null;
        pendingOneShotSkill = null;
        session.log('[CAVEMAN] Deactivated', { level: 'info' });
        return {
          additionalContext: 'Caveman mode OFF. Respond in normal prose from this turn onward. Do not use caveman compression style.',
          modifiedPrompt: 'Caveman mode deactivated. Respond normally from now on.',
        };
      }

      if (parsed.type === 'mode') {
        return buildModeSwitchResponse(parsed.mode, cwd);
      }

      if (parsed.type === 'oneshot') {
        return buildOneShotResponse(parsed.skill, prompt, cwd);
      }

      if (pendingOneShotSkill) {
        return buildOneShotResponse(pendingOneShotSkill, prompt, cwd);
      }

      if (currentMode) {
        if (justReinitialized) {
          const rules = getFilteredRules(cwd, currentMode);
          return { additionalContext: rules };
        }
        return {
          additionalContext: `[caveman-mode: ${currentMode}] Respond in compressed caveman prose. Technical terms exact. Code unchanged.`,
        };
      }

      return {};
    },
  },
});
