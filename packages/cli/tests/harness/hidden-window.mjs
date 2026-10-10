import assert from "node:assert/strict";

// #1214: a generated plugin runs the Caveman CLI as a child process on every
// host event. When the parent has no console of its own — OpenCode under a
// multiplexer or a GUI host — Windows gives each console child a NEW console
// window, so the user gets one flashing window per prompt, tool call and bash
// command. `windowsHide: true` suppresses that and is ignored off Windows.
//
// The CLI sets it on its own spawns already; the generated plugins did not,
// because each template is a standalone file that cannot import a shared
// helper (it loads in the host's module space, not ours). The literal has to
// be in every template, which is exactly the kind of thing that drifts — so
// the guard counts call sites instead of spot-checking one.
const JS_CHILD_CALL = /\b(?:execFileSync|execSync|spawnSync|execFile|spawn|exec)\s*\(/g;

export function assertHidesChildWindows(source, label) {
  const calls = source.match(JS_CHILD_CALL) ?? [];
  assert.ok(calls.length > 0, `${label}: expected at least one child-process call to guard`);
  const hidden = source.match(/windowsHide:\s*true/g) ?? [];
  assert.equal(
    hidden.length,
    calls.length,
    `${label}: ${calls.length} child-process call(s) but ${hidden.length} windowsHide: true — every spawn in a generated plugin must hide its console window on Windows (#1214)`,
  );
}

// The Python plugins reach the same syscall through subprocess, where the
// equivalent is the CREATE_NO_WINDOW creation flag. subprocess ignores
// `windowsHide`, and CREATE_NO_WINDOW does not exist on POSIX, so the
// generated module must resolve it at run time rather than import it.
export function assertHidesChildWindowsPython(source, label) {
  const calls = source.match(/subprocess\.(?:run|check_output|check_call|call|Popen)\s*\(/g) ?? [];
  assert.ok(calls.length > 0, `${label}: expected at least one subprocess call to guard`);
  const flagged = source.match(/creationflags=/g) ?? [];
  assert.equal(
    flagged.length,
    calls.length,
    `${label}: ${calls.length} subprocess call(s) but ${flagged.length} creationflags= — every subprocess in a generated plugin must pass CREATE_NO_WINDOW on Windows (#1214)`,
  );
  assert.match(source, /CREATE_NO_WINDOW/, `${label}: the creation flag must name CREATE_NO_WINDOW`);
  assert.match(
    source,
    /getattr\(\s*subprocess\s*,\s*["']CREATE_NO_WINDOW["']\s*,\s*0\s*\)/,
    `${label}: CREATE_NO_WINDOW must be resolved with getattr(..., 0) — the attribute does not exist off Windows`,
  );
}
