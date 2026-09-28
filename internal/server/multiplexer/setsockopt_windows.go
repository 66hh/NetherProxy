//go:build windows

package multiplexer

import "syscall"

// setsockoptV6Only 设置 IPV6_V6ONLY (Windows 的 fd 需转为 Handle)
func setsockoptV6Only(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
}
