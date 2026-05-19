//go:build !(linux && 386)

// Package ish provides a TCP listener shim for iSH (https://ish.app), a
// Linux user-mode emulator that runs on iOS. iSH targets linux/386 and lacks
// modern accept4 / Go's epoll-based poller, so a sandbox-aware accept loop
// is needed there. On every other GOOS/GOARCH WrapListener is a no-op
// pass-through.
package ish

import "net"

// WrapListener returns ln unchanged on non-iSH platforms.
func WrapListener(ln net.Listener) (net.Listener, error) {
	return ln, nil
}
