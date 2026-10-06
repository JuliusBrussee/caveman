//go:build linux && !js

package ccr

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// fchmodatEmptyPath tightens the inode behind an O_PATH descriptor via
// fchmodat2(2). It is a variable so a test can force the EOPNOTSUPP a
// pre-6.6 kernel returns: every CI runner has fchmodat2, so without a seam the
// procfs fallback below is never executed here and ships untested to the
// distributions that actually take it (RHEL 9, Debian 12, Amazon Linux 2023).
var fchmodatEmptyPath = func(fd int) error {
	return unix.Fchmodat(fd, "", 0o600, unix.AT_EMPTY_PATH)
}

// androidMarkerPaths are filesystem markers that exist on Android and on no
// ordinary Linux distribution. A variable so a test can point it at a temp file
// and exercise the detection without an Android device.
var androidMarkerPaths = []string{"/system/bin/linker64", "/system/bin/linker", "/system/build.prop"}

// onAndroid reports whether this GOOS=linux binary is in fact running on
// Android, where seccomp answers fchmodat2(2) with SIGSYS.
//
// Deliberately generous, because the two ways of being wrong do not cost the
// same. Guessing Android on an ordinary Linux box only takes the procfs
// fallback, which is a supported path that every pre-6.6 kernel already uses.
// Failing to spot Android kills the process: SIGSYS is not an errno, so there
// is nothing for the caller to catch and the engine never opens its store.
//
// ANDROID_ROOT alone is not enough for that reason — it is inherited
// environment, so a launcher that scrubs the environment (`env -u
// ANDROID_ROOT`) silently turns the fatal case back on. The filesystem markers
// are not inherited and cannot be unset.
func onAndroid() bool {
	for _, key := range []string{"ANDROID_ROOT", "ANDROID_DATA"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	for _, marker := range androidMarkerPaths {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func chmodSQLiteFile(path string, info os.FileInfo) error {
	// Closing an ordinary descriptor for the database or -shm drops ALL POSIX
	// locks held by SQLite in this process. O_PATH pins the inode without opening
	// it for I/O; closing this metadata-only descriptor does not release locks.
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fmt.Errorf("file changed while opening")
	}
	// Termux reports GOOS=linux but Android seccomp kills fchmodat2 with SIGSYS.
	if !onAndroid() {
		if err := fchmodatEmptyPath(fd); !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EINVAL) {
			return err
		}
	}
	// Kernels before fchmodat2/AT_EMPTY_PATH require procfs. This is the pinned
	// descriptor's kernel-controlled link, NOT the swappable database pathname.
	// chmod performs no open/close, so SQLite's locks remain intact. Fail closed
	// if procfs is unavailable; never fall back to opening the database for I/O.
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), 0o600)
}
