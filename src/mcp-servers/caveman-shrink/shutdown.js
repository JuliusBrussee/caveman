// Upstream teardown for the caveman-shrink proxy.
//
// The proxy spawns the upstream MCP server, so nothing else will clean it up.
// Without this, a host that stops caveman-shrink with a signal — ending a
// session, disabling a tool, reloading config — leaves the upstream running,
// reparented away from us, still holding its stdio open (#742).
//
// Platform notes:
//
//   POSIX  — the host's SIGTERM/SIGINT reaches us and child.kill() reaches the
//            upstream, so forwarding is the whole fix.
//   win32  — there are no real signals. Node emits SIGINT/SIGBREAK from console
//            events, but a host stopping a background proxy will not produce
//            them, so teardown there runs through the stdin-EOF path instead.
//            getSpawnInvocation() resolves .cmd shims to their Node target, so
//            our direct child is the server itself and kill() reaches it.
//
// Exported standalone so teardown is unit-testable without running the CLI
// entry point, mirroring spawn-options.js.

'use strict';

// Signals worth forwarding. SIGKILL is deliberately absent — it cannot be
// trapped, and a host sending it has already decided not to wait.
const FORWARDED_SIGNALS = ['SIGTERM', 'SIGINT', 'SIGHUP'];

// How long the upstream gets to exit on its own after being signalled before
// we stop waiting and exit anyway. Short enough not to hang a host that is
// tearing down, long enough for a server to flush and close cleanly.
const GRACE_MS = 2000;

function isAlive(child) {
  return Boolean(child && child.pid && child.exitCode === null && child.signalCode === null);
}

// Terminate the upstream. Returns true when a kill was actually attempted.
function killUpstream(child, signal = 'SIGTERM') {
  if (!isAlive(child)) return false;
  try {
    child.kill(signal);
    return true;
  } catch {
    return false;
  }
}

// Wire signal forwarding onto a live upstream child. `opts.process`,
// `opts.graceMs` and `opts.setTimeout` are injectable so tests need neither a
// real signal nor a two-second wait.
function installShutdownHandlers(child, opts = {}) {
  const proc = opts.process || process;
  const graceMs = opts.graceMs === undefined ? GRACE_MS : opts.graceMs;
  const setTimer = opts.setTimeout || setTimeout;
  let shuttingDown = false;

  function shutdown(signal) {
    if (shuttingDown) return;
    shuttingDown = true;
    killUpstream(child, signal);

    // If the upstream ignores the signal, stop waiting. unref() so a prompt
    // child exit still lets the process end through the normal `close` path
    // rather than idling here for the full grace period.
    const timer = setTimer(() => {
      proc.exit(128 + (signal === 'SIGINT' ? 2 : 15));
    }, graceMs);
    if (timer && typeof timer.unref === 'function') timer.unref();
  }

  for (const sig of FORWARDED_SIGNALS) {
    // SIGHUP is not emulated everywhere and can throw on registration.
    try { proc.on(sig, () => shutdown(sig)); } catch { /* not supported here */ }
  }

  return { shutdown, isShuttingDown: () => shuttingDown };
}

module.exports = { killUpstream, installShutdownHandlers, FORWARDED_SIGNALS, GRACE_MS };
