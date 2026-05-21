// Package netconn — мелкие net.Conn / transport.Net адаптеры для клиента
// и сервера: passthrough transport.Net (для pion turn), ConnectedUDPConn
// (WriteTo поверх dialed UDPConn) и SplitFirstWriteConn (обход DPI через
// разбиение первого сегмента).
package netconn

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/transport/v4"
)

// DirectNet реализует transport.Net через стандартный net.
type DirectNet struct{}

// New возвращает transport.Net поверх стандартного net.
func New() transport.Net {
	return DirectNet{}
}

type directDialer struct {
	*net.Dialer
}

type directListenConfig struct {
	*net.ListenConfig
}

type directTCPListener struct {
	*net.TCPListener
}

func (DirectNet) ListenPacket(network string, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, address)
}

func (DirectNet) ListenUDP(network string, locAddr *net.UDPAddr) (transport.UDPConn, error) {
	return net.ListenUDP(network, locAddr)
}

func (DirectNet) ListenTCP(network string, laddr *net.TCPAddr) (transport.TCPListener, error) {
	listener, err := net.ListenTCP(network, laddr)
	if err != nil {
		return nil, err
	}
	return directTCPListener{listener}, nil
}

func (DirectNet) Dial(network, address string) (net.Conn, error) {
	return net.Dial(network, address)
}

func (DirectNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	return net.DialUDP(network, laddr, raddr)
}

func (DirectNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	return net.DialTCP(network, laddr, raddr)
}

func (DirectNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (DirectNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (DirectNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (DirectNet) Interfaces() ([]*transport.Interface, error) {
	return nil, transport.ErrNotSupported
}

func (DirectNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	return nil, fmt.Errorf("%w: index=%d", transport.ErrInterfaceNotFound, index)
}

func (DirectNet) InterfaceByName(name string) (*transport.Interface, error) {
	return nil, fmt.Errorf("%w: %s", transport.ErrInterfaceNotFound, name)
}

func (DirectNet) CreateDialer(dialer *net.Dialer) transport.Dialer {
	return directDialer{Dialer: dialer}
}

func (DirectNet) CreateListenConfig(listenerConfig *net.ListenConfig) transport.ListenConfig {
	return directListenConfig{ListenConfig: listenerConfig}
}

func (d directDialer) Dial(network, address string) (net.Conn, error) {
	return d.Dialer.Dial(network, address)
}

func (d directListenConfig) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return d.ListenConfig.Listen(ctx, network, address)
}

func (d directListenConfig) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	return d.ListenConfig.ListenPacket(ctx, network, address)
}

func (l directTCPListener) AcceptTCP() (transport.TCPConn, error) {
	return l.TCPListener.AcceptTCP()
}

// ConnectedUDPConn адаптирует dialed (connected) *net.UDPConn к семантике
// net.PacketConn: WriteTo игнорирует адрес, т.к. ядро уже знает его из connect().
type ConnectedUDPConn struct {
	*net.UDPConn
}

// WriteTo игнорирует addr (UDP уже connected) и пишет p.
func (c *ConnectedUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

// SplitFirstWriteConn оборачивает TCP net.Conn и разбивает самый первый Write
// на два сегмента (SplitAt байт + остаток) с опциональной паузой Delay между ними.
// Ломает DPI-правила, матчащие фиксированный offset в первом сегменте без
// TCP-реассемблинга (например STUN magic cookie на offset 4-7).
type SplitFirstWriteConn struct {
	net.Conn
	SplitAt int
	Delay   time.Duration
	done    atomic.Bool
}

// Write делает one-shot разбиение при первом вызове, далее проксирует напрямую.
func (s *SplitFirstWriteConn) Write(b []byte) (int, error) {
	if s.done.CompareAndSwap(false, true) && len(b) > s.SplitAt {
		n1, err := s.Conn.Write(b[:s.SplitAt])
		if err != nil {
			return n1, err
		}
		if s.Delay > 0 {
			time.Sleep(s.Delay)
		}
		n2, err := s.Conn.Write(b[s.SplitAt:])
		return n1 + n2, err
	}
	return s.Conn.Write(b)
}
