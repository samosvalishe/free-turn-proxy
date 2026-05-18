// SPDX-License-Identifier: MIT

package main

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
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

// Wire format is identical to the client — see client/wrap.go for the
// full specification. Server uses epoch MSB=1, client uses MSB=0.

const (
	wrapKeyLen    = 32
	wrapHeaderLen = 13 // DTLS 1.2 record header
	wrapTagLen    = 16 // Poly1305 tag
	wrapLenPrefix = 2  // uint16 BE real payload length
)

var padBuckets = [...]int{128, 256, 512, 768, 1024, 1300}

// bufPool eliminates per-packet heap allocation on the hot read/write paths.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1600)
		return &b
	},
}

func padPlaintextSize(payloadLen int) int {
	needed := wrapLenPrefix + payloadLen
	for _, b := range padBuckets {
		if b >= needed {
			return b
		}
	}
	return needed
}

func wrapMaxWire(payloadLen int) int {
	return wrapHeaderLen + padPlaintextSize(payloadLen) + wrapTagLen
}

// wrapState holds the AEAD instance and nonce mask shared by all connections
// under one key, plus per-connection epoch/seq state.
type wrapState struct {
	aead      cipher.AEAD
	nonceMask [12]byte
}

func newWrapState(key []byte) (*wrapState, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("wrap: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("wrap: aead init: %w", err)
	}
	h := sha256.Sum256(key)
	var mask [12]byte
	copy(mask[:], h[:12])
	return &wrapState{aead: aead, nonceMask: mask}, nil
}

func (s *wrapState) deriveNonce(epoch uint16, seqNum uint64) [12]byte {
	var explicit [12]byte
	binary.BigEndian.PutUint16(explicit[2:4], epoch)
	binary.BigEndian.PutUint16(explicit[4:6], uint16(seqNum>>32))
	binary.BigEndian.PutUint32(explicit[6:10], uint32(seqNum))
	var nonce [12]byte
	for i := range 12 {
		nonce[i] = s.nonceMask[i] ^ explicit[i]
	}
	return nonce
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
	// Server epoch: random with MSB=1
	var eb [2]byte
	_, _ = rand.Read(eb[:])
	epoch := binary.BigEndian.Uint16(eb[:]) | 0x8000

	return &wrapPacketConn{inner: pc, ws: l.ws, epoch: epoch}, addr, nil
}

func (l *wrapPacketListener) Close() error   { return l.inner.Close() }
func (l *wrapPacketListener) Addr() net.Addr { return l.inner.Addr() }

// --- Per-peer PacketConn ---

type wrapPacketConn struct {
	inner net.PacketConn
	ws    *wrapState
	epoch uint16
	seq   atomic.Uint64
}

func (c *wrapPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	bp := bufPool.Get().(*[]byte)
	buf := *bp
	if cap(buf) < len(p)+wrapHeaderLen+wrapTagLen {
		buf = make([]byte, len(p)+wrapHeaderLen+wrapTagLen)
		*bp = buf
	}
	defer bufPool.Put(bp)

	n, addr, err := c.inner.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	wire := buf[:n]
	if len(wire) < wrapHeaderLen+wrapTagLen+wrapLenPrefix {
		return 0, addr, errors.New("wrap: packet too short")
	}
	if wire[0] != 0x17 {
		return 0, addr, fmt.Errorf("wrap: unexpected content type 0x%02X", wire[0])
	}

	epoch := binary.BigEndian.Uint16(wire[3:5])
	seqHigh := uint64(binary.BigEndian.Uint16(wire[5:7]))
	seqLow := uint64(binary.BigEndian.Uint32(wire[7:11]))
	seqNum := (seqHigh << 32) | seqLow
	bodyLen := int(binary.BigEndian.Uint16(wire[11:13]))

	if wrapHeaderLen+bodyLen > n {
		return 0, addr, errors.New("wrap: truncated packet")
	}

	nonce := c.ws.deriveNonce(epoch, seqNum)
	aad := wire[:wrapHeaderLen]
	ciphertext := wire[wrapHeaderLen : wrapHeaderLen+bodyLen]

	plain, err := c.ws.aead.Open(ciphertext[:0], nonce[:], ciphertext, aad)
	if err != nil {
		return 0, addr, fmt.Errorf("wrap: AEAD open: %w", err)
	}
	if len(plain) < wrapLenPrefix {
		return 0, addr, errors.New("wrap: plaintext too short")
	}
	realLen := int(binary.BigEndian.Uint16(plain[0:2]))
	if realLen > len(plain)-wrapLenPrefix || realLen > len(p) {
		return 0, addr, errors.New("wrap: payload length mismatch")
	}
	copy(p[:realLen], plain[wrapLenPrefix:wrapLenPrefix+realLen])
	return realLen, addr, nil
}

func (c *wrapPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	paddedLen := padPlaintextSize(len(p))
	wireLen := wrapHeaderLen + paddedLen + wrapTagLen

	bp := bufPool.Get().(*[]byte)
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer bufPool.Put(bp)

	seq := c.seq.Add(1) - 1

	// DTLS record header
	out[0] = 0x17
	out[1] = 0xFE
	out[2] = 0xFD
	binary.BigEndian.PutUint16(out[3:5], c.epoch)
	binary.BigEndian.PutUint16(out[5:7], uint16(seq>>32))
	binary.BigEndian.PutUint32(out[7:11], uint32(seq))
	binary.BigEndian.PutUint16(out[11:13], uint16(paddedLen+wrapTagLen))

	// Plaintext: [2B len | payload | random padding]
	plain := out[wrapHeaderLen : wrapHeaderLen+paddedLen]
	binary.BigEndian.PutUint16(plain[0:2], uint16(len(p)))
	copy(plain[wrapLenPrefix:], p)
	padStart := wrapLenPrefix + len(p)
	if padStart < paddedLen {
		_, _ = rand.Read(plain[padStart:paddedLen])
	}

	nonce := c.ws.deriveNonce(c.epoch, seq)
	aad := out[:wrapHeaderLen]
	c.ws.aead.Seal(out[wrapHeaderLen:wrapHeaderLen], nonce[:], plain, aad)

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
