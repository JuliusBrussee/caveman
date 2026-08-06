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

// Resolve a binary's real path without depending on any specific `which`
// location (T1 v11 Nit: hardcoded /usr/bin/which is absent on some Linux
// distros) — search $PATH directly, same lookup `command -v` would do.
function resolveBin(name) {
  const dirs = (process.env.PATH || '').split(path.delimiter);
  for (const d of dirs) {
    const candidate = path.join(d, name);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return candidate;
    } catch (_) { /* try next PATH entry */ }
  }
  return null;
}

// Builds a symlink farm containing exactly the binaries statusline.sh needs
// (awk, cat, wc, tr, od, grep, head, bash), plus python3 only when
// `includePython3` is true — so both the python3 branch and the frozen awk
// fallback branch can be forced deterministically instead of depending on
// whatever happens to be ambient on PATH (T1 v11 Low: the renamed
// "python3 path" tests relied on ambient python3, which CI never
// explicitly guarantees for the node job). Farms are cached and cleaned up
// on process exit (T1 v11 Nit: the original helper leaked its temp dir).
const pathFarmCache = new Map();
const pathFarmDirs = [];
function buildPathFarm(includePython3) {
  const key = includePython3 ? 'with-python3' : 'without-python3';
  if (pathFarmCache.has(key)) return pathFarmCache.get(key);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), `caveman-${key}-bin-`));
  const bins = ['bash', 'awk', 'cat', 'wc', 'tr', 'od', 'grep', 'head'];
  if (includePython3) bins.push('python3');
  for (const bin of bins) {
    const real = resolveBin(bin);
    if (!real) throw new Error(`buildPathFarm: could not resolve "${bin}" on PATH`);
    fs.symlinkSync(real, path.join(dir, bin));
  }
  pathFarmCache.set(key, dir);
  pathFarmDirs.push(dir);
  return dir;
}
process.on('exit', () => {
  for (const dir of pathFarmDirs) {
    try { fs.rmSync(dir, { recursive: true, force: true }); } catch (_) { /* best-effort cleanup */ }
  }
});
function pathWithoutPython3() { return buildPathFarm(false); }
function pathWithPython3Forced() { return buildPathFarm(true); }

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

  test('a differently-cased "session_id" JSON key must be treated as absent, matching JS/Bash case-sensitive property access (statusline.ps1) (PR-review v5 High)', (tmp) => {
    seedLegacy(tmp); // 'wenyan-ultra'
    fs.mkdirSync(tmp, { recursive: true });
    // PowerShell's dot-notation property access ($Data.session_id) is
    // case-INSENSITIVE by default, so a payload using any other casing
    // (e.g. "Session_Id") would previously resolve here even though
    // JS's JSON.parse(...).session_id and Bash's structural walker are
    // both case-sensitive and would treat the same payload as having no
    // session_id at all. A real scoped file exists at the path the
    // wrongly-cased key would compute to, so this is observable.
    fs.writeFileSync(path.join(tmp, '.caveman-active-case-mismatch'), 'ultra');
    const out = execFileSync(pwshBin, ['-NoProfile', '-File', STATUSLINE_PS1], {
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
      input: '{"Session_Id":"case-mismatch"}',
    });
    assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a differently-cased session_id key must fall back to the legacy sentinel');
    assert.doesNotMatch(out, /\[CAVEMAN:ULTRA\]/, 'must not read the scoped file a case-insensitive property match would compute');
  });

  test('the correctly-cased "session_id" key still resolves normally after the case-sensitive fix (statusline.ps1)', (tmp) => {
    fs.mkdirSync(tmp, { recursive: true });
    seedLegacy(tmp);
    safeWriteFlag(path.join(tmp, '.caveman-active-case-ok'), 'full');
    const out = execFileSync(pwshBin, ['-NoProfile', '-File', STATUSLINE_PS1], {
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
      input: '{"session_id":"case-ok"}',
    });
    assert.match(out, /\[CAVEMAN\]/, 'a correctly-cased session_id must still resolve the scoped flag');
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

test('a genuinely NESTED session_id occurrence must never be selected over (or in place of) the real top-level value (statusline.sh) (PR-review v3 High, superseded parser: v4 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  safeWriteFlag(scopedPath(tmp), 'full');
  // A genuinely nested "session_id" key (inside "meta") plus the real
  // top-level one -- the top-level value must win, and the nested one must
  // never even be considered a candidate.
  const payload = `{"cwd":"/x/","session_id":"${SESSION_ID}","meta":{"session_id":"other-session"}}`;
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: payload,
  });
  assert.match(out, /\[CAVEMAN\]/, 'the real top-level session_id must resolve the scoped flag');
  assert.doesNotMatch(out, /WENYAN/, 'must not fall back to legacy just because a nested duplicate key exists');
});

