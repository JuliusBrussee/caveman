// Teardown for the caveman-shrink proxy, kept out of index.js so it can be
// driven by tests without spawning a real upstream process.
//
// This wrapper is an MCP client to the upstream it spawns, so teardown follows
// the sequence the spec gives that client (lifecycle, "Shutdown" > stdio):
// close the upstream's input stream, wait for it to exit, send SIGTERM if it
// does not, then SIGKILL if that is ignored too. Both entry points below feed
// into the same escalation:
//
//   1. `closeInput` — our own client closed our stdin, so we close the
//      upstream's and start the sequence at step 1.
//   2. `forward` — the host signalled us instead, which enters at step 2.
//
// `onClose` then translates the upstream's exit into our own exit code and
// detaches the client-side stdin listeners so the event loop can drain.

const { constants: osConstants } = require('os');

// Long enough for a server to flush and close its own resources, short enough
// that a host tearing down a session does not visibly hang on us. Used for both
// waits, so an upstream that ignores EOF *and* SIGTERM costs two of these.
const DEFAULT_GRACE_MS = 2000;

function createShutdown({
  child,
  endUpstreamInput = () => {},
  detachInput = () => {},
  spawnFailed = () => false,
  graceMs = DEFAULT_GRACE_MS,
  // Injectable so the escalation can be tested without waiting on a real clock.
  timers = { setTimeout, clearTimeout },
} = {}) {
  let graceTimer = null;
  let eofTimer = null;

  // Node leaves both null until the child is gone, then sets exactly one.
  const upstreamGone = () => child.exitCode !== null || child.signalCode !== null;

  function clearTimer(id) {
    if (id !== null) timers.clearTimeout(id);
    return null;
  }

  // Send `signal` to the upstream and arm the escalation. Safe to call more
  // than once: `kill` marks the child, so an impatient second Ctrl-C is a
  // no-op rather than a duplicate signal and a second timer. A child that has
  // already exited on its own is left alone.
  function forward(signal) {
    if (child.killed || upstreamGone()) return;
    child.kill(signal);
    graceTimer = timers.setTimeout(() => {
      graceTimer = null;
      // Still here after the grace period: the upstream is ignoring the
      // signal, so take the one it cannot trap.
      if (!upstreamGone()) child.kill('SIGKILL');
    }, graceMs);
  }

  // Step 1 of that sequence: EOF on our stdin means our client wants us gone,
  // so pass the EOF on. An upstream that exits on it fires `close` and clears
  // the timer below before anything is signalled; one that ignores EOF would
  // otherwise sit there forever holding this wrapper open with it, which is
  // what the wait escalates out of. Nothing is signalled on this path until
  // the upstream has had its grace period to leave on its own.
  function closeInput() {
    endUpstreamInput();
    if (upstreamGone() || eofTimer !== null) return;
    eofTimer = timers.setTimeout(() => {
      eofTimer = null;
      forward('SIGTERM');
    }, graceMs);
  }

  return {
    forward,
    closeInput,

    // Returns the exit code this process should adopt. A child that died from
    // a signal has no exit code of its own, so report it the way a shell does.
    onClose(code, signal) {
      graceTimer = clearTimer(graceTimer);
      eofTimer = clearTimer(eofTimer);
      detachInput();
      if (spawnFailed()) return 1;
      if (signal) return 128 + (osConstants.signals[signal] || 1);
      return code || 0;
    },

    // Exposed for tests; nothing in index.js needs to ask.
    get pendingEscalation() {
      return graceTimer !== null || eofTimer !== null;
    },
  };
}

module.exports = { createShutdown, DEFAULT_GRACE_MS };
