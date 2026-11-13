//go:build !linux

package api

import "syscall"

func localVideoProcessAttr() *syscall.SysProcAttr { return nil }
