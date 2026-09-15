'use strict';

// Parse an argv value, not shell code. Quotes group arguments; backslashes,
// dollar signs and percent signs stay literal on every platform. JSON arrays
// provide an unambiguous form for arguments containing both kinds of quote.
function parseCommandArgs(value) {
  const source = String(value).trim();
  let args;
  if (source.startsWith('[')) {
    try { args = JSON.parse(source); }
    catch (_) { throw new Error('upstream command JSON must be an array of strings'); }
    if (!Array.isArray(args) || args.some(arg => typeof arg !== 'string')) {
      throw new Error('upstream command JSON must be an array of strings');
    }
  } else {
    args = [];
    let quote = null;
    let token = '';
    let started = false;
    for (const char of source) {
      if (quote) {
        if (char === quote) quote = null;
        else token += char;
        started = true;
      } else if (char === '"' || char === "'") {
        quote = char;
        started = true;
      } else if (/\s/.test(char)) {
        if (started) args.push(token);
        token = '';
        started = false;
      } else {
        token += char;
        started = true;
      }
    }
    if (quote) throw new Error('upstream command has an unmatched quote');
    if (started) args.push(token);
  }
  if (!args.length || !args[0].trim()) throw new Error('upstream command requires an executable');
  if (args.some(arg => arg.includes('\0'))) throw new Error('upstream command must not contain NUL bytes');
  return args;
}

module.exports = { parseCommandArgs };
