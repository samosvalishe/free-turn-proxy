// Package turndial инкапсулирует подключение, аутентификацию и аллокацию сессий TURN.
package turndial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun/v3"
	"github.com/pion/turn/v5"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/netctl"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

// ErrAllocQuota - TURN-код 486: реквизиты валидны, но по ним уже висит предельное число
// аллокаций. Лечится ожиданием, а не сменой реквизитов (см. IsAuthError в vkauth).
var ErrAllocQuota = errors.New("turndial: allocation quota reached")

// Config задаёт параметры подключения к TURN-серверу.
type Config struct {
	HostOverride string
	PortOverride string
	TransportUDP bool
	DialTimeout  time.Duration
	Log          logx.Logger
	StreamID     int
}

// Stream представляет активную TURN-аллокацию.
type Stream struct {
	Relay         net.PacketConn
	ServerUDPAddr *net.UDPAddr
	// PermDead закрывается при стойком провале ChannelBind refresh (relay блэкхолит трафик).
	PermDead <-chan struct{}
	close    func() error
}

func isQuotaError(err error) bool {
	turnErr, ok := errors.AsType[*stun.TurnError](err)
	return ok && turnErr.ErrorCodeAttr.Code == stun.CodeAllocQuotaReached
}

// Close освобождает аллокацию, TURN-клиент и транспортное соединение.
func (s *Stream) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

// Open выполняет подключение к TURN-серверу и создаёт релей-соединение.
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
		raw, derr := (&net.Dialer{Control: netctl.Apply}).Dial("udp", turnServerUDPAddr.String())
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (udp): %w", derr)
		}
		c, ok := raw.(*net.UDPConn)
		if !ok {
			_ = raw.Close()
			return nil, fmt.Errorf("turndial: expected *net.UDPConn, got %T", raw)
		}
		turnConn = &netconn.ConnectedUDPConn{UDPConn: c}
		closeConn = c.Close
	} else {
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		d := net.Dialer{Control: netctl.Apply}
		c, derr := d.DialContext(dctx, "tcp", turnServerAddr)
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (tcp): %w", derr)
		}
		// Разрезание внутри STUN magic cookie (байты 4-7) ломает DPI сигнатуры без TCP-реассемблинга.
		wrapped := &netconn.SplitFirstWriteConn{Conn: c, SplitAt: 5 + randx.Intn(3), Delay: 20 * time.Millisecond}
		turnConn = turn.NewSTUNConn(wrapped)
		closeConn = c.Close
	}

	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}

	// VK отбрасывает CreatePermission refresh с кодом 400; канал поддерживается через ChannelBind.
	permDead := make(chan struct{})
	var permOnce sync.Once
	loggerFactory := &permWatchFactory{
		inner:     &logxFactory{log: cfg.Log, stream: cfg.StreamID},
		threshold: permFailThreshold,
		onDead:    func() { permOnce.Do(func() { close(permDead) }) },
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:            turnServerAddr,
		TURNServerAddr:            turnServerAddr,
		Conn:                      turnConn,
		Net:                       netconn.New(),
		Username:                  user,
		Password:                  pass,
		RequestedAddressFamily:    addrFamily,
		PermissionRefreshInterval: 24 * time.Hour,
		LoggerFactory:             loggerFactory,
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
		if isQuotaError(err) {
			return nil, fmt.Errorf("%w: %w", ErrAllocQuota, err)
		}
		return nil, fmt.Errorf("TURN allocate: %w", err)
	}

	return &Stream{
		Relay:         relay,
		ServerUDPAddr: turnServerUDPAddr,
		PermDead:      permDead,
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
