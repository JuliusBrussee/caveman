#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode } = require('./caveman-config');

const flagPath = path.join(os.homedir(), '.claude', '.caveman-active');

const KNOWN_INTENSITIES = new Set(['lite', 'full', 'ultra', 'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra']);
const KNOWN_LANGS = new Set(['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh']);

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    if (prompt.startsWith('/caveman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0];
      const args = parts.slice(1);

      let mode = null;

      if (cmd === '/caveman-commit') {
        mode = 'commit';
      } else if (cmd === '/caveman-review') {
        mode = 'review';
      } else if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
        mode = 'compress';
      } else if (cmd === '/caveman' || cmd === '/caveman:caveman') {
        // Parse intensity and/or language from args
        // e.g. /caveman es, /caveman lite es, /caveman ultra en, /caveman es lite
        let intensity = null;
        let lang = null;

        for (const arg of args) {
          if (KNOWN_INTENSITIES.has(arg)) {
            intensity = arg;
          } else if (KNOWN_LANGS.has(arg)) {
            lang = arg;
          }
        }

        // Resolve intensity: explicit > default
        if (!intensity) {
          const def = getDefaultMode();
          intensity = KNOWN_INTENSITIES.has(def) ? def : 'full';
        }

        // Normalize wenyan alias
        if (intensity === 'wenyan') intensity = 'wenyan-full';

        // Build mode string: intensity[-lang]
        mode = lang ? `${intensity}-${lang}` : intensity;
      }

      if (mode && mode !== 'off') {
        fs.mkdirSync(path.dirname(flagPath), { recursive: true });
        fs.writeFileSync(flagPath, mode);
      } else if (mode === 'off') {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      }
    }

    // Detect deactivation
    if (/\b(stop caveman|normal mode|modo normal)\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }
  } catch (e) {
    // Silent fail
  }
});
