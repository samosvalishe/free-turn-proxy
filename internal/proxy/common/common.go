// Package common holds small helpers shared between udprelay and tcpfwd. The
// two proxy modes layer DTLS and srtpmimicry differently (see V2-6 decision
// in notes/AUDIT_2026-05-19_V2.md), so a full Engine/Handler abstraction was
// rejected. This package only collects the bits that are genuinely identical.
package common

import (
	"context"
	"fmt"
	"net"

	"github.com/cacggghp/vk-turn-proxy/internal/transport/turndial"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
)

// GetCredsFunc resolves VK TURN credentials for a (link, streamID) pair.
// Matches vkauth.Client.GetCredentials.
type GetCredsFunc func(ctx context.Context, link string, streamID int) (user, pass, rawURL string, err error)

// DialTURN fetches credentials and opens a TURN stream. The caller is
// responsible for closing the returned stream and for any auth-error retry
// policy (udprelay) or session-restart policy (tcpfwd).
func DialTURN(ctx context.Context, host, port string, udp bool, peer *net.UDPAddr, link string, streamID int, getCreds GetCredsFunc) (*turndial.Stream, error) {
	user, pass, rawURL, err := getCreds(ctx, link, streamID)
	if err != nil {
		return nil, fmt.Errorf("get TURN creds: %w", err)
	}
	return turndial.Open(ctx, turndial.Config{
		HostOverride: host,
		PortOverride: port,
		UDP:          udp,
	}, peer, user, pass, rawURL)
}

// NewClientWrap returns a client-side srtpmimicry.Conn if key has the
// expected length, otherwise (nil, nil) — wrap disabled. NewConn errors are
// propagated so callers can surface them.
func NewClientWrap(key []byte) (*srtpmimicry.Conn, error) {
	if len(key) != srtpmimicry.KeyLen {
		return nil, nil
	}
	return srtpmimicry.NewConn(key, false)
}
