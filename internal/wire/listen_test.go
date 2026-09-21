package wire

import (
	"crypto/rand"
	"net"
	"testing"
	"time"
)

func TestListenCodecRoundTrip(t *testing.T) {
	t.Parallel()

	for _, profile := range []string{ProfileRTPOpus, ProfileRTPOpus2, ProfileRTPOpus3} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()

			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				t.Fatal(err)
			}
			ln, err := Listen(profile, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, key)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()

			raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()

			codec, err := NewClientCodec(profile, key)
			if err != nil {
				t.Fatal(err)
			}
			client := &RelayPacketConn{Relay: raw, Peer: ln.Addr(), Codec: codec}

			want := []byte("obfuscated payload")
			if _, err = client.WriteTo(want, nil); err != nil {
				t.Fatal(err)
			}

			srv, _, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.Close() }()

			buf := make([]byte, 1500)
			if err = srv.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			n, from, err := srv.ReadFrom(buf)
			if err != nil {
				t.Fatal(err)
			}
			if string(buf[:n]) != string(want) {
				t.Fatalf("server got %q, want %q", buf[:n], want)
			}

			// Peer у серверной стороны пустой: ответ обязан уйти отправителю.
			if _, err = srv.WriteTo(buf[:n], from); err != nil {
				t.Fatal(err)
			}
			if err = client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			n, _, err = client.ReadFrom(buf)
			if err != nil {
				t.Fatal(err)
			}
			if string(buf[:n]) != string(want) {
				t.Fatalf("client got %q, want %q", buf[:n], want)
			}
		})
	}
}
