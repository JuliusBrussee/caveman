#!/usr/bin/env node
// cavecrew model overrides — patch installed agent frontmatter from env vars.
//
// Called by caveman-activate.js early in SessionStart so users can pin
// per-agent models without shadow-copying entire agent files.
//
// Env vars:
//   CAVECREW_REVIEWER_MODEL    → agents/cavecrew-reviewer.md
//   CAVECREW_BUILDER_MODEL     → agents/cavecrew-builder.md
//   CAVECREW_INVESTIGATOR_MODEL → agents/cavecrew-investigator.md
//
// Rules:
//   - Unset / blank → no-op.
//   - Values containing newlines or control characters → ignored.
//   - Existing `model:` line in frontmatter → replaced in-place.
//   - No `model:` line → inserted after `tools:` (or before closing `---`).
//   - File missing / outside plugin layout → silent no-op.
//   - All filesystem errors → silent fail.

const fs = require('fs');
const path = require('path');

const AGENT_ENV_MAP = [
  { envVar: 'CAVECREW_REVIEWER_MODEL',     file: path.join('agents', 'cavecrew-reviewer.md') },
  { envVar: 'CAVECREW_BUILDER_MODEL',      file: path.join('agents', 'cavecrew-builder.md') },
  { envVar: 'CAVECREW_INVESTIGATOR_MODEL', file: path.join('agents', 'cavecrew-investigator.md') },
];

// Return the plugin root directory given the hooks directory path.
// Plugin layout: <plugin_root>/hooks/<this-file>  →  plugin root = parent of hooks dir.
function resolvePluginRoot(hookDir) {
  return path.resolve(hookDir, '..');
}

// Patch the YAML frontmatter of `content` to set `model: <modelValue>`.
// Returns the patched string, or the original if no frontmatter or already identical.
// Rejects `modelValue` strings that contain newlines or control characters.
function patchFrontmatterModel(content, modelValue) {
  // Reject blank or unsafe model strings
  if (!modelValue || /[\x00-\x1f\x7f]/.test(modelValue)) return content;

  // Must begin with YAML frontmatter delimiter
  if (!content.startsWith('---')) return content;

  // Find the closing ---
  const closeIdx = content.indexOf('\n---', 3);
  if (closeIdx === -1) return content;

  const fmRaw = content.slice(0, closeIdx);           // opening --- through last fm line
  const after  = content.slice(closeIdx);             // \n--- onward (body)

  // Preserve original line ending so we don't create mixed CRLF/LF on Windows
  const nl = fmRaw.includes('\r\n') ? '\r\n' : '\n';

  const modelLine = 'model: ' + modelValue;
  const modelRe   = /^model:[ \t]*.*$/m;

  if (modelRe.test(fmRaw)) {
    // Replace existing model: line
    const patched = fmRaw.replace(modelRe, modelLine);
    if (patched === fmRaw) return content;            // already identical
    return patched + after;
  }

  // Insert after tools: line when present; else before closing ---
  const toolsMatch = fmRaw.match(/^tools:[ \t]*.*$/m);
  if (toolsMatch) {
    const toolsEnd = fmRaw.indexOf(toolsMatch[0]) + toolsMatch[0].length;
    return fmRaw.slice(0, toolsEnd) + nl + modelLine + fmRaw.slice(toolsEnd) + after;
  }

  // Append before closing delimiter
  return fmRaw + nl + modelLine + after;
}

// Apply all env-var overrides to agent files under `pluginRoot`.
// `env` defaults to process.env; pass an object in tests.
function applyOverrides(pluginRoot, env) {
  const envArg = env || process.env;
  for (const { envVar, file } of AGENT_ENV_MAP) {
    const raw = envArg[envVar];
    if (!raw || !raw.trim()) continue;

    const modelValue = raw.trim();
    if (/[\x00-\x1f\x7f]/.test(modelValue)) continue;

    const agentPath = path.join(pluginRoot, file);
    let content;
    try {
      content = fs.readFileSync(agentPath, 'utf8');
    } catch (e) {
      continue; // missing file or wrong layout → silent no-op
    }

    const patched = patchFrontmatterModel(content, modelValue);
    if (patched === content) continue;

    try {
      fs.writeFileSync(agentPath, patched, 'utf8');
    } catch (e) {
      // Silent fail — never block session start
    }
  }
}

module.exports = { resolvePluginRoot, patchFrontmatterModel, applyOverrides, AGENT_ENV_MAP };