test('a session_id key found ONLY nested (absent at the top level) must resolve like no session_id was sent at all (statusline.sh) (PR-review v4 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // Only a NESTED "session_id" exists; there is no top-level one at all.
  // A scoped file exists at the path the nested value would compute to --
  // this is what makes the bug observable (without it, "correctly ignored"
  // and "accepted but happened to ENOENT" look identical).
  fs.writeFileSync(scopedPath(tmp), 'ultra');
  const payload = '{"cwd":"/x/","meta":{"session_id":"' + SESSION_ID + '"}}';
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: payload,
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a session_id that only exists nested must never be treated as the top-level value');
});

test('a non-string top-level session_id must not be shadowed by a genuinely nested string session_id (statusline.sh) (PR-review v4 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // Reviewer's exact v4 exploit: {"session_id":123,"meta":{"session_id":"other-session"}}.
  // A match-count-based ambiguity guard sees exactly ONE quoted candidate
  // (the nested one -- 123 isn't quoted) and would wrongly select it.
  fs.writeFileSync(path.join(tmp, '.caveman-active-other-session'), 'lite');
  const payload = '{"session_id":123,"meta":{"session_id":"other-session"}}';
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: payload,
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a numeric top-level session_id must fall back to legacy, never adopt a nested string value');
  assert.doesNotMatch(out, /\[CAVEMAN:LITE\]/, 'must not read the scoped file the nested value would compute');
});

test('a top-level session_id of null/bool/array is rejected exactly like a number (statusline.sh) (PR-review v4 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  for (const value of ['null', 'true', '["x"]', '{"a":1}']) {
    const out = execFileSync('bash', [STATUSLINE_SH], {
      encoding: 'utf8',
      env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
      input: `{"session_id":${value}}`,
    });
    assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, `a top-level session_id of ${value} must fall back to legacy`);
  }
});

test('a JSON-escaped session_id value must never resolve the WRONG (undecoded) scoped path (statusline.sh) (PR-review v6/v10)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // {"session_id":"a"} decodes to "a" in JS/PowerShell (real JSON
  // parsers). Pre-python3-migration (v6), the awk walker never decoded
  // escapes and rejected the value outright rather than risk resolving the
  // wrongly-undecoded "u0061" path. Post-migration (v10), when python3 is
  // available it decodes correctly via a real parser and resolves "a" --
  // matching JS/PowerShell exactly, which is strictly better than the old
  // reject-on-any-escape behavior. Either way, the wrongly-undecoded
  // "u0061" path must never be the one selected.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  fs.writeFileSync(path.join(tmp, '.caveman-active-u0061'), 'ultra');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"\\u0061"}',
  });
  assert.doesNotMatch(out, /\[CAVEMAN:ULTRA\]/, 'must never resolve the wrongly-undecoded "u0061" scoped path');
  assert.ok(
    /\[CAVEMAN:WENYAN-ULTRA\]/.test(out) || /\[CAVEMAN:LITE\]/.test(out),
    'must either fall back to legacy (no python3) or correctly decode to "a" (python3 present), never anything else'
  );
});

test('a genuinely unescaped session_id value with the same characters still resolves normally (statusline.sh)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a"}',
  });
  assert.match(out, /\[CAVEMAN:LITE\]/, 'a plain, unescaped value must still resolve the scoped flag normally');
});

