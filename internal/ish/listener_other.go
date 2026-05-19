//go:build !(linux && 386)

// Package ish provides a TCP listener shim for the iSH iOS simulator on
// linux/386, which lacks modern accept4 and Go's epoll-based poller. On every
// other GOOS/GOARCH WrapListener is a no-op pass-through.
package ish

import "net"

// WrapListener returns ln unchanged on non-iSH platforms.
func WrapListener(ln net.Listener) (net.Listener, error) {
	return ln, nil
}
