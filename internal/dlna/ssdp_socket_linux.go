//go:build linux

package dlna

import "syscall"

// Linux SO_REUSEPORT is socket option 15 (SOL_SOCKET).
const linuxSOReusePort = 15

func setSSDPReuseOptions(fd uintptr) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return err
	}
	// Preserve the prior best-effort reuse-port behavior.
	_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, linuxSOReusePort, 1)
	return nil
}
