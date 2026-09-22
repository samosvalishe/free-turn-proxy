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

func QuotaBackoff() time.Duration {
	return time.Duration(15+randx.Intn(15)) * time.Second
}

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
	serverAddr, err := resolveServer(cfg, rawAddr)
	if err != nil {
		return nil, err
	}
	turnConn, closeConn, err := dialTransport(ctx, cfg, serverAddr)
	if err != nil {
		return nil, err
	}

	addrFamily := turn.RequestedAddressFamilyIPv6
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
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
		STUNServerAddr:            serverAddr.String(),
		TURNServerAddr:            serverAddr.String(),
		Conn:                      turnConn,
		Net:                       netconn.New(),
		Username:                  user,
		Password:                  pass,
		RequestedAddressFamily:    addrFamily,
		PermissionRefreshInterval: 24 * time.Hour,
		LoggerFactory:             loggerFactory,
	})
	if err != nil {
		return nil, fmt.Errorf("create TURN client: %w", withClose(err, closeConn))
	}
	if err = client.Listen(); err != nil {
		client.Close()
		return nil, fmt.Errorf("TURN listen: %w", withClose(err, closeConn))
	}
	// Allocate не принимает ctx: отмену доставляет закрытие клиента и транспорта.
	stopCancel := context.AfterFunc(ctx, func() {
		client.Close()
		_ = closeConn()
	})
	relay, err := client.Allocate()
	if !stopCancel() {
		if err == nil {
			_ = relay.Close()
		}
		return nil, fmt.Errorf("TURN allocate: %w", ctx.Err())
	}
	if err != nil {
		client.Close()
		err = withClose(err, closeConn)
		if isQuotaError(err) {
			return nil, fmt.Errorf("%w: %w", ErrAllocQuota, err)
		}
		return nil, fmt.Errorf("TURN allocate: %w", err)
	}

	return &Stream{
		Relay:         relay,
		ServerUDPAddr: serverAddr,
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

func resolveServer(cfg Config, rawAddr string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(rawAddr)
	if err != nil {
		return nil, fmt.Errorf("parse TURN addr: %w", err)
	}
	if cfg.HostOverride != "" {
		host = cfg.HostOverride
	}
	if cfg.PortOverride != "" {
		port = cfg.PortOverride
	}
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("resolve TURN addr: %w", err)
	}
	return addr, nil
}

// dialTransport отдаёт STUN-транспорт до сервера и закрытие его сокета.
func dialTransport(ctx context.Context, cfg Config, addr *net.UDPAddr) (net.PacketConn, func() error, error) {
	d := net.Dialer{Control: netctl.Apply}
	if cfg.TransportUDP {
		raw, err := d.DialContext(ctx, "udp", addr.String())
		if err != nil {
			return nil, nil, fmt.Errorf("dial TURN (udp): %w", err)
		}
		c, ok := raw.(*net.UDPConn)
		if !ok {
			_ = raw.Close()
			return nil, nil, fmt.Errorf("turndial: expected *net.UDPConn, got %T", raw)
		}
		return &netconn.ConnectedUDPConn{UDPConn: c}, c.Close, nil
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, err := d.DialContext(dctx, "tcp", addr.String())
	if err != nil {
		return nil, nil, fmt.Errorf("dial TURN (tcp): %w", err)
	}
	// Разрезание внутри STUN magic cookie (байты 4-7) ломает DPI сигнатуры без TCP-реассемблинга.
	wrapped := &netconn.SplitFirstWriteConn{Conn: c, SplitAt: 5 + randx.Intn(3), Delay: 20 * time.Millisecond}
	return turn.NewSTUNConn(wrapped), c.Close, nil
}

// withClose закрывает транспорт после отказа, не теряя причину отказа.
func withClose(err error, closeConn func() error) error {
	if cerr := closeConn(); cerr != nil {
		return fmt.Errorf("%w (close: %v)", err, cerr)
	}
	return err
}
