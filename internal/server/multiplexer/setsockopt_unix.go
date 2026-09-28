//go:build !windows

package multiplexer

import "syscall"

// setsockoptV6Only 设置 IPV6_V6ONLY
func setsockoptV6Only(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
}
