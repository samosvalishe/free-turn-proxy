package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
)

type DialFunc func(ctx context.Context, streamID int) (*turndial.Stream, error)

// GetCredsFunc разрешает TURN-реквизиты для streamID.
type GetCredsFunc func(ctx context.Context, streamID int) (user, pass string, rawURLs []string, err error)

// DialTURN запрашивает учетные данные и подключается к первому доступному TURN-серверу из списка кандидатов.
func DialTURN(ctx context.Context, host, port string, udp bool, peer *net.UDPAddr, streamID int, getCreds GetCredsFunc, log logx.Logger) (*turndial.Stream, error) {
	user, pass, rawURLs, err := getCreds(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("get TURN creds: %w", err)
	}
	if len(rawURLs) == 0 {
		return nil, fmt.Errorf("no TURN candidates")
	}
	if host != "" {
		rawURLs = rawURLs[:1]
	}
	var errs []error
	for _, rawURL := range rawURLs {
		stream, derr := turndial.Open(ctx, turndial.Config{
			HostOverride: host,
			PortOverride: port,
			TransportUDP: udp,
			Log:          log,
			StreamID:     streamID,
		}, peer, user, pass, rawURL)
		if derr == nil {
			return stream, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", rawURL, derr))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("all TURN candidates failed: %w", errors.Join(errs...))
}
