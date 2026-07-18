//go:build windows

package main

import (
	"os"
	"os/exec"
)

// Windows does not support Unix-style exec. Start the same command and then
// exit the old process; the Windows service wrapper can track the new child.
func restartProcessInPlace(argv0 string, argv, envv []string) error {
	cmd := exec.Command(argv0, argv[1:]...)
	cmd.Env = envv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
