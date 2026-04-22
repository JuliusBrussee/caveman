// Integration test for Copilot CLI extension
// Tests parser, rules, stop detection, and extension structure

import { parseSlashCommand, isStopCommand, VALID_MODES, ONE_SHOT_SKILLS, INDEPENDENT_MODES } from '../.github/extensions/caveman/parser.mjs';
import { CAVEMAN_SKILL, CAVEMAN_COMMIT_SKILL, CAVEMAN_REVIEW_SKILL, CAVEMAN_COMPRESS_SKILL, CAVEMAN_HELP_SKILL } from '../.github/extensions/caveman/rules.mjs';
import { readFileSync } from 'node:fs';

let passed = 0, failed = 0;
function check(name, result) {
  if (result) { passed++; } else { failed++; console.log('FAIL: ' + name); }
}

// === PARSER: Mode commands ===
check('/caveman -> mode:full', (() => { const r = parseSlashCommand('/caveman', 'full'); return r.type === 'mode' && r.mode === 'full'; })());
check('/caveman lite -> mode:lite', (() => { const r = parseSlashCommand('/caveman lite', 'full'); return r.type === 'mode' && r.mode === 'lite'; })());
check('/caveman ultra -> mode:ultra', (() => { const r = parseSlashCommand('/caveman ultra', 'full'); return r.type === 'mode' && r.mode === 'ultra'; })());
check('caveman -> mode:full', (() => { const r = parseSlashCommand('caveman', 'full'); return r.type === 'mode' && r.mode === 'full'; })());
check('caveman lite -> mode:lite', (() => { const r = parseSlashCommand('caveman lite', 'full'); return r.type === 'mode' && r.mode === 'lite'; })());
check('caveman ultra -> mode:ultra', (() => { const r = parseSlashCommand('caveman ultra', 'full'); return r.type === 'mode' && r.mode === 'ultra'; })());
check('/caveman wenyan -> mode:wenyan', (() => { const r = parseSlashCommand('/caveman wenyan', 'full'); return r.type === 'mode' && r.mode === 'wenyan'; })());
check('/caveman wenyan-lite', (() => { const r = parseSlashCommand('/caveman wenyan-lite', 'full'); return r.type === 'mode' && r.mode === 'wenyan-lite'; })());
check('/caveman wenyan-full', (() => { const r = parseSlashCommand('/caveman wenyan-full', 'full'); return r.type === 'mode' && r.mode === 'wenyan'; })());
check('/caveman wenyan-ultra', (() => { const r = parseSlashCommand('/caveman wenyan-ultra', 'full'); return r.type === 'mode' && r.mode === 'wenyan-ultra'; })());
check('caveman wenyan -> mode:wenyan', (() => { const r = parseSlashCommand('caveman wenyan', 'full'); return r.type === 'mode' && r.mode === 'wenyan'; })());
check('/caveman off -> mode:off', (() => { const r = parseSlashCommand('/caveman off', 'full'); return r.type === 'mode' && r.mode === 'off'; })());
check('caveman off -> mode:off', (() => { const r = parseSlashCommand('caveman off', 'full'); return r.type === 'mode' && r.mode === 'off'; })());

// === PARSER: One-shot skills ===
check('/caveman-commit -> oneshot:commit', (() => { const r = parseSlashCommand('/caveman-commit fix login', 'full'); return r.type === 'oneshot' && r.skill === 'commit'; })());
check('/caveman-review -> oneshot:review', (() => { const r = parseSlashCommand('/caveman-review', 'full'); return r.type === 'oneshot' && r.skill === 'review'; })());
check('/caveman-compress -> oneshot:compress', (() => { const r = parseSlashCommand('/caveman-compress', 'full'); return r.type === 'oneshot' && r.skill === 'compress'; })());
check('/caveman-help -> oneshot:help', (() => { const r = parseSlashCommand('/caveman-help', 'full'); return r.type === 'oneshot' && r.skill === 'help'; })());
check('caveman commit -> oneshot:commit', (() => { const r = parseSlashCommand('caveman commit fix login', 'full'); return r.type === 'oneshot' && r.skill === 'commit'; })());
check('caveman-review -> oneshot:review', (() => { const r = parseSlashCommand('caveman-review', 'full'); return r.type === 'oneshot' && r.skill === 'review'; })());
check('caveman compress -> oneshot:compress', (() => { const r = parseSlashCommand('caveman compress README.md', 'full'); return r.type === 'oneshot' && r.skill === 'compress'; })());
check('caveman help -> oneshot:help', (() => { const r = parseSlashCommand('caveman help', 'full'); return r.type === 'oneshot' && r.skill === 'help'; })());

