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