test('truncated/malformed JSON must not still yield a usable session_id (statusline.sh) (PR-review v6 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // JS's JSON.parse and PowerShell's ConvertFrom-Json both throw on this
  // (missing closing brace) and fall back to legacy. The Bash walker
  // previously stopped scanning as soon as it found a plausible value,
  // without checking whether the rest of the document was well-formed.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a"', // missing closing brace
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'truncated JSON must fall back to legacy, matching a real parser throwing on malformed input');
});

test('a mismatched closing bracket type must be rejected, not treated as balancing the depth (statusline.sh) (PR-review v8 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // {"session_id":"a"]  -- opened with { but closed with ], the WRONG
  // bracket type. A depth counter that doesn't track WHICH bracket
  // opened each level sees depth return to 0 and wrongly accepts this,
  // even though JS/PowerShell reject mismatched delimiters outright.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a"]',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a mismatched closing bracket must fall back to legacy, matching a real parser rejecting invalid JSON');
});

test('trailing non-whitespace after the session_id value must be rejected (statusline.sh) (PR-review v8 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // {"session_id":"a" garbage}  -- arbitrary text between the value and
  // the next delimiter was previously skipped character-by-character
  // instead of being rejected as invalid JSON grammar.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a" garbage}',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'trailing garbage after the value must fall back to legacy, matching a real parser rejecting invalid JSON');
});

test('a missing value for an unrelated key must be rejected (statusline.sh, python3 path) (PR-review v9 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // {"session_id":"a","x":}  -- a missing value for a DIFFERENT key is
  // invalid JSON grammar. Forces the python3 path deterministically (T1
  // v11 Low: relying on ambient python3 meant a python3-less runner would
  // silently exercise the awk path instead and fail confusingly): real
  // json.load rejects the whole document, so extraction yields nothing and
  // legacy is rendered.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithPython3Forced(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a","x":}',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a missing member value elsewhere in the document must fall back to legacy');
});

test('a missing value for an unrelated key is NOT rejected by the frozen awk fallback (statusline.sh, no-python3 path) (T1 v10 High: awk fallback residual gap, not further hardened per design review)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // Known, accepted residual gap: the awk walker is FROZEN per the
  // trip-wire design review (no further hardening -- python3-primary
  // covers the correct-semantics case). With python3 absent, the walker
  // does NOT flag a missing value for an unrelated key as malformed, so it
  // resolves "a" and the SCOPED flag renders -- diverging from
  // JSON.parse/ConvertFrom-Json. This test documents the actual behavior
  // so "68 tests pass" never again implies the awk fallback was verified
  // to reject this input; it wasn't, and per design it's not going to be.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithoutPython3(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a","x":}',
  });
  assert.match(out, /\[CAVEMAN:LITE\]/, 'documents the frozen awk fallback resolving "a" despite the missing member value elsewhere -- accepted residual, not a regression target');
});

test('a trailing comma before the closing brace must be rejected (statusline.sh, python3 path) (PR-review v9 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithPython3Forced(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a",}',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a trailing comma before the closing brace must fall back to legacy, matching real JSON grammar');
});

test('a trailing comma before the closing brace is NOT rejected by the frozen awk fallback (statusline.sh, no-python3 path) (T1 v10 High: awk fallback residual gap, not further hardened per design review)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // Same known/accepted residual class as the missing-value case above.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithoutPython3(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a",}',
  });
  assert.match(out, /\[CAVEMAN:LITE\]/, 'documents the frozen awk fallback resolving "a" despite the trailing comma -- accepted residual, not a regression target');
});

test('duplicate top-level session_id keys must resolve like JSON.parse/ConvertFrom-Json (last occurrence wins) (statusline.sh, python3 path) (PR-review v9 High)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // {"session_id":"a","session_id":"../bad"} is a WELL-FORMED, RFC-8259-
  // compliant payload. Both JSON.parse and ConvertFrom-Json resolve
  // duplicate keys to the LAST occurrence (verified empirically), so the
  // real session_id here is "../bad" -- which fails the sanitizer and
  // falls back to legacy. Forces the python3 path deterministically.
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithPython3Forced(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a","session_id":"../bad"}',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'duplicate top-level session_id keys must resolve to the LAST occurrence, which then fails the sanitizer and falls back to legacy');
  assert.doesNotMatch(out, /\[CAVEMAN:LITE\]/, 'must not resolve the scoped flag from the FIRST duplicate occurrence');
});