// === PARSER: Stop commands ===
check('"stop caveman" -> stop', parseSlashCommand('stop caveman', 'full').type === 'stop');
check('"normal mode" -> stop', parseSlashCommand('normal mode', 'full').type === 'stop');

// === PARSER: Non-commands -> null ===
check('normal prompt -> null', parseSlashCommand('fix the bug in auth', 'full').type === null);
check('question about stop -> null', parseSlashCommand('How do I stop caveman from running?', 'full').type === null);
check('empty -> null', parseSlashCommand('', 'full').type === null);
check('null input -> null', parseSlashCommand(null, 'full').type === null);

// === PARSER: Bare /caveman reactivation ===
check('bare /caveman with full default', (() => { const r = parseSlashCommand('/caveman', 'full'); return r.type === 'mode' && r.mode === 'full'; })());
check('bare /caveman with lite default', (() => { const r = parseSlashCommand('/caveman', 'lite'); return r.type === 'mode' && r.mode === 'lite'; })());
check('bare caveman with lite default', (() => { const r = parseSlashCommand('caveman', 'lite'); return r.type === 'mode' && r.mode === 'lite'; })());

// === PARSER: Unknown arg treated as bare ===
check('unknown arg fallback', (() => { const r = parseSlashCommand('/caveman foobar', 'full'); return r.type === 'mode' && r.mode === 'full'; })());
check('plain unknown arg -> null', parseSlashCommand('caveman foobar', 'full').type === null);

// === STOP DETECTION ===
check('stop exact match', isStopCommand('stop caveman'));
check('stop case insensitive', isStopCommand('STOP CAVEMAN'));
check('stop with whitespace', isStopCommand('  stop caveman  '));
check('normal mode exact', isStopCommand('normal mode'));
check('NOT: question about stop', !isStopCommand('How do I stop caveman?'));
check('NOT: sentence with stop', !isStopCommand('Please stop caveman from doing that'));
check('NOT: stop with suffix', !isStopCommand('stop caveman please help'));
check('NOT: partial normal mode', !isStopCommand('I want normal mode for debugging'));
check('NOT: empty string', !isStopCommand(''));
check('NOT: null', !isStopCommand(null));
check('NOT: caveman architecture overview', parseSlashCommand('caveman architecture overview', 'full').type === null);

// === RULES EXPORTS ===
check('CAVEMAN_SKILL non-empty', typeof CAVEMAN_SKILL === 'string' && CAVEMAN_SKILL.length > 100);
check('CAVEMAN_COMMIT_SKILL non-empty', typeof CAVEMAN_COMMIT_SKILL === 'string' && CAVEMAN_COMMIT_SKILL.length > 50);
check('CAVEMAN_REVIEW_SKILL non-empty', typeof CAVEMAN_REVIEW_SKILL === 'string' && CAVEMAN_REVIEW_SKILL.length > 50);
check('CAVEMAN_COMPRESS_SKILL non-empty', typeof CAVEMAN_COMPRESS_SKILL === 'string' && CAVEMAN_COMPRESS_SKILL.length > 50);
check('CAVEMAN_HELP_SKILL non-empty', typeof CAVEMAN_HELP_SKILL === 'string' && CAVEMAN_HELP_SKILL.length > 50);

// === EXTENSION ENTRYPOINT ===
const extensionEntry = readFileSync(new URL('../.github/extensions/caveman/extension.mjs', import.meta.url), 'utf8');
check('extension entrypoint has node shebang', extensionEntry.startsWith('#!/usr/bin/env node'));

// Rules contain expected keywords
check('CAVEMAN_SKILL mentions intensity', CAVEMAN_SKILL.includes('lite') && CAVEMAN_SKILL.includes('ultra'));
check('CAVEMAN_COMMIT_SKILL mentions commit', CAVEMAN_COMMIT_SKILL.toLowerCase().includes('commit'));
check('CAVEMAN_REVIEW_SKILL mentions review', CAVEMAN_REVIEW_SKILL.toLowerCase().includes('review'));

