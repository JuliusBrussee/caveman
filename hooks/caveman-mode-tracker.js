#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode } = require('./caveman-config');

const flagPath = path.join(os.homedir(), '.claude', '.caveman-active');
const prevPath = path.join(os.homedir(), '.claude', '.caveman-active.prev');

function readMode(p) {
  try { return fs.readFileSync(p, 'utf8').trim(); } catch (e) { return ''; }
}
function clearFlag(p) {
  try { fs.unlinkSync(p); } catch (e) {}
}

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // Match /caveman commands
    if (prompt.startsWith('/caveman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /caveman, /caveman-commit, /caveman-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      if (cmd === '/caveman-commit') {
        mode = 'commit';
      } else if (cmd === '/caveman-review') {
        mode = 'review';
      } else if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
        mode = 'compress';
      } else if (cmd === '/caveman-translate') {
        // Translate is transient — remember previous mode so we can restore it
        // when the user exits with "stop caveman-translate" / "done translating".
        const prev = readMode(flagPath);
        if (prev && prev !== 'translate') {
          try {
            fs.mkdirSync(path.dirname(prevPath), { recursive: true });
            fs.writeFileSync(prevPath, prev);
          } catch (e) {}
        }
        mode = 'translate';
      } else if (cmd === '/caveman' || cmd === '/caveman:caveman') {
        if (arg === 'lite') mode = 'lite';
        else if (arg === 'ultra') mode = 'ultra';
        else if (arg === 'wenyan-lite') mode = 'wenyan-lite';
        else if (arg === 'wenyan' || arg === 'wenyan-full') mode = 'wenyan';
        else if (arg === 'wenyan-ultra') mode = 'wenyan-ultra';
        else mode = getDefaultMode();
      }

      if (mode) {
        fs.mkdirSync(path.dirname(flagPath), { recursive: true });
        fs.writeFileSync(flagPath, mode);
      }
    }

    // Translate exit — restore the prior mode if any. Must run BEFORE the
    // generic "stop caveman" detection since "stop caveman-translate" matches
    // both expressions.
    if (/\b(stop caveman-translate|done translating)\b/i.test(prompt)) {
      const prev = readMode(prevPath);
      if (prev) {
        try {
          fs.mkdirSync(path.dirname(flagPath), { recursive: true });
          fs.writeFileSync(flagPath, prev);
        } catch (e) {}
      } else {
        clearFlag(flagPath);
      }
      clearFlag(prevPath);
      return;
    }

    // Full deactivation — also clears any stashed translate-prev.
    if (/\b(stop caveman|normal mode)\b/i.test(prompt)) {
      clearFlag(flagPath);
      clearFlag(prevPath);
    }
  } catch (e) {
    // Silent fail
  }
});
