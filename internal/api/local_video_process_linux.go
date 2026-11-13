//go:build linux

package api

import "syscall"

// Linux terminates FFmpeg if its supervising NVR process exits unexpectedly.
func localVideoProcessAttr() *syscall.SysProcAttr {
 return &syscall.SysProcAttr{Pdeathsig:syscall.SIGTERM}
}
