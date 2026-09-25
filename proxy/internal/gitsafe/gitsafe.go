// Package gitsafe runs git against a repository whose contents are not
// trusted. Every git invocation that points at a user's working directory
// belongs here: a repository's own .git/config may name programs git will run
// — core.fsmonitor runs while the index is read or refreshed, so `ls-files`
// and `status` are each enough — which would let merely opening a session
// inside a freshly cloned untrusted repository execute code.
package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// hardened overrides every repository-config knob that names a program git
// would execute. Command-line -c beats repository config; never drop these to
// "simplify" an invocation.
var hardened = []string{
	"--no-pager",
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.sshCommand=",
	"-c", "core.askPass=",
	"-c", "core.editor=true",
	"-c", "core.pager=cat",
	"-c", "core.alternateRefsCommand=",
	"-c", "diff.external=",
	"-c", "credential.helper=",
	"-c", "protocol.ext.allow=never",
	"-c", "uploadpack.packObjectsHook=",
}

// Command builds a git invocation rooted at root that the repository cannot
// steer. Inherited GIT_* variables are dropped so an outer environment cannot
// redirect the index, config, or work tree either.
//
// System config (/etc/gitconfig) is deliberately still read. It is root-owned,
// so it is not part of the threat this guards, and the -c overrides above beat
// it anyway — while skipping it discards org-wide `safe.directory` allowances,
// which turns `git status` into a "dubious ownership" failure on shared and CI
// machines and silently costs us repository state. Do not add
// GIT_CONFIG_NOSYSTEM back.
func Command(ctx context.Context, root string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(hardened)+len(args)+2)
	full = append(full, "-C", root)
	full = append(full, hardened...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return cmd
}
