//go:build linux && !js

package ccr

import (
	"os"
	"path/filepath"
	"runtime"
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
	if onAndroid() {
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
	if onAndroid() {
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

// Codex scrubs the MCP server environment, so ANDROID_ROOT alone misses
// caveman-mcp on Termux; the system image marker must still be detected.
func TestDetectAndroidFromBuildProp(t *testing.T) {
	t.Setenv("ANDROID_ROOT", "")
	original := androidBuildProp
	t.Cleanup(func() { androidBuildProp = original })

	androidBuildProp = filepath.Join(t.TempDir(), "build.prop")
	if err := os.WriteFile(androidBuildProp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !detectAndroid() {
		t.Fatal("build.prop present with ANDROID_ROOT unset: want Android detected")
	}
	if runtime.GOOS == "android" {
		return
	}
	androidBuildProp = filepath.Join(t.TempDir(), "missing")
	if detectAndroid() {
		t.Fatal("no build.prop, no ANDROID_ROOT: want not Android")
	}
}

// On Android the fchmodat2 syscall itself is fatal (SIGSYS), so the gate must
// skip it outright and go straight to procfs.
func TestChmodSQLiteFileSkipsFchmodat2OnAndroid(t *testing.T) {
	originalAndroid, originalFchmodat := onAndroid, fchmodatEmptyPath
	t.Cleanup(func() { onAndroid, fchmodatEmptyPath = originalAndroid, originalFchmodat })
	onAndroid = func() bool { return true }
	fchmodatEmptyPath = func(int) error {
		t.Fatal("fchmodat2 called on Android")
		return nil
	}

	path := filepath.Join(t.TempDir(), "ccr.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSQLitePath(path); err != nil {
		t.Fatalf("prepare on Android: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode %v, want -rw-------", perm)
	}
}
