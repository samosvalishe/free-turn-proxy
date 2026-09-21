package wire

import (
	"net"
	"sync"
	"time"
)

var relayBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1600+128)
		return &b
	},
}

// RelayPacketConn адаптирует net.PacketConn релея с опциональным шифрованием/обфускацией.
type RelayPacketConn struct {
	Relay net.PacketConn
	Peer  net.Addr
	Codec Codec
}

func (r *RelayPacketConn) dst(addr net.Addr) net.Addr {
	if r.Peer != nil {
		return r.Peer
	}
	return addr
}

func (r *RelayPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if r.Codec == nil {
		return r.Relay.ReadFrom(b)
	}
	bp := relayBufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	buf := *bp
	need := r.Codec.MaxWire(len(b))
	if cap(buf) < need {
		buf = make([]byte, need)
		*bp = buf
	}
	defer relayBufPool.Put(bp)

	n, addr, err := r.Relay.ReadFrom(buf[:cap(buf)])
	if err != nil {
		return 0, addr, err
	}
	m, err := r.Codec.Unwrap(buf[:n], b)
	if err != nil {
		return 0, addr, err
	}
	return m, addr, nil
}

func (r *RelayPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	peer := r.dst(addr)
	if r.Codec == nil {
		return r.Relay.WriteTo(b, peer)
	}
	wireLen := r.Codec.MaxWire(len(b))

	bp := relayBufPool.Get().(*[]byte) //nolint:errcheck // pool New always returns *[]byte
	out := *bp
	if cap(out) < wireLen {
		out = make([]byte, wireLen)
		*bp = out
	}
	out = out[:wireLen]
	defer relayBufPool.Put(bp)

	n, err := r.Codec.WrapInto(out, b)
	if err != nil {
		return 0, err
	}
	if _, err = r.Relay.WriteTo(out[:n], peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *RelayPacketConn) Close() error                       { return r.Relay.Close() }
func (r *RelayPacketConn) LocalAddr() net.Addr                { return r.Relay.LocalAddr() }
func (r *RelayPacketConn) SetDeadline(t time.Time) error      { return r.Relay.SetDeadline(t) }
func (r *RelayPacketConn) SetReadDeadline(t time.Time) error  { return r.Relay.SetReadDeadline(t) }
func (r *RelayPacketConn) SetWriteDeadline(t time.Time) error { return r.Relay.SetWriteDeadline(t) }
