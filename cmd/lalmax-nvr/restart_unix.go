//go:build !windows

package main

import "syscall"

// restartProcessInPlace replaces the current process on Unix. The PID stays
// unchanged, so scripts that track the binary with a pid file keep working.
func restartProcessInPlace(argv0 string, argv, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
