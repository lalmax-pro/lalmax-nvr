package transcode

import "os/exec"

// Fixed backends execute a single binary, never a shell or user-provided script.
// Windows Job Object process-tree containment remains a separate hardening item.
func configureProcess(cmd *exec.Cmd)       {}
func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
func killProcess(cmd *exec.Cmd) error      { return cmd.Process.Kill() }
