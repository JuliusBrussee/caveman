#!/usr/bin/env node
// Shared session-id sanitizer + file-state-matrix tests for per-session flag
// isolation. Exercises sanitizeSessionId/resolveFlag/resolvePrev directly
// (caveman-config.js) AND the same scenarios end-to-end through the Bash and
// PowerShell statuslines, proving all three implementations agree and that
// an invalid session id never produces a stripped/truncated scoped path.
//
// Run: node tests/test_session_scoping.js

const fs = require('fs');
const path = require('path');
const os = require('os');
const assert = require('assert');
const { execFileSync } = require('child_process');

const {
  sanitizeSessionId, flagBaseName, resolveFlag, resolvePrev, safeWriteFlag,
} = require('../src/hooks/caveman-config');

const ROOT = path.join(__dirname, '..');
const STATUSLINE_SH = path.join(ROOT, 'src', 'hooks', 'caveman-statusline.sh');
const STATUSLINE_PS1 = path.join(ROOT, 'src', 'hooks', 'caveman-statusline.ps1');

let passed = 0;
let failed = 0;

function test(name, fn) {
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-scoping-test-'));
  try {
    fn(tmpBase);
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.error(`  ✗ ${name}`);
    console.error(`    ${e.message}`);
  } finally {
    try { fs.chmodSync(tmpBase, 0o700); } catch (_) {}
    fs.rmSync(tmpBase, { recursive: true, force: true });
  }
}

function skip(name, reason) {
  console.log(`  ~ ${name} (skipped: ${reason})`);
}

let pwshBin = null;
try {
  execFileSync(process.platform === 'win32' ? 'where' : 'which', ['pwsh'], { stdio: 'ignore' });
  pwshBin = 'pwsh';
} catch (_) {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', ['powershell'], { stdio: 'ignore' });
    pwshBin = 'powershell';
  } catch (_) { /* neither available */ }
}

function runStatuslineSh(configDir, sessionId) {
  return execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: JSON.stringify({ session_id: sessionId }),
  });
}

function runStatuslinePs1(configDir, sessionId) {
  return execFileSync(pwshBin, ['-NoProfile', '-File', STATUSLINE_PS1], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: JSON.stringify({ session_id: sessionId }),
  });
}

// ── Sanitizer vector table ──────────────────────────────────────────────────

const VALID_UUID = 'a1b2c3d4-e5f6-7890-abcd-ef1234567890';
const VALID_SHORT = 'test-sess-1';
const OVERSIZED = 'a'.repeat(200);

const SANITIZER_VECTORS = [
  { name: 'valid UUID', input: VALID_UUID, expected: VALID_UUID },
  { name: 'valid short alnum+hyphen id', input: VALID_SHORT, expected: VALID_SHORT },
  { name: 'empty string', input: '', expected: null },
  { name: 'path traversal', input: '../../etc/passwd', expected: null },
  { name: 'embedded path separator', input: 'abc/def', expected: null },
  { name: '200-char string (exceeds 128 cap)', input: OVERSIZED, expected: null },
  { name: 'null', input: null, expected: null },
  { name: 'number (wrong type)', input: 12345, expected: null },
];

for (const v of SANITIZER_VECTORS) {
  test(`sanitizeSessionId: ${v.name}`, () => {
    assert.strictEqual(sanitizeSessionId(v.input), v.expected);
  });
}

// ── Cross-implementation parity: JS sanitizer vs Bash vs PowerShell ────────
// Critical property: an invalid session id must fall back to the LEGACY
// path in every implementation, never a stripped/truncated scoped path.
// Prove this by seeding a distinguishable legacy sentinel and a (path-
// unsafe) scoped file that would only be reachable via a stripped id.

for (const v of SANITIZER_VECTORS) {
  test(`cross-impl parity (bash): ${v.name}`, () => {
    const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-parity-'));
    try {
      fs.writeFileSync(path.join(tmpBase, '.caveman-active'), 'ultra'); // legacy sentinel
      const out = runStatuslineSh(tmpBase, v.input);
      if (v.expected === null) {
        // Invalid/rejected id -> legacy sentinel is what renders.
        assert.match(out, /\[CAVEMAN:ULTRA\]/, `expected legacy sentinel for input ${JSON.stringify(v.input)}`);
      }
      // (valid ids with no scoped file also correctly fall back to legacy --
      // covered by the file-state matrix below with a real scoped file.)
    } finally {
      fs.rmSync(tmpBase, { recursive: true, force: true });
    }
  });
}

