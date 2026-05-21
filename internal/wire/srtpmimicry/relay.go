// SPDX-License-Identifier: MIT

package srtpmimicry

import (
	"net"
	"time"
)

// RelayPacketConn оборачивает TURN relay PacketConn, направляя все записи
// фиксированному пиру. Если Conn non-nil, пакеты оборачиваются/разворачиваются
// SRTP-mimicry AEAD.
type RelayPacketConn struct {
	Relay net.PacketConn
	Peer  net.Addr
	Conn  *Conn
}

func (r *RelayPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if r.Conn == nil {
		return r.Relay.ReadFrom(b)
	}
	bp := bufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	buf := *bp
	need := MaxWire(len(b))
	if cap(buf) < need {
		buf = make([]byte, need)
		*bp = buf
	}
	defer bufPool.Put(bp)

	n, addr, err := r.Relay.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	m, err := r.Conn.Unwrap(buf[:n], b)
	if err != nil {
		return 0, addr, err
	}
	return m, addr, nil
}

func (r *RelayPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	if r.Conn == nil {
		return r.Relay.WriteTo(b, r.Peer)
	}
	wireLen := MaxWire(len(b))

	bp := bufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer bufPool.Put(bp)

	n, err := r.Conn.WrapInto(out, b)
	if err != nil {
		return 0, err
	}
	if _, err = r.Relay.WriteTo(out[:n], r.Peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *RelayPacketConn) Close() error                       { return r.Relay.Close() }
func (r *RelayPacketConn) LocalAddr() net.Addr                { return r.Relay.LocalAddr() }
func (r *RelayPacketConn) SetDeadline(t time.Time) error      { return r.Relay.SetDeadline(t) }
func (r *RelayPacketConn) SetReadDeadline(t time.Time) error  { return r.Relay.SetReadDeadline(t) }
func (r *RelayPacketConn) SetWriteDeadline(t time.Time) error { return r.Relay.SetWriteDeadline(t) }
