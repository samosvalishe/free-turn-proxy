package turndial

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/samosvalishe/free-turn-proxy/internal/netctl"
)

func Direct(ctx context.Context, peer *net.UDPAddr) (*Stream, error) {
	network := "udp4"
	if peer.IP.To4() == nil {
		network = "udp6"
	}
	pc, err := (&net.ListenConfig{Control: netctl.Apply}).ListenPacket(ctx, network, "")
	if err != nil {
		return nil, fmt.Errorf("direct listen: %w", err)
	}
	c, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("turndial: expected *net.UDPConn, got %T", pc)
	}
	ap := peer.AddrPort()
	return &Stream{
		Relay:         &peerConn{UDPConn: c, peer: peer, want: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())},
		ServerUDPAddr: peer,
		close:         c.Close,
	}, nil
}

type peerConn struct {
	*net.UDPConn
	peer *net.UDPAddr
	want netip.AddrPort
}

func (c *peerConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, from, err := c.ReadFromUDPAddrPort(p)
		if err != nil {
			return n, nil, err
		}
		if from.Addr().Unmap() == c.want.Addr() && from.Port() == c.want.Port() {
			return n, c.peer, nil
		}
	}
}

func (c *peerConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.WriteToUDPAddrPort(p, c.want)
}
