package dtlsdial

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

func GenerateSelfSignedCert() (tls.Certificate, error) {
	return selfsign.GenerateSelfSigned()
}

// Dialer конфигурирует DTLS-handshake клиента.
type Dialer struct {
	HandshakeTimeout time.Duration
	HandshakeSem     chan struct{}
}

// Dial выполняет DTLS-handshake поверх pc к peer с уникальным self-signed сертификатом.
func (d *Dialer) Dial(ctx context.Context, pc net.PacketConn, peer *net.UDPAddr) (*dtls.Conn, error) {
	certificate, err := GenerateSelfSignedCert()
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
