// Package dnsdial управляет DNS-резолвингом и dialer для outbound HTTP/TLS (UDP/53, DoH, auto fallback).
package dnsdial

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netctl"

	// Mozilla CA roots для сборок без CGO.
	_ "golang.org/x/crypto/x509roots/fallback"
)

var logHolder logx.Holder

func SetLogger(l logx.Logger) { logHolder.Set(l) }

func Log() logx.Logger { return logHolder.Get() }

const (
	dohQueryTimeout = 6 * time.Second
	// dohForwardBudget кратен dohQueryTimeout для прохода по всем fallback-эндпоинтам.
	dohForwardBudget    = 25 * time.Second
	dohMaxResponseBytes = 64 * 1024
	dohContentType      = "application/dns-message"

	dohDialerTimeout   = 5 * time.Second
	dohDialerKeepAlive = 30 * time.Second
	appDialerTimeout   = 20 * time.Second
	appDialerKeepAlive = 30 * time.Second

	forwarderUDPBufSize = 4096
	forwarderTCPReadDL  = 30 * time.Second
	forwarderTCPWriteDL = 10 * time.Second
	autoUDPBudget       = 1500 * time.Millisecond
)

// DohEndpoint задаёт DNS-over-HTTPS сервер со статическими bootstrap-IP.
type DohEndpoint struct {
	URL          string
	Hostname     string
	BootstrapIPs []string
}

// Yandex в начале списка из-за доступности у RU-операторов; остальные - fallback.
var defaultDohEndpoints = []DohEndpoint{
	{"https://common.dot.dns.yandex.net/dns-query", "common.dot.dns.yandex.net", []string{"77.88.8.8", "77.88.8.1"}},
	{"https://secure.dot.dns.yandex.net/dns-query", "secure.dot.dns.yandex.net", []string{"77.88.8.88", "77.88.8.2"}},
	{"https://family.dot.dns.yandex.net/dns-query", "family.dot.dns.yandex.net", []string{"77.88.8.7", "77.88.8.3"}},
}

type DohResolver struct {
	endpoints []DohEndpoint
	client    *http.Client
}

// NewDohResolver создаёт DoH резолвер с прямым bootstrap IP dialer без системного DNS.
func NewDohResolver(endpoints []DohEndpoint) *DohResolver {
	if len(endpoints) == 0 {
		endpoints = defaultDohEndpoints
	}
	return &DohResolver{
		endpoints: endpoints,
		client:    &http.Client{Timeout: dohQueryTimeout, Transport: newBootstrapTransport(endpoints)},
	}
}

func newDohResolverWithClient(endpoints []DohEndpoint, client *http.Client) *DohResolver {
	return &DohResolver{endpoints: endpoints, client: client}
}

func newBootstrapTransport(endpoints []DohEndpoint) *http.Transport {
	bootstrap := make(map[string][]string, len(endpoints))
	for _, ep := range endpoints {
		bootstrap[ep.Hostname] = ep.BootstrapIPs
	}
	dialer := &net.Dialer{Timeout: dohDialerTimeout, KeepAlive: dohDialerKeepAlive, Control: netctl.Apply}

	return &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, ok := bootstrap[host]
			if !ok {
				return nil, fmt.Errorf("doh: no bootstrap IPs for %q", host)
			}
			var lastErr error
			for _, ip := range ips {
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			return nil, lastErr
		},
		// Явный SNI гарантирует передачу hostname при TCP-dial по bootstrap-IP в HTTP/2.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, ok := bootstrap[host]
			if !ok {
				return nil, fmt.Errorf("doh: no bootstrap IPs for %q", host)
			}
			var lastErr error
			for _, ip := range ips {
				rawConn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if derr != nil {
					lastErr = derr
					continue
				}
				tlsConn := tls.Client(rawConn, &tls.Config{
					MinVersion: tls.VersionTLS12,
					ServerName: host,
					NextProtos: []string{"h2", "http/1.1"},
				})
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					_ = rawConn.Close()
					lastErr = err
					continue
				}
				return tlsConn, nil
			}
			return nil, lastErr
		},
	}
}

