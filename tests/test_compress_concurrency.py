"""Tests for the cross-session compress lock — without it, two concurrent caveman-compress runs on the same file interleave reads/writes and silently corrupt output; file_lock serializes access per (parent-dir-name, stem), matching backup_dir_for's own collision domain."""

import errno
import os
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402


class LockPathTests(unittest.TestCase):
    def test_relative_and_resolved_spellings_of_same_file_yield_same_lock_path(self):
        with tempfile.TemporaryDirectory() as data_home, tempfile.TemporaryDirectory() as tmp:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target_dir = Path(tmp) / "sub"
                target_dir.mkdir()
                (target_dir / "task.md").write_text("x")
                resolved = (target_dir / "task.md").resolve()
                relative_cwd = os.getcwd()
                try:
                    os.chdir(tmp)
                    via_relative = compress_mod.lock_path_for(Path("sub/task.md"))
                finally:
                    os.chdir(relative_cwd)
                self.assertEqual(via_relative, compress_mod.lock_path_for(resolved))

    def test_same_basename_different_dirs_yields_different_lock_paths(self):
        # repo-a and repo-b differ in parent-dir name, so their backup paths differ too — only files whose backup path would collide should share a lock.
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                a = compress_mod.lock_path_for(Path("/repo-a/CLAUDE.md"))
                b = compress_mod.lock_path_for(Path("/repo-b/CLAUDE.md"))
                self.assertNotEqual(a, b)

    def test_different_stems_in_same_parent_dir_yield_different_lock_paths(self):
        # Negative case for the domain fix: same parent-dir name alone must not over-coarsen the key — two distinct files in the same directory still need separate locks.
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                a = compress_mod.lock_path_for(Path("/repo-a/docs/task.md"))
                b = compress_mod.lock_path_for(Path("/repo-a/docs/other.md"))
                self.assertNotEqual(a, b)

    def test_same_parent_dir_name_and_stem_share_lock_path(self):
        # backup_dir_for keys its own collision guard on parent-dir-name + stem only (not the full path); the lock must share that domain or both processes can pass the "backup already exists" guard before either has written. task.md vs task.txt is the sharpest real case: same stem, same backup filename, different source extension.
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                a = compress_mod.lock_path_for(Path("/repo-a/docs/task.md"))
                b = compress_mod.lock_path_for(Path("/repo-b/docs/task.txt"))
                self.assertEqual(a, b)
                backup_a = compress_mod.backup_dir_for(Path("/repo-a/docs/task.md")) / "task.original.md"
                backup_b = compress_mod.backup_dir_for(Path("/repo-b/docs/task.txt")) / "task.original.md"
                self.assertEqual(backup_a, backup_b)


