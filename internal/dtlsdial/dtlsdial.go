// Package dtlsdial wraps pion-dtls client setup (self-signed cert, EMS,
// AES-128-GCM, send-only CID) plus an optional concurrency gate around the
// handshake. Used by the client UDP and VLESS pipelines.
package dtlsdial

import (
	"context"
	"net"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// Dialer configures the DTLS client handshake.
type Dialer struct {
	// HandshakeTimeout caps the handshake context. Zero means no timeout.
	HandshakeTimeout time.Duration
	// HandshakeSem, if non-nil, gates concurrent handshakes (Dial blocks
	// until a slot is available or ctx fires).
	HandshakeSem chan struct{}
}

// Dial generates a fresh self-signed cert, acquires the optional handshake
// slot, and performs a DTLS client handshake over pc to peer. On success
// returns the connected *dtls.Conn. Caller closes it.
func (d *Dialer) Dial(ctx context.Context, pc net.PacketConn, peer *net.UDPAddr) (*dtls.Conn, error) {
	certificate, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, err
	}
	if d.HandshakeSem != nil {
		select {
		case d.HandshakeSem <- struct{}{}:
			defer func() { <-d.HandshakeSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	hsCtx := ctx
	if d.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		hsCtx, cancel = context.WithTimeout(ctx, d.HandshakeTimeout)
		defer cancel()
	}

	dtlsConn, err := dtls.ClientWithOptions(
		pc,
		peer,
		dtls.WithCertificates(certificate),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	)
	if err != nil {
		return nil, err
	}
	if err := dtlsConn.HandshakeContext(hsCtx); err != nil {
		_ = dtlsConn.Close()
		return nil, err
	}
	return dtlsConn, nil
}
