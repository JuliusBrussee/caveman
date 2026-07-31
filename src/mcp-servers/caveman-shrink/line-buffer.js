// Newline-delimited framing for the upstream MCP stdio stream.
//
// Node hands stdout to a 'data' listener in arbitrary byte-sized chunks with no
// regard for character boundaries. Decoding each chunk independently with
// Buffer#toString('utf8') therefore destroys any multi-byte character that
// happens to straddle a chunk boundary: the trailing bytes of the split
// character decode to U+FFFD in one chunk and the leading bytes to U+FFFD in
// the next. The resulting line is still valid JSON, so nothing downstream
// notices — the model just receives a corrupted tool description.
//
// StringDecoder exists for exactly this: it retains an incomplete trailing
// sequence and emits it once the continuation bytes arrive.
//
// Exported standalone so the framing is unit-testable without re-running the
// CLI entry point (index.js spawns the upstream as soon as it is required),
// mirroring spawn-options.js.

'use strict';

const { StringDecoder } = require('string_decoder');

function makeLineBuffer(onLine) {
  const decoder = new StringDecoder('utf8');
  let buf = '';
  return chunk => {
    buf += typeof chunk === 'string' ? chunk : decoder.write(chunk);
    let nl;
    while ((nl = buf.indexOf('\n')) !== -1) {
      const line = buf.slice(0, nl);
      buf = buf.slice(nl + 1);
      if (line.trim()) onLine(line);
    }
  };
}

module.exports = { makeLineBuffer };
