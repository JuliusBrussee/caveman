//go:build !windows

// Package securehome keeps CAVEMAN_HOME private to the user who owns it.
package securehome

// Restrict is a no-op off Windows, where the 0700 mode already does the job.
func Restrict(string) error { return nil }