class FileLockTests(unittest.TestCase):
    def test_second_acquire_waits_until_first_releases(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                acquired_second_at = []
                released_first_at = []

                def hold_first():
                    with compress_mod.file_lock(target):
                        time.sleep(0.3)
                        released_first_at.append(time.monotonic())

                def try_second():
                    time.sleep(0.05)  # let the first thread acquire first
                    with mock.patch.object(compress_mod, "LOCK_POLL_INTERVAL", 0.02):
                        with compress_mod.file_lock(target):
                            acquired_second_at.append(time.monotonic())

                t1 = threading.Thread(target=hold_first, daemon=True)
                t2 = threading.Thread(target=try_second, daemon=True)
                with mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 2):  # a lock regression should fail this test in ~2s, not block the run for the real 900s default
                    t1.start()
                    t2.start()
                    t1.join(timeout=5)
                    t2.join(timeout=5)
                self.assertFalse(t1.is_alive())
                self.assertFalse(t2.is_alive())

                self.assertEqual(len(released_first_at), 1)
                self.assertEqual(len(acquired_second_at), 1)
                # The second lock must not be acquired before the first is released — this is the exact race that let two sessions interleave writes.
                self.assertGreaterEqual(acquired_second_at[0], released_first_at[0])

    def test_lock_file_persists_but_is_unlocked_after_release(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                lock_path = compress_mod.lock_path_for(target)
                with compress_mod.file_lock(target):
                    self.assertTrue(lock_path.exists())
                # OS-native locks are held on the open file description, not the file's existence — the marker file itself is never deleted.
                self.assertTrue(lock_path.exists())
                start = time.monotonic()
                with compress_mod.file_lock(target):
                    pass
                self.assertLess(time.monotonic() - start, 1)

    def test_crashed_holder_lock_released_by_os_on_close(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                lock_path = compress_mod.lock_path_for(target)
                lock_path.parent.mkdir(parents=True, exist_ok=True)
                fd = os.open(lock_path, os.O_CREAT | os.O_RDWR)
                os.write(fd, b"\0")
                os.lseek(fd, 0, 0)
                compress_mod._try_lock_nonblocking(fd)
                os.close(fd)  # simulates the holder process crashing/being killed without a clean unlock

                start = time.monotonic()
                with mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 30):
                    with compress_mod.file_lock(target):
                        pass
                # The OS releases the lock the instant the holder's fd closes — must succeed near-instantly, not wait out the budget.
                self.assertLess(time.monotonic() - start, 2)

    @unittest.skipIf(compress_mod._IS_WINDOWS, "O_NOFOLLOW is POSIX-only")
    def test_refuses_to_open_through_a_symlinked_lock_path(self):
        with tempfile.TemporaryDirectory() as data_home, tempfile.TemporaryDirectory() as tmp:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                lock_path = compress_mod.lock_path_for(target)
                lock_path.parent.mkdir(parents=True, exist_ok=True)
                decoy = Path(tmp) / "decoy"
                decoy.write_text("do not touch")
                lock_path.symlink_to(decoy)

                with self.assertRaises(OSError):
                    with compress_mod.file_lock(target):
                        pass  # pragma: no cover - must never be reached
                self.assertEqual(decoy.read_text(), "do not touch")

    @unittest.skipIf(compress_mod._IS_WINDOWS, "creating dir symlinks needs elevated privilege on Windows")
    def test_refuses_when_lock_dir_itself_is_a_symlink(self):
        with tempfile.TemporaryDirectory() as data_home, tempfile.TemporaryDirectory() as tmp:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                lock_dir = compress_mod.lock_path_for(target).parent
                decoy_dir = Path(tmp) / "decoy_dir"
                decoy_dir.mkdir()
                lock_dir.parent.mkdir(parents=True, exist_ok=True)
                lock_dir.symlink_to(decoy_dir)

                with self.assertRaises(OSError):
                    with compress_mod.file_lock(target):
                        pass  # pragma: no cover - must never be reached
                self.assertEqual(list(decoy_dir.iterdir()), [])

    def test_fresh_lock_not_stolen_and_times_out(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                held = threading.Event()
                release = threading.Event()

                def hold():
                    with compress_mod.file_lock(target):
                        held.set()
                        release.wait(timeout=5)

                holder = threading.Thread(target=hold, daemon=True)
                holder.start()
                held.wait(timeout=5)
                try:
                    with mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 0.2), \
                         mock.patch.object(compress_mod, "LOCK_POLL_INTERVAL", 0.02):
                        with self.assertRaises(compress_mod.LockTimeoutError):
                            with compress_mod.file_lock(target):
                                pass  # pragma: no cover - must never be reached
                finally:
                    release.set()
                    holder.join(timeout=5)
                self.assertFalse(holder.is_alive())

    def test_lock_released_on_exception_inside_block(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                with self.assertRaises(ValueError):
                    with compress_mod.file_lock(target):
                        raise ValueError("boom")
                start = time.monotonic()
                with compress_mod.file_lock(target):
                    pass
                self.assertLess(time.monotonic() - start, 1)

    def test_filesystem_without_lock_support_degrades_to_unlocked(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                entered = []
                with mock.patch.object(compress_mod, "_try_lock_nonblocking", side_effect=OSError(errno.EOPNOTSUPP, "Operation not supported")), \
                     mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 2), \
                     mock.patch.object(compress_mod, "LOCK_POLL_INTERVAL", 0.02):  # matches sibling lock tests: a misclassification regression should fail this fast, not spin out the real 900s budget
                    with compress_mod.file_lock(target):
                        entered.append(True)  # must still reach the critical section instead of raising
                self.assertEqual(entered, [True])

    def test_enolck_is_not_treated_as_unsupported_and_still_raises(self):
        # ENOLCK is not a reliable "this filesystem has no locking" signal (it can also mean transient kernel lock-record exhaustion) — treating it as one could let a genuinely contended lock proceed unlocked.
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                with mock.patch.object(compress_mod, "_try_lock_nonblocking", side_effect=OSError(errno.ENOLCK, "No locks available")), \
                     mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 2), \
                     mock.patch.object(compress_mod, "LOCK_POLL_INTERVAL", 0.02):  # matches sibling lock tests: a misclassification regression should fail this fast, not spin out the real 900s budget
                    with self.assertRaises(OSError) as ctx:
                        with compress_mod.file_lock(target):
                            pass  # pragma: no cover - must never be reached
                self.assertEqual(ctx.exception.errno, errno.ENOLCK)

    @unittest.skipIf(compress_mod._IS_WINDOWS, "POSIX permission bits only")
    def test_lock_dir_is_chmod_0700(self):
        with tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                target = Path("/tmp/whatever/CLAUDE.md")
                with compress_mod.file_lock(target):
                    pass
                lock_dir = compress_mod.lock_path_for(target).parent
                self.assertEqual(lock_dir.stat().st_mode & 0o777, 0o700)


class CompressFileLockIntegrationTests(unittest.TestCase):
    def test_concurrent_compress_calls_serialize_instead_of_interleaving(self):
        # Two threads on the SAME file used to interleave; the lock instead serializes them so exactly one call_claude runs.
        with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                original = "# Heading\n\nProse to compress, long enough to pass the identity check here.\n"
                compressed = "# Heading\n\nProse.\n"
                path = Path(tmp) / "task.md"
                path.write_text(original, encoding="utf-8")

                lock_path = compress_mod.lock_path_for(path.resolve())
                call_starts = []

                def slow_claude(prompt):
                    call_starts.append(time.monotonic())
                    time.sleep(0.2)
                    return compressed

                valid = mock.Mock(is_valid=True, errors=[], warnings=[])
                results = []

                def run():
                    results.append(compress_mod.compress_file(path))

                t1 = threading.Thread(target=run, daemon=True)
                t2 = threading.Thread(target=run, daemon=True)
                # Patched once here, not inside run(): two threads each entering their own mock.patch.object on the same attribute unpatch in start order, not entry order, so the first thread's exit would restore the real call_claude/validate while the other thread is still mid-flight.
                with mock.patch.object(compress_mod, "call_claude", side_effect=slow_claude), \
                     mock.patch.object(compress_mod, "validate", return_value=valid), \
                     mock.patch.object(compress_mod, "LOCK_WAIT_SECONDS", 2), \
                     mock.patch.object(compress_mod, "LOCK_POLL_INTERVAL", 0.02):  # a lock regression should fail this test in ~2s, not block the run for the real 900s default; a 1s poll interval would leave only two attempts in that budget on a loaded CI box
                    t1.start()
                    time.sleep(0.05)  # ensure t1 acquires the lock first
                    t2.start()
                    t1.join(timeout=10)
                    t2.join(timeout=10)
                    # Asserted inside the patch scope so a failure here is reported before the later assertions run, while the patched state is still the one that produced it.
                    self.assertFalse(t1.is_alive())
                    self.assertFalse(t2.is_alive())

                # Exactly one compression ran — the lock stopped the second thread from touching the file while the first was mid-flight.
                self.assertEqual(len(call_starts), 1)
                self.assertEqual(len(results), 2)
                self.assertEqual(sorted(results), [False, True])
                self.assertEqual(path.read_text(encoding="utf-8"), compressed)
                self.assertTrue(lock_path.exists())

    def test_rejected_input_leaves_no_lock_file_behind(self):
        with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory() as data_home:
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
                sensitive = Path(tmp) / "id_rsa"
                sensitive.write_text("fake key material")
                with self.assertRaises(ValueError):
                    compress_mod.compress_file(sensitive)
                self.assertFalse(compress_mod._state_base_dir("locks").exists())

                missing = Path(tmp) / "nope.md"
                with self.assertRaises(FileNotFoundError):
                    compress_mod.compress_file(missing)
                self.assertFalse(compress_mod._state_base_dir("locks").exists())


if __name__ == "__main__":
    unittest.main()