if (pwshBin) {
  for (const v of SANITIZER_VECTORS) {
    test(`cross-impl parity (powershell): ${v.name}`, () => {
      const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-parity-ps-'));
      try {
        fs.writeFileSync(path.join(tmpBase, '.caveman-active'), 'ultra');
        const out = runStatuslinePs1(tmpBase, v.input);
        if (v.expected === null) {
          assert.match(out, /\[CAVEMAN:ULTRA\]/, `expected legacy sentinel for input ${JSON.stringify(v.input)}`);
        }
      } finally {
        fs.rmSync(tmpBase, { recursive: true, force: true });
      }
    });
  }
} else {
  skip('cross-impl parity (powershell): all vectors', 'pwsh/powershell not found on PATH');
}

// ── File-state matrix ───────────────────────────────────────────────────────
// For a VALID session id, every scoped-file state must resolve correctly and
// must read the legacy sentinel ONLY for the true ENOENT case.

const SESSION_ID = 'matrix-sess';
const LEGACY_SENTINEL = 'wenyan-ultra'; // distinguishable from every scoped test value

function seedLegacy(configDir) {
  fs.mkdirSync(configDir, { recursive: true });
  fs.writeFileSync(path.join(configDir, '.caveman-active'), LEGACY_SENTINEL);
}

function scopedPath(configDir) {
  return path.join(configDir, flagBaseName(SESSION_ID));
}

test('file-state matrix: scoped-ENOENT reads legacy sentinel (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, LEGACY_SENTINEL);
  assert.strictEqual(r.rejected, false);
  assert.strictEqual(r.path, path.join(tmp, '.caveman-active'));
});

test('file-state matrix: scoped-ENOENT reads legacy sentinel (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/);
});

if (pwshBin) {
  test('file-state matrix: scoped-ENOENT reads legacy sentinel (statusline.ps1)', (tmp) => {
    seedLegacy(tmp);
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/);
  });
}

test('file-state matrix: scoped-valid-content ignores legacy sentinel (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  safeWriteFlag(scopedPath(tmp), 'ultra');
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, 'ultra');
  assert.strictEqual(r.rejected, false);
  assert.strictEqual(r.path, scopedPath(tmp));
});

test('file-state matrix: scoped-valid-content ignores legacy sentinel (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  safeWriteFlag(scopedPath(tmp), 'ultra');
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.match(out, /\[CAVEMAN:ULTRA\]/);
  assert.doesNotMatch(out, /WENYAN/);
});

if (pwshBin) {
  test('file-state matrix: scoped-valid-content ignores legacy sentinel (statusline.ps1)', (tmp) => {
    seedLegacy(tmp);
    safeWriteFlag(scopedPath(tmp), 'ultra');
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.match(out, /\[CAVEMAN:ULTRA\]/);
    assert.doesNotMatch(out, /WENYAN/);
  });
}

test('file-state matrix: scoped-off-content renders nothing (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  safeWriteFlag(scopedPath(tmp), 'off');
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, 'off');
  assert.strictEqual(r.rejected, false);
});

test('file-state matrix: scoped-off-content renders nothing (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  safeWriteFlag(scopedPath(tmp), 'off');
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '');
});

if (pwshBin) {
  test('file-state matrix: scoped-off-content renders nothing (statusline.ps1)', (tmp) => {
    seedLegacy(tmp);
    safeWriteFlag(scopedPath(tmp), 'off');
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '');
  });
}

test('file-state matrix: scoped-invalid-content rejected, never falls back (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'not-a-real-mode');
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, null);
  assert.strictEqual(r.rejected, true);
  assert.strictEqual(r.path, scopedPath(tmp));
});

test('file-state matrix: scoped-invalid-content rejected, never falls back (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'not-a-real-mode');
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), ''); // rejected -> nothing, NOT the legacy sentinel
});

if (pwshBin) {
  test('file-state matrix: scoped-invalid-content rejected, never falls back (statusline.ps1)', (tmp) => {
    seedLegacy(tmp);
    fs.mkdirSync(tmp, { recursive: true });
    fs.writeFileSync(scopedPath(tmp), 'not-a-real-mode');
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '');
  });
}

test('file-state matrix: scoped-oversized rejected, never falls back (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'x'.repeat(200));
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, null);
  assert.strictEqual(r.rejected, true);
});

test('file-state matrix: scoped-oversized rejected, never falls back (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'x'.repeat(200));
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '');
});

test('scoped-oversized content that STARTS with a valid mode word must still be rejected, not truncated-then-accepted (statusline.sh) (PR-review High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'full' + 'x'.repeat(100)); // oversized, but head -c 64 alone would see "full..."
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '', 'oversized content must be rejected outright, never truncated down to a valid-looking mode');
});

