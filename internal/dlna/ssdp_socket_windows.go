//go:build windows

package dlna

import "syscall"

func setSSDPReuseOptions(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
