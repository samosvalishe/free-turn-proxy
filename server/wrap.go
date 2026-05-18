// SPDX-License-Identifier: MIT

package main

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	pionudp "github.com/pion/transport/v4/udp"
	"golang.org/x/crypto/chacha20poly1305"
)

// Wire format is identical to client — see client/wrap.go. Server sets the
// MSB of sessionID; client clears it. Counter init is random per process.

const (
	wrapKeyLen    = 32
	wrapNonceLen  = 12
	wrapTagLen    = 16
	wrapPrefixMin = 1
	wrapPrefixMax = 8
)

// bufPool eliminates per-packet heap allocation on the hot read/write paths.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1600+wrapPrefixMax+wrapNonceLen+wrapTagLen)
		return &b
	},
}

// wrapState holds the AEAD instance shared by all connections under one key.
type wrapState struct {
	aead cipher.AEAD
}

func newWrapState(key []byte) (*wrapState, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("wrap: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("wrap: aead init: %w", err)
	}
	return &wrapState{aead: aead}, nil
}

// --- Listener ---

func listenWrapped(addr *net.UDPAddr, key []byte) (dtlsnet.PacketListener, error) {
	ws, err := newWrapState(key)
	if err != nil {
		return nil, err
	}
	inner, err := pionudp.Listen("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("wrap: udp listen: %w", err)
	}
	return &wrapPacketListener{
		inner: dtlsnet.PacketListenerFromListener(inner),
		ws:    ws,
	}, nil
}

type wrapPacketListener struct {
	inner dtlsnet.PacketListener
	ws    *wrapState
}

func (l *wrapPacketListener) Accept() (net.PacketConn, net.Addr, error) {
	pc, addr, err := l.inner.Accept()
	if err != nil {
		return pc, addr, err
	}
	c := &wrapPacketConn{inner: pc, ws: l.ws}
	// Per-connection sessionID (server MSB=1) + random counter init.
	if _, err := rand.Read(c.sessionID[:]); err != nil {
		return nil, addr, fmt.Errorf("wrap: sessionID rand: %w", err)
	}
	c.sessionID[0] |= 0x80
	var cb [8]byte
	if _, err := rand.Read(cb[:]); err != nil {
		return nil, addr, fmt.Errorf("wrap: counter rand: %w", err)
	}
	c.counter.Store(binary.BigEndian.Uint64(cb[:]))
	return c, addr, nil
}

func (l *wrapPacketListener) Close() error   { return l.inner.Close() }
func (l *wrapPacketListener) Addr() net.Addr { return l.inner.Addr() }

// --- Per-peer PacketConn ---

type wrapPacketConn struct {
	inner     net.PacketConn
	ws        *wrapState
	sessionID [4]byte
	counter   atomic.Uint64
}

func (c *wrapPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	bp := bufPool.Get().(*[]byte)
	buf := *bp
	need := len(p) + wrapPrefixMax + wrapNonceLen + wrapTagLen
	if cap(buf) < need {
		buf = make([]byte, need)
		*bp = buf
	}
	defer bufPool.Put(bp)

	n, addr, err := c.inner.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	wire := buf[:n]
	if len(wire) < wrapPrefixMin+wrapNonceLen+wrapTagLen {
		return 0, addr, errors.New("wrap: packet too short")
	}
	prefixLen := wrapPrefixMin + int(wire[0]&0x07)
	if len(wire) < prefixLen+wrapNonceLen+wrapTagLen {
		return 0, addr, errors.New("wrap: packet too short for prefix")
	}
	nonce := wire[prefixLen : prefixLen+wrapNonceLen]
	ct := wire[prefixLen+wrapNonceLen:]

	plain, err := c.ws.aead.Open(ct[:0], nonce, ct, nonce)
	if err != nil {
		return 0, addr, fmt.Errorf("wrap: AEAD open: %w", err)
	}
	if len(plain) > len(p) {
		return 0, addr, errors.New("wrap: dst buffer too small")
	}
	copy(p[:len(plain)], plain)
	return len(plain), addr, nil
}

func (c *wrapPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	var hdr [1]byte
	if _, err := rand.Read(hdr[:]); err != nil {
		return 0, fmt.Errorf("wrap: prefix rand: %w", err)
	}
	prefixLen := wrapPrefixMin + int(hdr[0]&0x07)
	wireLen := prefixLen + wrapNonceLen + len(p) + wrapTagLen

	bp := bufPool.Get().(*[]byte)
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer bufPool.Put(bp)

	out[0] = hdr[0]
	if prefixLen > 1 {
		if _, err := rand.Read(out[1:prefixLen]); err != nil {
			return 0, fmt.Errorf("wrap: prefix rand: %w", err)
		}
	}

	noncePos := prefixLen
	copy(out[noncePos:noncePos+4], c.sessionID[:])
	ctr := c.counter.Add(1) - 1
	binary.BigEndian.PutUint64(out[noncePos+4:noncePos+wrapNonceLen], ctr)

	nonce := out[noncePos : noncePos+wrapNonceLen]
	ctPos := noncePos + wrapNonceLen
	copy(out[ctPos:], p)
	c.ws.aead.Seal(out[ctPos:ctPos], nonce, out[ctPos:ctPos+len(p)], nonce)

	if _, err := c.inner.WriteTo(out, addr); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wrapPacketConn) Close() error                       { return c.inner.Close() }
func (c *wrapPacketConn) LocalAddr() net.Addr                { return c.inner.LocalAddr() }
func (c *wrapPacketConn) SetDeadline(t time.Time) error      { return c.inner.SetDeadline(t) }
func (c *wrapPacketConn) SetReadDeadline(t time.Time) error  { return c.inner.SetReadDeadline(t) }
func (c *wrapPacketConn) SetWriteDeadline(t time.Time) error { return c.inner.SetWriteDeadline(t) }