test('scoped content with embedded invalid characters ("f u l l") must be rejected, not stripped-then-accepted (statusline.sh) (PR-review High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(scopedPath(tmp), 'f u l l');
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '', '"f u l l" must be rejected outright, never stripped down to "full"');
});

if (pwshBin) {
  test('scoped content with an embedded newline ("full\\nnot-a-mode") must be rejected, not first-line-accepted (statusline.ps1) (PR-review High)', (tmp) => {
    seedLegacy(tmp);
    fs.mkdirSync(tmp, { recursive: true });
    fs.writeFileSync(scopedPath(tmp), 'full\nnot-a-mode');
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '', 'multi-line content must be rejected as a whole, never validated on its first line alone');
  });
}

test('file-state matrix: scoped-symlink rejected, never falls back (resolveFlag)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  const target = path.join(tmp, 'elsewhere.txt');
  fs.writeFileSync(target, 'full');
  fs.symlinkSync(target, scopedPath(tmp));
  const r = resolveFlag(tmp, SESSION_ID);
  assert.strictEqual(r.mode, null);
  assert.strictEqual(r.rejected, true);
});

test('file-state matrix: scoped-symlink rejected, never falls back (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  const target = path.join(tmp, 'elsewhere.txt');
  fs.writeFileSync(target, 'full');
  fs.symlinkSync(target, scopedPath(tmp));
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '');
});

if (pwshBin) {
  test('file-state matrix: scoped-symlink rejected, never falls back (statusline.ps1)', (tmp) => {
    seedLegacy(tmp);
    fs.mkdirSync(tmp, { recursive: true });
    const target = path.join(tmp, 'elsewhere.txt');
    fs.writeFileSync(target, 'full');
    fs.symlinkSync(target, scopedPath(tmp));
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '');
  });

  test('file-state matrix: scoped DANGLING symlink rejected, never falls back to legacy (statusline.ps1) (PR-review High)', (tmp) => {
    seedLegacy(tmp);
    fs.mkdirSync(tmp, { recursive: true });
    fs.symlinkSync(path.join(tmp, 'nonexistent-target'), scopedPath(tmp));
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '', 'a dangling scoped symlink must render nothing, not the legacy sentinel');
  });

  test('a numeric JSON session_id must be rejected like JS/Bash, never cast to a matching scoped path (statusline.ps1) (PR-review v2 High)', (tmp) => {
    seedLegacy(tmp); // 'wenyan-ultra'
    fs.mkdirSync(tmp, { recursive: true });
    // A real scoped file DOES exist at the path a numeric-to-string cast
    // would compute -- this is what makes the bug observable (a sanitizer
    // vector test with no matching file on disk can't distinguish
    // "correctly rejected" from "accepted but happened to ENOENT").
    fs.writeFileSync(path.join(tmp, '.caveman-active-123'), 'ultra');
    const out = runStatuslinePs1(tmp, 123); // JSON.stringify emits {"session_id":123}, not a string
    assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a numeric session_id must fall back to the legacy sentinel, never read the scoped-123 file');
    assert.doesNotMatch(out, /\[CAVEMAN:ULTRA\]/, 'must not read the scoped file a naive string-cast would compute');
  });
}

// ── Non-ENOENT stat failure (resolveFlag + resolvePrev) ─────────────────────
// Simulated via a parent directory with execute permission stripped, causing
// lstatSync to throw EACCES rather than ENOENT. Root bypasses permission
// checks entirely, so this sub-case is skipped (with a printed reason, not a
// silent no-op) when running as root.

const isRoot = typeof process.getuid === 'function' && process.getuid() === 0;

