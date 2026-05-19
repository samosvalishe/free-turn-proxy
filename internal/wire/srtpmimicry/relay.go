// SPDX-License-Identifier: MIT

package srtpmimicry

import (
	"net"
	"time"
)

// RelayPacketConn wraps a TURN relay PacketConn to direct all writes to a fixed peer.
// When Conn is non-nil, packets are wrapped/unwrapped with SRTP-mimicry AEAD.
type RelayPacketConn struct {
	Relay net.PacketConn
	Peer  net.Addr
	Conn  *Conn
}

func (r *RelayPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if r.Conn == nil {
		return r.Relay.ReadFrom(b)
	}
	buf := make([]byte, MaxWire(len(b)))
	n, addr, err := r.Relay.ReadFrom(buf)
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
	out := make([]byte, MaxWire(len(b)))
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
