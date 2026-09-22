package turndial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/pion/turn/v5"
)

func TestIsQuotaError(t *testing.T) {
	quota := &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: stun.CodeAllocQuotaReached}}
	if !isQuotaError(fmt.Errorf("allocate: %w", quota)) {
		t.Fatal("wrapped 486 not recognized")
	}
	if isQuotaError(errors.New("486 Allocation Quota Reached")) || isQuotaError(nil) {
		t.Fatal("untyped error treated as quota")
	}
	unauthorized := &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: stun.CodeUnauthorized}}
	if isQuotaError(unauthorized) {
		t.Fatal("401 treated as quota")
	}
}

func TestOpen_BadAddress(t *testing.T) {
	peer := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}
	_, err := Open(context.Background(), Config{}, peer, "u", "p", "not-a-host-port")
	if err == nil {
		t.Fatal("expected error for malformed addr")
	}
	if !strings.Contains(err.Error(), "parse TURN addr") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpen_HostOverrideApplied(t *testing.T) {
	peer := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := Open(ctx, Config{HostOverride: "127.0.0.1", PortOverride: "1", TransportUDP: false, DialTimeout: 200 * time.Millisecond}, peer, "u", "p", "8.8.8.8:443")
	if err == nil {
		t.Fatal("expected dial error against unreachable :1")
	}
	if !strings.Contains(err.Error(), "dial TURN") {
		t.Fatalf("expected dial error, got: %v", err)
	}
}

// Молчащий TURN: отмена ctx обязана прервать Allocate, а не ждать ретрансмиты pion.
func TestOpen_CancelInterruptsAllocate(t *testing.T) {
	for _, udp := range []bool{false, true} {
		t.Run(fmt.Sprintf("udp=%v", udp), func(t *testing.T) {
			addr := silentTURN(t, udp)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			peer := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}

			start := time.Now()
			_, err := Open(ctx, Config{TransportUDP: udp}, peer, "u", "p", addr)
			if d := time.Since(start); d > time.Second {
				t.Fatalf("Open returned after %s, want prompt cancel", d)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Open = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

func silentTURN(t *testing.T, udp bool) string {
	t.Helper()
	if udp {
		pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		return pc.LocalAddr().String()
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return ln.Addr().String()
}

// Успешный Allocate: хук отмены снят, Stream переживает последующую отмену ctx.
func TestOpen_AllocateThenCancelKeepsStream(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, turn.GenerateAuthKey(ra.Username, ra.Realm, "p"), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	peer := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	stream, err := Open(ctx, Config{TransportUDP: true}, peer, "u", "p", pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()
	cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := stream.Relay.WriteTo([]byte("x"), peer); err != nil {
		t.Fatalf("relay closed by cancelled ctx: %v", err)
	}
}
