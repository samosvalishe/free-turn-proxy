package turndial

import (
	"net"
	"testing"
	"time"
)

func TestDirectAcceptsOnlyPeer(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	stranger, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stranger.Close() }()

	peer, ok := server.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatal("server addr is not UDP")
	}
	s, err := Direct(t.Context(), peer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.PermDead != nil {
		t.Fatal("PermDead must be nil without relay")
	}

	if _, err = s.Relay.WriteTo([]byte("ping"), nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, client, err := server.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("server read = %q, %v", buf[:n], err)
	}

	if _, err = stranger.WriteToUDP([]byte("junk"), client); err != nil {
		t.Fatal(err)
	}
	if _, err = server.WriteToUDP([]byte("pong"), client); err != nil {
		t.Fatal(err)
	}
	_ = s.Relay.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := s.Relay.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "pong" {
		t.Fatalf("client read = %q, %v", buf[:n], err)
	}
	if from.String() != peer.String() {
		t.Fatalf("from = %s, want %s", from, peer)
	}
}
