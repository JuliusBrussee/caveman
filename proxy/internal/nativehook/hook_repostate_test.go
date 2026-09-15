package nativehook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

// `git status` refreshes the index, which runs core.fsmonitor. It is the git
// call this process makes most often against a repository it did not write, so
// a session started inside a freshly cloned hostile repo would execute the
// repository's own program before the user did anything.
func TestRepositoryStateIgnoresRepositoryControlledCommands(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "executed")
	hook := filepath.Join(root, "fsmonitor-hook.sh")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+sentinel+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Hook", "GIT_AUTHOR_EMAIL=hook@example.test", "GIT_COMMITTER_NAME=Hook", "GIT_COMMITTER_EMAIL=hook@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "source")
	git("config", "core.fsmonitor", hook)
	git("config", "core.hooksPath", root)

	currentRepositoryState(context.Background(), root)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("repository-controlled command executed while reading repository state")
	}
}