// forwardRaw отправляет DNS-wire запрос к эндпоинтам по очереди с таймаутом на попытку.
func (r *DohResolver) forwardRaw(ctx context.Context, query []byte) ([]byte, error) {
	if len(r.endpoints) == 0 {
		return nil, errors.New("doh: no endpoints configured")
	}
	var lastErr error
	for _, ep := range r.endpoints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		epCtx, cancel := context.WithTimeout(ctx, dohQueryTimeout)
		body, err := r.postWire(epCtx, ep, query)
		cancel()
		if err != nil {
			Log().Warnf("[DoH] %s: %v", ep.Hostname, err)
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

func (r *DohResolver) postWire(ctx context.Context, ep DohEndpoint, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(query))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", dohContentType)
	req.Header.Set("Accept", dohContentType)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// dohForwarder - локальный stub DNS сервер, проксирующий raw DNS-пакеты в DoH.
type dohForwarder struct {
	udpAddr string
	tcpAddr string
}

var (
	dohForwarderOnce sync.Once
	dohForwarderInst *dohForwarder
	dohForwarderErr  error
	// Резолвер подменяется на лету: ядро перезапускается в том же процессе (mobile), а Once держит листенеры.
	dohForwarderRes atomic.Pointer[DohResolver]
)

func sharedDohForwarder(r *DohResolver) (*dohForwarder, error) {
	dohForwarderRes.Store(r)
	dohForwarderOnce.Do(func() {
		dohForwarderInst, dohForwarderErr = startDohForwarder()
	})
	return dohForwarderInst, dohForwarderErr
}

func currentDohResolver() *DohResolver { return dohForwarderRes.Load() }

func startDohForwarder() (_ *dohForwarder, err error) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("doh forwarder: listen UDP: %w", err)
	}
	defer func() {
		if err != nil {
			_ = udpConn.Close()
		}
	}()
	tcpLn, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("doh forwarder: listen TCP: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tcpLn.Close()
		}
	}()

	fwd := &dohForwarder{
		udpAddr: udpConn.LocalAddr().String(),
		tcpAddr: tcpLn.Addr().String(),
	}
	Log().Infof("[DoH] forwarder listening udp=%s tcp=%s", fwd.udpAddr, fwd.tcpAddr)

	go fwd.serveUDP(udpConn)
	go fwd.serveTCP(tcpLn)
	return fwd, nil
}

func (*dohForwarder) serveUDP(conn *net.UDPConn) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, forwarderUDPBufSize)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			Log().Errorf("[DoH] udp read: %v", err)
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func(q []byte, c *net.UDPAddr) {
			ctx, cancel := context.WithTimeout(context.Background(), dohForwardBudget)
			defer cancel()
			resp, err := currentDohResolver().forwardRaw(ctx, q)
			if err != nil {
				Log().Warnf("[DoH] udp forward failed: %v", err)
				return
			}
			if _, err := conn.WriteToUDP(resp, c); err != nil {
				Log().Warnf("[DoH] udp write: %v", err)
			}
		}(query, client)
	}
}

func (*dohForwarder) serveTCP(ln *net.TCPListener) {
	defer func() { _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			Log().Errorf("[DoH] tcp accept: %v", err)
			return
		}
		go handleDohForwarderTCP(conn)
	}
}

func handleDohForwarderTCP(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(forwarderTCPReadDL)) //nolint:errcheck
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		qlen := int(lenBuf[0])<<8 | int(lenBuf[1])
		if qlen == 0 || qlen > forwarderUDPBufSize {
			return
		}
		query := make([]byte, qlen)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), dohForwardBudget)
		resp, err := currentDohResolver().forwardRaw(ctx, query)
		cancel()
		if err != nil {
			Log().Warnf("[DoH] tcp forward failed: %v", err)
			return
		}
		if len(resp) > 0xFFFF {
			Log().Warnf("[DoH] response too large for TCP framing: %d", len(resp))
			return
		}
		out := make([]byte, 2+len(resp))
		respLen := uint16(len(resp)) //nolint:gosec // bounded above
		binary.BigEndian.PutUint16(out[:2], respLen)
		copy(out[2:], resp)
		_ = conn.SetWriteDeadline(time.Now().Add(forwarderTCPWriteDL)) //nolint:errcheck
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