test('duplicate top-level session_id keys resolve FIRST-wins in the frozen awk fallback, diverging from real JSON.parse (statusline.sh, no-python3 path) (T1 v10 High: awk fallback residual gap, not further hardened per design review)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // Known, accepted residual gap: with python3 absent, the awk walker's
  // `if (found == "none")` value-assignment guard keeps the FIRST
  // occurrence of a top-level session_id key, not the last. On this
  // well-formed input the walker resolves "a" (the first occurrence) and
  // renders the SCOPED flag, where a real JSON parser would resolve
  // "../bad" (the last occurrence) and fail the sanitizer into legacy.
  // The awk walker is frozen per the trip-wire design review -- this is
  // documented, not silently reintroduced as "fixed".
  fs.writeFileSync(path.join(tmp, '.caveman-active-a'), 'lite');
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, PATH: pathWithoutPython3(), CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a","session_id":"../bad"}',
  });
  assert.match(out, /\[CAVEMAN:LITE\]/, 'documents the frozen awk fallback resolving the FIRST duplicate occurrence, not the last -- accepted residual, not a regression target');
});

test('a session_id value containing an embedded NUL byte is rejected before it ever reaches shell capture (statusline.sh) (PR-review v10 High/design)', (tmp) => {
  seedLegacy(tmp);
  fs.mkdirSync(tmp, { recursive: true });
  // A JSON-escaped \u0000 decodes to a literal NUL character. Some bash
  // builds preserve a NUL through $(...) command substitution; the
  // actual macOS default /bin/bash (3.2.57, what "#!/bin/bash" resolves
  // to) silently drops it, splicing the surrounding bytes together --
  // the same root cause as the v3 flag-content NUL finding, just at the
  // JSON-extraction boundary instead. The python3 path sidesteps this
  // entirely by validating the FULL decoded string against the safe
  // charset (which by definition can never contain NUL) before ever
  // writing to stdout; the frozen awk fallback also rejects it (any
  // escape sequence in the value is rejected outright there).
  fs.writeFileSync(path.join(tmp, '.caveman-active-ab'), 'lite'); // the path a naive NUL-dropping capture would compute
  const out = execFileSync('bash', [STATUSLINE_SH], {
    encoding: 'utf8',
    env: { ...process.env, CLAUDE_CONFIG_DIR: tmp, CAVEMAN_STATUSLINE_SAVINGS: '0' },
    input: '{"session_id":"a\\u0000b"}',
  });
  assert.match(out, /\[CAVEMAN:WENYAN-ULTRA\]/, 'a NUL-containing decoded value must be rejected entirely and fall back to legacy');
  assert.doesNotMatch(out, /\[CAVEMAN:LITE\]/, 'must never resolve the scoped path a NUL-dropping shell capture would compute ("ab")');
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

if (pwshBin) {
  test('scoped content containing a NUL byte must be rejected outright (statusline.ps1) (invariant-matrix confirmation, no fix needed)', (tmp) => {
    // Unlike Bash's $(...) command substitution, a .NET string CAN hold an
    // embedded NUL character, and Get-Content -Raw preserves it -- so this
    // was never actually reachable in PowerShell. Asserted here as a
    // confirming regression test (not a fix) so this invariant is verified
    // by the suite instead of relying on ad-hoc manual confirmation.
    seedLegacy(tmp);
    fs.mkdirSync(tmp, { recursive: true });
    fs.writeFileSync(scopedPath(tmp), Buffer.from('full\x00garbage', 'utf8'));
    const out = runStatuslinePs1(tmp, SESSION_ID);
    assert.strictEqual(out.trim(), '', 'NUL-containing content must be rejected in PowerShell too');
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
