// Teardown for the caveman-shrink proxy, kept out of index.js so it can be
// driven by tests without spawning a real upstream process.
//
// Two jobs:
//   1. Forward a termination signal to the upstream and, if the upstream
//      ignores it, escalate to SIGKILL after a grace period. Without the
//      escalation a server that traps SIGTERM and declines to exit keeps both
//      itself and this wrapper alive after the host has asked them to stop.
//   2. Translate the upstream's `close` into our own exit code, and detach the
//      client-side stdin listeners so the event loop can drain.

const { constants: osConstants } = require('os');

// Long enough for a server to flush and close its own resources, short enough
// that a host tearing down a session does not visibly hang on us.
const DEFAULT_GRACE_MS = 2000;

function createShutdown({
  child,
  detachInput = () => {},
  spawnFailed = () => false,
  graceMs = DEFAULT_GRACE_MS,
  // Injectable so the escalation can be tested without waiting on a real clock.
  timers = { setTimeout, clearTimeout },
} = {}) {
  let graceTimer = null;

  function clearGrace() {
    if (graceTimer === null) return;
    timers.clearTimeout(graceTimer);
    graceTimer = null;
  }

  return {
    // Send `signal` to the upstream and arm the escalation. Safe to call more
    // than once: `kill` marks the child, so an impatient second Ctrl-C is a
    // no-op rather than a duplicate signal and a second timer. A child that has
    // already exited on its own is left alone.
    forward(signal) {
      if (child.killed || child.exitCode !== null || child.signalCode !== null) return;
      child.kill(signal);
      graceTimer = timers.setTimeout(() => {
        graceTimer = null;
        // Still here after the grace period: the upstream is ignoring the
        // signal, so take the one it cannot trap.
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
      }, graceMs);
    },

    // Returns the exit code this process should adopt. A child that died from
    // a signal has no exit code of its own, so report it the way a shell does.
    onClose(code, signal) {
      clearGrace();
      detachInput();
      if (spawnFailed()) return 1;
      if (signal) return 128 + (osConstants.signals[signal] || 1);
      return code || 0;
    },

    // Exposed for tests; nothing in index.js needs to ask.
    get pendingEscalation() {
      return graceTimer !== null;
    },
  };
}

module.exports = { createShutdown, DEFAULT_GRACE_MS };