// === CONSTANTS ===
check('all expected modes', ['off', 'lite', 'full', 'ultra', 'wenyan', 'wenyan-lite', 'wenyan-full', 'wenyan-ultra'].every(m => VALID_MODES.includes(m)));
check('all expected skills', ['commit', 'review', 'compress', 'help'].every(s => ONE_SHOT_SKILLS.includes(s)));
check('independent modes', ['commit', 'review', 'compress'].every(m => INDEPENDENT_MODES.includes(m)));

// === EXTENSION SOURCE SHAPE ===
// Static source checks only. Copilot SDK runtime is not available in plain
// Node tests, so these assertions intentionally verify bundled entrypoint
// structure rather than runtime hook behavior.
const ext = readFileSync(new URL('../.github/extensions/caveman/extension.mjs', import.meta.url), 'utf8');
check('source imports joinSession from SDK', ext.includes("joinSession") && ext.includes("@github/copilot-sdk/extension"));
check('source contains commands array registration', ext.includes('commands: [') || ext.includes('commands: shouldRegisterSlashCommands() ? ['));
check('source includes caveman command name', ext.includes("name: 'caveman'"));
check('source includes caveman-commit command name', ext.includes("name: 'caveman-commit'"));
check('source defines shouldRegisterSlashCommands helper', ext.includes('function shouldRegisterSlashCommands()'));
check('source defines onSessionStart hook', ext.includes('onSessionStart'));
check('source defines onUserPromptSubmitted hook', ext.includes('onUserPromptSubmitted'));
check('source guards currentMode before overwrite', ext.includes("if (currentMode !== undefined)"));
check('source tracks justReinitialized flag', ext.includes('justReinitialized'));
check('source computes bareDefault fallback', ext.includes("const bareDefault = currentMode || 'full'"));
check('source includes caveman-mode injection marker', ext.includes('[caveman-mode:'));
check('source tracks pendingOneShotSkill', ext.includes('pendingOneShotSkill'));
check('source tracks lastHandledSlashPrompt', ext.includes('lastHandledSlashPrompt'));
check('source defines handleRegisteredCommand', ext.includes('handleRegisteredCommand'));
check('source imports shared config resolver', ext.includes("import configModule from './config.js'"));
check('source defines tryReadWorkspaceSkill', ext.includes('tryReadWorkspaceSkill'));
check('source defines filterRulesToLevel', ext.includes('function filterRulesToLevel'));
check('source defines rulesCache', ext.includes('rulesCache'));
check('source calls session.log', ext.includes('session.log'));
check('source checks off mode branch', ext.includes("mode === null") || ext.includes("defaultMode === 'off'"));
check('source guards INDEPENDENT_MODES defaults', ext.includes('INDEPENDENT_MODES.includes(configuredDefault)') || ext.includes('INDEPENDENT_MODES.includes(defaultMode)'));
check('source strips plain one-shot aliases', ext.includes('caveman\\s+(?:commit|review|compress|help)'));
check('source normalizes command args into slash prompt', ext.includes('const normalizedCommand = `/${context.commandName}'));

// === PARSER FILE SYNC ===
const canonical = readFileSync(new URL('../lib/caveman-mode-parser.mjs', import.meta.url), 'utf8');
const copy = readFileSync(new URL('../.github/extensions/caveman/parser.mjs', import.meta.url), 'utf8');
check('parser.mjs matches canonical lib/', canonical === copy);

// === CONFIG FILE SYNC ===
const canonicalConfig = readFileSync(new URL('../lib/caveman-config.js', import.meta.url), 'utf8');
const hookConfig = readFileSync(new URL('../hooks/caveman-config.js', import.meta.url), 'utf8');
const extensionConfig = readFileSync(new URL('../.github/extensions/caveman/config.js', import.meta.url), 'utf8');
check('hooks config matches canonical lib/', canonicalConfig === hookConfig);
check('extension config matches canonical lib/', canonicalConfig === extensionConfig);

// === SUMMARY ===
console.log('\n' + '='.repeat(50));
console.log(`Integration tests: ${passed}/${passed + failed}` + (failed === 0 ? ' ✅ ALL PASSED' : ` ❌ ${failed} FAILURES`));
console.log('='.repeat(50));
process.exit(failed > 0 ? 1 : 0);
