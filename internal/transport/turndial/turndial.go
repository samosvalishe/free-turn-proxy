// Package turndial centralizes the TURN dial+allocate pipeline shared by
// the client UDP (oneTurnConnection) and VLESS (createSmuxSession) modes.
//
// One call to Open performs: parse target, apply host/port overrides,
// resolve UDP addr, dial UDP-or-TCP (with SplitFirstWriteConn over TCP),
// turn.NewClient, Listen, Allocate. Returns the relay PacketConn plus a
// Close that tears down the allocation, TURN client, and underlying conn.
package turndial

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/samosvalishe/btp/internal/netconn"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

// Config configures one Open call.
type Config struct {
	// HostOverride, if non-empty, replaces the host part returned by the
	// credentials lookup.
	HostOverride string
	// PortOverride, if non-empty, replaces the port part returned by the
	// credentials lookup.
	PortOverride string
	// UDP=true dials TURN over UDP; otherwise over TCP via STUNConn.
	UDP bool
	// DialTimeout caps the TCP dial. Zero defaults to 5s.
	DialTimeout time.Duration
}

// Stream is a live TURN allocation with its dependencies. Close tears
// everything down in reverse order.
type Stream struct {
	// Relay is the allocated relay PacketConn returned by turn.Client.Allocate.
	Relay net.PacketConn
	// ServerUDPAddr is the resolved TURN server UDP address (host:port).
	ServerUDPAddr *net.UDPAddr
	close         func() error
}

// Close releases the allocation, TURN client and underlying transport.
// Safe to call once. Returns the first non-nil error (if any).
func (s *Stream) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

// Open dials TURN, creates a turn.Client and allocates a relay. rawAddr is
// the host:port returned by the credentials lookup; user/pass are the TURN
// long-term credentials.
func Open(ctx context.Context, cfg Config, peer *net.UDPAddr, user, pass, rawAddr string) (*Stream, error) {
	urlhost, urlport, err := net.SplitHostPort(rawAddr)
	if err != nil {
		return nil, fmt.Errorf("parse TURN addr: %w", err)
	}
	if cfg.HostOverride != "" {
		urlhost = cfg.HostOverride
	}
	if cfg.PortOverride != "" {
		urlport = cfg.PortOverride
	}
	turnServerAddr := net.JoinHostPort(urlhost, urlport)
	turnServerUDPAddr, err := net.ResolveUDPAddr("udp", turnServerAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve TURN addr: %w", err)
	}
	turnServerAddr = turnServerUDPAddr.String()

	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}

	var (
		turnConn  net.PacketConn
		closeConn func() error
	)
	if cfg.UDP {
		c, derr := net.DialUDP("udp", nil, turnServerUDPAddr) //nolint:noctx
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (udp): %w", derr)
		}
		turnConn = &netconn.ConnectedUDPConn{UDPConn: c}
		closeConn = c.Close
	} else {
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		var d net.Dialer
		c, derr := d.DialContext(dctx, "tcp", turnServerAddr)
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (tcp): %w", derr)
		}
		wrapped := &netconn.SplitFirstWriteConn{Conn: c, SplitAt: 6, Delay: 20 * time.Millisecond}
		turnConn = turn.NewSTUNConn(wrapped)
		closeConn = c.Close
	}

	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         turnServerAddr,
		TURNServerAddr:         turnServerAddr,
		Conn:                   turnConn,
		Net:                    netconn.New(),
		Username:               user,
		Password:               pass,
		RequestedAddressFamily: addrFamily,
		LoggerFactory:          logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("create TURN client: %w", err)
	}
	if err = client.Listen(); err != nil {
		client.Close()
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("TURN listen: %w", err)
	}
	relay, err := client.Allocate()
	if err != nil {
		client.Close()
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("TURN allocate: %w", err)
	}

	return &Stream{
		Relay:         relay,
		ServerUDPAddr: turnServerUDPAddr,
		close: func() error {
			var firstErr error
			if cerr := relay.Close(); cerr != nil {
				firstErr = cerr
			}
			client.Close()
			if cerr := closeConn(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
			return firstErr
		},
	}, nil
}
