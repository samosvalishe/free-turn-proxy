package udpserver

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/dtls/v3"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
)

func echoBackend(t *testing.T) string {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().String()
}

func dtlsPair(t *testing.T, ctx context.Context, backend string) net.Conn {
	t.Helper()
	cert, err := dtlsdial.GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := dtls.ListenWithOptions("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, dtls.WithCertificates(cert))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		Handle(ctx, logx.Nop(), conn, backend)
	}()

	pc, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverAddr, ok := ln.Addr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("listener addr %T", ln.Addr())
	}
	dialer := &dtlsdial.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, err := dialer.Dial(ctx, pc, serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Датаграмма в пределах клиентского буфера 2048 проходит сервер в обе стороны целиком.
func TestHandleRelaysLargeDatagram(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := dtlsPair(t, ctx, echoBackend(t))

	for _, size := range []int{1200, 1800, 2048} {
		payload := bytes.Repeat([]byte{0x5a}, size)
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("size %d: write: %v", size, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], payload) {
			t.Fatalf("size %d: echo n=%d err=%v", size, n, err)
		}
	}
}
