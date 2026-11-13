//go:build linux

package transcode

import (
    "os/exec"
    "syscall"
)

type processOwner struct{ cmd *exec.Cmd }

func startOwnedProcess(cmd *exec.Cmd) (*processOwner,error) {
    if err:=cmd.Start();err!=nil{return nil,err}
    return &processOwner{cmd:cmd},nil
}
func (p processOwner) interrupt() error{return interruptProcess(p.cmd)}
func (p processOwner) kill() error{return killProcess(p.cmd)}
func (p processOwner) close(){}

// Pdeathsig ensures an abruptly killed NVR or isolated worker does not leave
// behind credential-bearing FFmpeg/GStreamer processes. Setpgid keeps normal
// stop/timeout cleanup scoped to the owned process tree.
func configureProcess(cmd *exec.Cmd) {
    cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true,Pdeathsig:syscall.SIGKILL}
    cmd.Cancel=func()error{return killProcess(cmd)}
}
func interruptProcess(cmd *exec.Cmd) error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGTERM)}
func killProcess(cmd *exec.Cmd) error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