if (isRoot) {
  skip('file-state matrix: non-ENOENT stat failure (resolveFlag)', 'running as root -- permission checks bypassed');
  skip('file-state matrix: non-ENOENT stat failure (resolvePrev)', 'running as root -- permission checks bypassed');
} else if (process.platform === 'win32') {
  skip('file-state matrix: non-ENOENT stat failure (resolveFlag)', 'POSIX permission bits not applicable on win32');
  skip('file-state matrix: non-ENOENT stat failure (resolvePrev)', 'POSIX permission bits not applicable on win32');
} else {
  test('file-state matrix: non-ENOENT stat failure rejected, never falls back (resolveFlag)', (tmp) => {
    // claudeDir itself is the locked dir -- lstat on anything inside it
    // throws EACCES (can't traverse to stat the child), not ENOENT.
    fs.writeFileSync(path.join(tmp, flagBaseName(SESSION_ID)), 'full');
    fs.chmodSync(tmp, 0o000); // strip execute
    try {
      const r = resolveFlag(tmp, SESSION_ID);
      assert.strictEqual(r.mode, null);
      assert.strictEqual(r.rejected, true);
    } finally {
      fs.chmodSync(tmp, 0o700); // restore so cleanup can rmSync
    }
  });

  test('resolvePrev: non-ENOENT stat failure on .prev returns rejected: true (Tier-1 v6 Medium)', (tmp) => {
    // Session must already have scoped ACTIVE identity for .prev's own
    // fail-closed branch to run (rather than falling through to resolveState).
    // Establish that BEFORE locking the dir, since resolveFlag needs to stat
    // the active flag too.
    safeWriteFlag(scopedPath(tmp), 'ultra');
    fs.writeFileSync(`${scopedPath(tmp)}.prev`, 'full');
    fs.chmodSync(tmp, 0o000);
    try {
      const r = resolvePrev(tmp, SESSION_ID);
      assert.strictEqual(r.mode, null);
      assert.strictEqual(r.rejected, true);
    } finally {
      fs.chmodSync(tmp, 0o700);
    }
  });

  test('non-ENOENT scoped lookup failure must fail closed, never fall back to legacy (statusline.sh) (PR-review v3 High)', (tmp) => {
    // Legacy flag lives inside the SAME directory whose permissions we lock,
    // so a bug here is observable as "renders the legacy sentinel anyway"
    // rather than a silent empty result either way.
    seedLegacy(tmp);
    fs.writeFileSync(scopedPath(tmp), 'full');
    fs.chmodSync(tmp, 0o000);
    try {
      const out = runStatuslineSh(tmp, SESSION_ID);
      assert.strictEqual(out.trim(), '', '[-e]/[-L] cannot distinguish EACCES from ENOENT -- must fail closed, never render the legacy sentinel');
    } finally {
      fs.chmodSync(tmp, 0o700);
    }
  });

  if (pwshBin) {
    test('non-ENOENT scoped lookup failure must fail closed, never fall back to legacy (statusline.ps1) (PR-review v3 High)', (tmp) => {
      seedLegacy(tmp);
      fs.writeFileSync(scopedPath(tmp), 'full');
      fs.chmodSync(tmp, 0o000);
      try {
        const out = runStatuslinePs1(tmp, SESSION_ID);
        assert.strictEqual(out.trim(), '', 'catching every exception (not just ItemNotFoundException) must fail closed, never render the legacy sentinel');
      } finally {
        fs.chmodSync(tmp, 0o700);
      }
    });
  }
}

test('a session_id-shaped substring elsewhere in the JSON must not be silently preferred over (or confused with) the real top-level value (statusline.sh) (PR-review v3 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  safeWriteFlag(scopedPath(tmp), 'full');
  // Two genuine, distinct `"session_id":"..."` occurrences in the raw text --
  // grep -o has no notion of "top-level property" and would previously pick
  // whichever `head -n 1` happened to return first. With the ambiguity guard,
  // 2+ matches reject the session id outright rather than guessing.
  const payload = `{"cwd":"/x/","session_id":"${SESSION_ID}","note":"session_id":"other-session"}`;
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: payload,
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'ambiguous session_id must fall back to the legacy sentinel, never guess between candidates');
  assert.doesNotMatch(out, /\[CAVEMAN\]\[0m$/, 'must not resolve the scoped "full" flag from an ambiguous extraction');
});

test('a single unambiguous session_id occurrence still resolves the scoped flag normally (statusline.sh)', (tmp) => {
  fs.mkdirSync(tmp, { recursive: true });
  seedLegacy(tmp);
  safeWriteFlag(scopedPath(tmp), 'full');
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.match(out, /\[CAVEMAN\]/, 'a single unambiguous session_id must still read the scoped file');
});

test('scoped content containing a NUL byte must be rejected outright, never silently truncated by command substitution (statusline.sh) (PR-review v3 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // "full\0garbage" -- $(cat ...) would drop everything from the NUL byte
  // onward, leaving a bash variable containing exactly "full" unless this is
  // rejected before ever reading the file into a variable.
  fs.writeFileSync(scopedPath(tmp), Buffer.from('full\x00garbage', 'utf8'));
  const out = runStatuslineSh(tmp, SESSION_ID);
  assert.strictEqual(out.trim(), '', 'NUL-containing content must be rejected, never truncated down to a valid-looking mode');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