func dohForwarderDial(r *DohResolver) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		fwd, err := sharedDohForwarder(r) //nolint:contextcheck
		if err != nil {
			return nil, err
		}
		var d net.Dialer
		switch network {
		case "tcp", "tcp4", "tcp6":
			return d.DialContext(ctx, "tcp", fwd.tcpAddr)
		default:
			return d.DialContext(ctx, "udp", fwd.udpAddr)
		}
	}
}

const (
	DNSModePlain = "plain"
	DNSModeDoH   = "doh"
	DNSModeAuto  = "auto"
)

var udpDNSServersPtr atomic.Pointer[[]string]

func init() { ResetUDPDNSServers() }

func ResetUDPDNSServers() {
	def := []string{
		"77.88.8.8:53", "77.88.8.1:53",
		"8.8.8.8:53", "8.8.4.4:53",
		"1.1.1.1:53", "1.0.0.1:53",
	}
	udpDNSServersPtr.Store(&def)
}

func udpDNSServers() []string { return *udpDNSServersPtr.Load() }

// SetUDPDNSServers атомарно обновляет список UDP/53 серверов (IP или IP:Port).
func SetUDPDNSServers(servers []string) {
	if len(servers) == 0 {
		return
	}
	normalized := make([]string, 0, len(servers))
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "53")
		}
		normalized = append(normalized, s)
	}
	if len(normalized) == 0 {
		return
	}
	udpDNSServersPtr.Store(&normalized)
}

type dialFunc = func(context.Context, string, string) (net.Conn, error)

func buildDialer(mode string, r *DohResolver) net.Dialer {
	switch mode {
	case DNSModePlain:
		return newAppDialer(udpDNSDial)
	case DNSModeDoH:
		return newAppDialer(dohForwarderDial(r))
	case DNSModeAuto:
		return newAppDialer(autoDial(r))
	default:
		panic(fmt.Sprintf("unknown DNS mode %q", mode))
	}
}

func newAppDialer(dial dialFunc) net.Dialer {
	return net.Dialer{
		Timeout:   appDialerTimeout,
		KeepAlive: appDialerKeepAlive,
		Resolver:  &net.Resolver{PreferGo: true, Dial: dial},
		Control:   netctl.Apply,
	}
}

func udpDNSDial(ctx context.Context, _ string, _ string) (net.Conn, error) {
	var (
		d       = net.Dialer{Control: netctl.Apply}
		lastErr error
	)
	for _, s := range udpDNSServers() {
		conn, err := d.DialContext(ctx, "udp", s)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no UDP DNS servers available")
	}
	return nil, lastErr
}

// autoDial выполняет DNS probe по UDP/53 и при недоступности переключается на DoH.
func autoDial(r *DohResolver) dialFunc {
	var (
		probed sync.Once
		useDoH atomic.Bool
		doh    = dohForwarderDial(r)
	)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		probed.Do(func() {
			if udpProbe(autoUDPBudget) {
				Log().Infof("[DNS] UDP/53 probe OK, using UDP")
			} else {
				Log().Warnf("[DNS] UDP/53 unreachable; sticky-switching to DoH")
				useDoH.Store(true)
			}
		})
		if useDoH.Load() {
			return doh(ctx, network, addr)
		}
		return udpDNSDial(ctx, network, addr)
	}
}

// udpProbe проверяет доступность UDP/53 реальным DNS-запросом к первым серверам.
func udpProbe(timeout time.Duration) bool {
	m := new(dns.Msg)
	m.SetQuestion("dns.google.", dns.TypeA)
	m.RecursionDesired = true
	wire, err := m.Pack()
	if err != nil {
		return false
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 512)
	servers := udpDNSServers()
	limit := min(len(servers), 2)
	for _, server := range servers[:limit] {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		// netctl.Apply обязателен для исключения сокета пробы из VPN-туннеля.
		d := net.Dialer{Timeout: remaining, Control: netctl.Apply}
		conn, err := d.Dial("udp", server) //nolint:noctx
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(deadline) //nolint:errcheck
		_, _ = conn.Write(wire)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err == nil && n > 12 {
			return true
		}
	}
	return false
}
