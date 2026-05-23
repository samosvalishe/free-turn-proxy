// Package turndial централизует TURN dial+allocate pipeline, общий для
// UDP (oneTurnConnection) и VLESS (createSmuxSession) режимов клиента.
//
// Один вызов Open выполняет: парсинг цели, применение host/port override,
// резолв UDP-адреса, dial UDP-или-TCP (с SplitFirstWriteConn поверх TCP),
// turn.NewClient, Listen, Allocate. Возвращает relay PacketConn и Close,
// который разрушает аллокацию, TURN-клиент и транспорт.
package turndial

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
)

// Config конфигурирует один вызов Open.
type Config struct {
	// HostOverride, если непустой, заменяет host из lookup credentials.
	HostOverride string
	// PortOverride, если непустой, заменяет port из lookup credentials.
	PortOverride string
	// TransportUDP=true — dial TURN по UDP; иначе по TCP через STUNConn.
	TransportUDP bool
	// DialTimeout ограничивает TCP dial. Ноль → 5s.
	DialTimeout time.Duration
}

// Stream — активная TURN-аллокация с зависимостями. Close разрушает в обратном порядке.
type Stream struct {
	// Relay — выделенный relay PacketConn из turn.Client.Allocate.
	Relay net.PacketConn
	// ServerUDPAddr — резолвнутый UDP-адрес TURN-сервера (host:port).
	ServerUDPAddr *net.UDPAddr
	close         func() error
}

// Close освобождает аллокацию, TURN-клиент и транспорт.
// Безопасно вызвать один раз. Возвращает первую non-nil ошибку.
func (s *Stream) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

// Open подключается к TURN, создаёт turn.Client и выделяет relay. rawAddr —
// host:port из lookup credentials; user/pass — долгосрочные TURN-реквизиты.
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
	if cfg.TransportUDP {
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
