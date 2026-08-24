package gitsafe

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCommandOverridesRepositoryControlledConfig(t *testing.T) {
	cmd := Command(context.Background(), "/tmp/repo", "status")
	args := cmd.Args
	for _, required := range []string{"core.fsmonitor=false", "core.hooksPath=/dev/null", "protocol.ext.allow=never", "diff.external="} {
		if !slices.Contains(args, required) {
			t.Fatalf("missing hardening override %q in %v", required, args)
		}
	}
	if args[len(args)-1] != "status" || !slices.Contains(args, "/tmp/repo") {
		t.Fatalf("caller arguments did not survive hardening: %v", args)
	}
}

func TestCommandDropsInheritedGitEnvButKeepsSystemConfig(t *testing.T) {
	t.Setenv("GIT_DIR", "/somewhere/else/.git")
	cmd := Command(context.Background(), "/tmp/repo", "status")
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "GIT_DIR=") {
			t.Fatalf("inherited GIT_DIR reached git: %q", entry)
		}
		// System config is root-owned, so it is not part of this threat, and
		// skipping it discards org-wide safe.directory allowances — which turns
		// `git status` into a "dubious ownership" failure on shared machines and
		// silently costs us repository state.
		if strings.HasPrefix(entry, "GIT_CONFIG_NOSYSTEM=") {
			t.Fatalf("system git config must stay readable: %q", entry)
		}
	}
}
