//go:build linux && !js

package ccr

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Every CI runner has fchmodat2(2), so chmodSQLiteFile always returns on its
// first branch here and the procfs fallback is never executed — yet the
// fallback is the branch that ships to every pre-6.6 kernel still in support
// (RHEL 9 on 5.14, Debian 12 and Amazon Linux 2023 on 6.1). Forcing the
// EOPNOTSUPP those kernels return runs the whole locking regression through it.
//
// The claim under test is not "chmod works" but "chmod without an ordinary
// descriptor keeps SQLite's POSIX locks": a chmod through the pinned
// /proc/self/fd link performs no open and no close of the database inode, so a
// short-lived consumer cannot decide it is the last connection and unlink the
// live writer's WAL.
func TestProcfsChmodFallbackPreservesLiveWriter(t *testing.T) {
	forceProcfsFallback(t)

	path := filepath.Join(t.TempDir(), "ccr.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	// The parent is what matters: the consumer subprocess does not inherit the
	// seam, which mirrors the real mixed fleet (an old proxy, a new CLI).
	request := lockingConsumerRequest{Path: path}
	for round := range 3 {
		data := []byte("procfs fallback round " + string(rune('0'+round)) + ": exact bytes\x00\xff\r\n")
		handle, err := writer.Put(Recovery{ContentType: "text", Compressor: "text", Original: data})
		if err != nil {
			t.Fatalf("round %d: write after consumer closed: %v", round, err)
		}
		object, err := writer.PutObject(Object{Type: ObjectCommandResult, SessionID: "procfs-regression", Data: data})
		if err != nil {
			t.Fatalf("round %d: write object after consumer closed: %v", round, err)
		}
		request.Recoveries = append(request.Recoveries, lockingRecovery{Handle: handle, Object: object, Data: data})
		runLockingConsumer(t, request)
	}
}

// The fallback must still do the job it exists for. A lock-preserving chmod
// that silently fails to tighten the mode would leave a database of prompts,
// credentials and tool results at the umask default.
func TestProcfsChmodFallbackTightensMode(t *testing.T) {
	forceProcfsFallback(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "ccr.db")
	if err := os.WriteFile(path, []byte("not yet secured"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err != nil {
		t.Fatalf("prepare via procfs fallback: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("procfs fallback left mode %v, want -rw-------", perm)
	}
}

// A symlink must not be followed on the fallback path either. chmodSQLiteFile
// is only reached after the caller's Lstat rejects a symlink, so this pins the
// caller's guard rather than the fallback's own O_NOFOLLOW.
func TestProcfsChmodFallbackRefusesSymlink(t *testing.T) {
	if os.Getenv("ANDROID_ROOT") != "" {
		t.Skip("Android O_PATH symlink semantics differ; covered on Linux")
	}
	forceProcfsFallback(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("someone else's file"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ccr.db")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err == nil {
		t.Fatal("PrepareSQLitePath followed a symlink; want refusal")
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("symlink target was chmodded to %v; want it untouched at -rw-r--r--", perm)
	}
}

// forceProcfsFallback makes fchmodat2 report the EOPNOTSUPP of a pre-6.6
// kernel for the duration of one test, and asserts the real syscall works here
// first — otherwise a future breakage of the primary path would hide behind a
// green fallback test.
func forceProcfsFallback(t *testing.T) {
	t.Helper()
	if os.Getenv("ANDROID_ROOT") != "" {
		return
	}
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(probe, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	realErr := fchmodatEmptyPath(fd)
	_ = unix.Close(fd)
	if realErr != nil {
		t.Skipf("fchmodat2 unavailable on this kernel (%v) — the fallback is already the live path", realErr)
	}

	original := fchmodatEmptyPath
	fchmodatEmptyPath = func(int) error { return unix.EOPNOTSUPP }
	t.Cleanup(func() { fchmodatEmptyPath = original })
}

// TestAndroidGateSkipsFchmodat2 pins the testable half of the Termux report in
// #1186. Termux builds report GOOS=linux, so a build tag cannot separate them,
// and Android's seccomp policy answers an unknown syscall with SIGSYS — which
// kills the process instead of returning an errno. That is why the existing
// EOPNOTSUPP/EINVAL check could not have caught it: there is no error to
// inspect, chmodSQLiteFile never returns, and opening the engine's store takes
// the whole process down.
//
// The SIGSYS itself is not reproducible off Android. What is reproducible, and
// what this asserts, is the gate's contract: with ANDROID_ROOT set,
// chmodSQLiteFile must not reach fchmodat2 at all, and must still tighten the
// mode through the procfs fallback. The spy stands in for the syscall that
// would be fatal there; without the gate it is called, and because the spy
// reports success the primary branch also returns early and leaves the file
// world-readable — so this fails two ways on an ungated build.
func TestAndroidGateSkipsFchmodat2(t *testing.T) {
	t.Setenv("ANDROID_ROOT", "/system")

	var called bool
	original := fchmodatEmptyPath
	fchmodatEmptyPath = func(int) error { called = true; return nil }
	t.Cleanup(func() { fchmodatEmptyPath = original })

	path := filepath.Join(t.TempDir(), "ccr.db")
	if err := os.WriteFile(path, []byte("not yet secured"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err != nil {
		t.Fatalf("prepare with ANDROID_ROOT set: %v", err)
	}
	if called {
		t.Error("fchmodat2 was reached with ANDROID_ROOT set; on Android that syscall raises SIGSYS and kills the process")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("Android procfs path left mode %v, want -rw-------", perm)
	}
}

// TestAndroidGateSkipsFchmodat2WithScrubbedEnvironment is the hole Devin Review
// found in the first version of this gate (#1196): detection read ANDROID_ROOT
// and nothing else, so a Termux launcher that scrubs the environment — plain
// `env -u ANDROID_ROOT` is enough — put the fatal syscall back in the path
// while looking like an ordinary Linux box. Being wrong this way kills the
// process, since SIGSYS is not an errno the caller can catch, so detection
// cannot rest on inherited environment alone. The filesystem markers cannot be
// unset by a launcher.
func TestAndroidGateSkipsFchmodat2WithScrubbedEnvironment(t *testing.T) {
	t.Setenv("ANDROID_ROOT", "")
	t.Setenv("ANDROID_DATA", "")

	marker := filepath.Join(t.TempDir(), "linker64")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	originalMarkers := androidMarkerPaths
	androidMarkerPaths = []string{marker}
	t.Cleanup(func() { androidMarkerPaths = originalMarkers })

	var called bool
	original := fchmodatEmptyPath
	fchmodatEmptyPath = func(int) error { called = true; return nil }
	t.Cleanup(func() { fchmodatEmptyPath = original })

	path := filepath.Join(t.TempDir(), "ccr.db")
	if err := os.WriteFile(path, []byte("not yet secured"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err != nil {
		t.Fatalf("prepare with ANDROID_ROOT scrubbed but an Android marker present: %v", err)
	}
	if called {
		t.Error("fchmodat2 was reached on Android with a scrubbed environment; SIGSYS there kills the process")
	}
	if perm := info(t, path).Mode().Perm(); perm != 0o600 {
		t.Fatalf("Android procfs path left mode %v, want -rw-------", perm)
	}
}

// The generosity above must not cost every ordinary Linux user the primary
// syscall: with no Android environment and no marker, fchmodat2 is still the
// path taken. Without this, a detector that answered true unconditionally would
// move all of Linux onto the fallback and no test would notice.
func TestOrdinaryLinuxStillUsesFchmodat2(t *testing.T) {
	t.Setenv("ANDROID_ROOT", "")
	t.Setenv("ANDROID_DATA", "")
	originalMarkers := androidMarkerPaths
	androidMarkerPaths = []string{filepath.Join(t.TempDir(), "absent")}
	t.Cleanup(func() { androidMarkerPaths = originalMarkers })

	if onAndroid() {
		t.Fatal("onAndroid() reported Android with no environment and no marker")
	}

	var called bool
	original := fchmodatEmptyPath
	fchmodatEmptyPath = func(fd int) error { called = true; return original(fd) }
	t.Cleanup(func() { fchmodatEmptyPath = original })

	path := filepath.Join(t.TempDir(), "ccr.db")
	if err := os.WriteFile(path, []byte("not yet secured"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err != nil {
		t.Fatalf("prepare on ordinary linux: %v", err)
	}
	if !called {
		t.Error("fchmodat2 was skipped on ordinary linux; the primary path must stay primary")
	}
	if perm := info(t, path).Mode().Perm(); perm != 0o600 {
		t.Fatalf("left mode %v, want -rw-------", perm)
	}
}

func info(t *testing.T, path string) os.FileInfo {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return stat
}
