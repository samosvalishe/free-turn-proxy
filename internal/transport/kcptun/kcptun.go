package kcptun

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// Profile — настраиваемые KCP-параметры. Обе стороны туннеля должны совпадать.
type Profile struct {
	NoDelay    int
	Interval   int
	Resend     int
	NC         int
	SndWnd     int
	RcvWnd     int
	MTU        int
	ACKNoDelay bool
}

// FEC управляет шардами KCP forward-error-correction. Нулевые значения отключают FEC.
type FEC struct {
	Data   int
	Parity int
}

// DefaultProfile — исторический balanced-профиль, поставляемый с прокси.
func DefaultProfile() Profile {
	return Profile{
		NoDelay:    1,
		Interval:   20,
		Resend:     2,
		NC:         1,
		SndWnd:     512,
		RcvWnd:     512,
		MTU:        1200,
		ACKNoDelay: true,
	}
}

// LoadProfileFromEnv читает VK_TURN_KCP_PROFILE и per-field переопределения
// VK_TURN_KCP_*. Неизвестное имя профиля → DefaultProfile.
func LoadProfileFromEnv() Profile {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("VK_TURN_KCP_PROFILE")))
	var p Profile
	switch name {
	case "legacy", "fast":
		p = Profile{NoDelay: 1, Interval: 10, Resend: 2, NC: 1, SndWnd: 4096, RcvWnd: 4096, MTU: 1280, ACKNoDelay: true}
	case "cc", "balanced":
		p = Profile{NoDelay: 1, Interval: 20, Resend: 2, NC: 0, SndWnd: 512, RcvWnd: 512, MTU: 1200, ACKNoDelay: true}
	case "slow", "conservative":
		p = Profile{NoDelay: 0, Interval: 40, Resend: 2, NC: 0, SndWnd: 256, RcvWnd: 256, MTU: 1150, ACKNoDelay: false}
	default:
		p = DefaultProfile()
	}
	p.NoDelay = envInt("VK_TURN_KCP_NODELAY", p.NoDelay)
	p.Interval = envInt("VK_TURN_KCP_INTERVAL", p.Interval)
	p.Resend = envInt("VK_TURN_KCP_RESEND", p.Resend)
	p.NC = envInt("VK_TURN_KCP_NC", p.NC)
	p.SndWnd = envInt("VK_TURN_KCP_SNDWND", p.SndWnd)
	p.RcvWnd = envInt("VK_TURN_KCP_RCVWND", p.RcvWnd)
	p.MTU = envInt("VK_TURN_KCP_MTU", p.MTU)
	p.ACKNoDelay = envBool("VK_TURN_KCP_ACK_NODELAY", p.ACKNoDelay)
	return p
}

// LoadFECFromEnv парсит VK_TURN_KCP_FEC как "data:parity" (напр. "10:3").
// Пустое/невалидное → отключено.
func LoadFECFromEnv() FEC {
	raw := strings.TrimSpace(os.Getenv("VK_TURN_KCP_FEC"))
	if raw == "" {
		return FEC{}
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return FEC{}
	}
	d, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	p, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || d <= 0 || p <= 0 {
		return FEC{}
	}
	return FEC{Data: d, Parity: p}
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// DtlsPacketConn оборачивает net.Conn (DTLS) как net.PacketConn для KCP.
// Каждый DTLS Read/Write сохраняет границы сообщений (datagram семантика).
type DtlsPacketConn struct {
	conn net.Conn
}

func NewDtlsPacketConn(conn net.Conn) *DtlsPacketConn {
	return &DtlsPacketConn{conn: conn}
}

func (d *DtlsPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := d.conn.Read(b)
	return n, d.conn.RemoteAddr(), err
}

func (d *DtlsPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return d.conn.Write(b)
}

func (d *DtlsPacketConn) Close() error {
	return d.conn.Close()
}

func (d *DtlsPacketConn) LocalAddr() net.Addr {
	return d.conn.LocalAddr()
}

func (d *DtlsPacketConn) SetDeadline(t time.Time) error {
	return d.conn.SetDeadline(t)
}

func (d *DtlsPacketConn) SetReadDeadline(t time.Time) error {
	return d.conn.SetReadDeadline(t)
}

func (d *DtlsPacketConn) SetWriteDeadline(t time.Time) error {
	return d.conn.SetWriteDeadline(t)
}

// NewKCPOverDTLS создаёт KCP-сессию поверх DTLS-соединения.
// isServer: true — серверная сторона (listener), false — клиентская (dialer).
func NewKCPOverDTLS(dtlsConn net.Conn, isServer bool, profile Profile, fec FEC) (*kcp.UDPSession, error) {
	pc := NewDtlsPacketConn(dtlsConn)

	block, err := kcp.NewNoneBlockCrypt(nil) // DTLS уже шифрует
	if err != nil {
		return nil, err
	}

	var sess *kcp.UDPSession

	if isServer {
		var listener *kcp.Listener
		listener, err = kcp.ServeConn(block, fec.Data, fec.Parity, pc)
		if err != nil {
			return nil, err
		}
		if err = listener.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			_ = listener.Close()
			return nil, err
		}
		sess, err = listener.AcceptKCP()
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	} else {
		sess, err = kcp.NewConn2(dtlsConn.RemoteAddr(), block, fec.Data, fec.Parity, pc)
		if err != nil {
			return nil, err
		}
	}

	sess.SetNoDelay(profile.NoDelay, profile.Interval, profile.Resend, profile.NC)
	sess.SetWindowSize(profile.SndWnd, profile.RcvWnd)
	sess.SetMtu(profile.MTU)
	sess.SetACKNoDelay(profile.ACKNoDelay)

	return sess, nil
}

// DefaultSmuxConfig возвращает smux-конфигурацию, настроенную под TURN-туннель.
func DefaultSmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.MaxReceiveBuffer = 4 * 1024 * 1024
	cfg.MaxStreamBuffer = 1 * 1024 * 1024
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.KeepAliveTimeout = 30 * time.Second
	return cfg
}
