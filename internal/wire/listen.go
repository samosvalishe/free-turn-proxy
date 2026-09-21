package wire

import (
	"fmt"
	"net"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	pionudp "github.com/pion/transport/v4/udp"
)

type NewServerCodec func() (Codec, error)

func ListenCodec(addr *net.UDPAddr, newCodec NewServerCodec) (dtlsnet.PacketListener, error) {
	inner, err := pionudp.Listen("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("wire: udp listen: %w", err)
	}
	return &packetListener{inner: dtlsnet.PacketListenerFromListener(inner), newCodec: newCodec}, nil
}

type packetListener struct {
	inner    dtlsnet.PacketListener
	newCodec NewServerCodec
}

func (l *packetListener) Accept() (net.PacketConn, net.Addr, error) {
	pc, addr, err := l.inner.Accept()
	if err != nil {
		return pc, addr, err
	}
	codec, err := l.newCodec()
	if err != nil {
		return nil, addr, err
	}
	return &RelayPacketConn{Relay: pc, Codec: codec}, addr, nil
}

func (l *packetListener) Close() error   { return l.inner.Close() }
func (l *packetListener) Addr() net.Addr { return l.inner.Addr() }
