// Package dnsdial owns DNS resolution and the net.Dialer wired into all
// outbound HTTP/TLS clients. Selects between UDP/53, DNS-over-HTTPS, or
// auto (UDP probe → sticky DoH fallback) based on Mode.
package dnsdial

import (
	"bytes"
	"context"
	"crypto/tls"
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

	"github.com/samosvalishe/btp/internal/logx"

	// Embedded Mozilla CA roots for CGO_ENABLED=0 builds (Android).
	_ "golang.org/x/crypto/x509roots/fallback"
)

// Log is the package-level logger. Defaults to no-op; main wires it via
// SetLogger so DNS-mode output respects the global -debug flag and levels.
//
// Deprecated: dnsdial is a procedural package with global state; a future
// refactor should thread a logger through AppDialer/InstallGlobalResolver.
// SetLogger remains for backward compatibility.
var Log logx.Logger = logx.Nop()

// SetLogger installs a logger for this package.
//
// Deprecated: prefer threading a logx.Logger through AppDialer.
func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const (
	dohQueryTimeout = 6 * time.Second
	// Total budget across all endpoint attempts in forwardRaw. Must be a
	// multiple of dohQueryTimeout to give every fallback a real chance.
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

// DohEndpoint describes a single DNS-over-HTTPS server together with the IPs
// we bootstrap to — so that resolving the endpoint hostname does not itself
// require DNS.
type DohEndpoint struct {
	URL          string
	Hostname     string
	BootstrapIPs []string
}

// Yandex is tried first because it tends to stay reachable on RU mobile
// operators even when international resolvers get blocked; Google and
// Cloudflare follow as fallbacks.
var defaultDohEndpoints = []DohEndpoint{
	{"https://common.dot.dns.yandex.net/dns-query", "common.dot.dns.yandex.net", []string{"77.88.8.8", "77.88.8.1"}},
	{"https://secure.dot.dns.yandex.net/dns-query", "secure.dot.dns.yandex.net", []string{"77.88.8.88", "77.88.8.2"}},
	{"https://family.dot.dns.yandex.net/dns-query", "family.dot.dns.yandex.net", []string{"77.88.8.7", "77.88.8.3"}},
}

// DohResolver POSTs DNS wire queries to one of several DoH endpoints.
type DohResolver struct {
	endpoints []DohEndpoint
	client    *http.Client
}

// NewDohResolver constructs a resolver using defaultDohEndpoints if endpoints
// is nil. Endpoint hostnames are dialed by IP using BootstrapIPs, so the DoH
// transport never depends on the system resolver.
func NewDohResolver(endpoints []DohEndpoint) *DohResolver {
	if len(endpoints) == 0 {
		endpoints = defaultDohEndpoints
	}
	return &DohResolver{
		endpoints: endpoints,
		client:    &http.Client{Timeout: dohQueryTimeout, Transport: newBootstrapTransport(endpoints)},
	}
}

// newDohResolverWithClient is a test hook that skips the bootstrap transport.
func newDohResolverWithClient(endpoints []DohEndpoint, client *http.Client) *DohResolver {
	return &DohResolver{endpoints: endpoints, client: client}
}

// newBootstrapTransport returns an http.Transport whose DialContext only
// knows how to reach the configured DoH endpoint hostnames, by mapping each
// to its BootstrapIPs.
func newBootstrapTransport(endpoints []DohEndpoint) *http.Transport {
	bootstrap := make(map[string][]string, len(endpoints))
	for _, ep := range endpoints {
		bootstrap[ep.Hostname] = ep.BootstrapIPs
	}
	dialer := &net.Dialer{Timeout: dohDialerTimeout, KeepAlive: dohDialerKeepAlive}

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
		// Explicit DialTLSContext ensures SNI = endpoint hostname even when
		// the underlying TCP dial targets a bootstrap IP. Without this, some
		// HTTP/2 paths can leak the literal IP as ServerName and fail TLS.
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

// forwardRaw POSTs an opaque DNS-wire query to the configured DoH endpoints
// in order and returns the first successful raw response together with the
// endpoint that produced it. No parsing — useful for the local forwarder
// which needs to pass through whatever the upstream resolver answers
// (RESINFO/HTTPS/SVCB/EDNS options/…).
//
// Each endpoint gets its own per-attempt deadline (dohQueryTimeout) so a
// slow first endpoint does not consume the entire budget and starve the
// fallbacks. The parent ctx still bounds the total wait via cancel chain.
func (r *DohResolver) forwardRaw(ctx context.Context, query []byte) ([]byte, DohEndpoint, error) {
	if len(r.endpoints) == 0 {
		return nil, DohEndpoint{}, errors.New("doh: no endpoints configured")
	}
	var lastErr error
	for _, ep := range r.endpoints {
		if err := ctx.Err(); err != nil {
			return nil, DohEndpoint{}, err
		}
		epCtx, cancel := context.WithTimeout(ctx, dohQueryTimeout)
		body, err := r.postWire(epCtx, ep, query)
		cancel()
		if err != nil {
			Log.Warnf("[DoH] %s: %v", ep.Hostname, err)
			lastErr = err
			continue
		}
		return body, ep, nil
	}
	return nil, DohEndpoint{}, lastErr
}

// postWire performs a single application/dns-message POST to one endpoint.
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

// Go's net.Resolver dials this stub like a regular nameserver, which avoids
// the many edge cases of a fake-net.Conn approach (RESINFO probes, EDNS
// handshakes, truncation, …). Whatever it reads on UDP/TCP is sent verbatim
// to a DoH endpoint and the wire response is sent back to the client.

type dohForwarder struct {
	udpAddr string
	tcpAddr string
}

var (
	dohForwarderOnce sync.Once
	dohForwarderInst *dohForwarder
	dohForwarderErr  error
)

// sharedDohForwarder lazily starts a process-wide forwarder bound to the
// supplied resolver. The first caller wins; subsequent callers reuse the
// same forwarder regardless of what they pass in.
func sharedDohForwarder(r *DohResolver) (*dohForwarder, error) {
	dohForwarderOnce.Do(func() {
		dohForwarderInst, dohForwarderErr = startDohForwarder(r)
	})
	return dohForwarderInst, dohForwarderErr
}

func startDohForwarder(r *DohResolver) (_ *dohForwarder, err error) {
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
	Log.Infof("[DoH] forwarder listening udp=%s tcp=%s", fwd.udpAddr, fwd.tcpAddr)

	go fwd.serveUDP(udpConn, r)
	go fwd.serveTCP(tcpLn, r)
	return fwd, nil
}

func (f *dohForwarder) serveUDP(conn *net.UDPConn, r *DohResolver) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, forwarderUDPBufSize)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			Log.Errorf("[DoH] udp read: %v", err)
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func(q []byte, c *net.UDPAddr) {
			ctx, cancel := context.WithTimeout(context.Background(), dohForwardBudget)
			defer cancel()
			resp, _, err := r.forwardRaw(ctx, q)
			if err != nil {
				Log.Warnf("[DoH] udp forward failed: %v", err)
				return
			}
			if _, err := conn.WriteToUDP(resp, c); err != nil {
				Log.Warnf("[DoH] udp write: %v", err)
			}
		}(query, client)
	}
}

func (f *dohForwarder) serveTCP(ln *net.TCPListener, r *DohResolver) {
	defer func() { _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			Log.Errorf("[DoH] tcp accept: %v", err)
			return
		}
		go handleDohForwarderTCP(conn, r)
	}
}

func handleDohForwarderTCP(conn net.Conn, r *DohResolver) {
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

		ctx, cancel := context.WithTimeout(context.Background(), dohQueryTimeout)
		resp, _, err := r.forwardRaw(ctx, query)
		cancel()
		if err != nil {
			Log.Warnf("[DoH] tcp forward failed: %v", err)
			return
		}
		out := make([]byte, 2+len(resp))
		out[0] = byte(len(resp) >> 8)
		out[1] = byte(len(resp))
		copy(out[2:], resp)
		_ = conn.SetWriteDeadline(time.Now().Add(forwarderTCPWriteDL)) //nolint:errcheck
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// dohForwarderDial returns a Resolver.Dial that connects to the local DoH
// forwarder over UDP or TCP (whichever the resolver asked for).
func dohForwarderDial(r *DohResolver) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		fwd, err := sharedDohForwarder(r)
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
	DNSModeUDP  = "udp"
	DNSModeDoH  = "doh"
	DNSModeAuto = "auto"
)

var udpDNSServersPtr atomic.Pointer[[]string]

func init() {
	def := []string{
		"77.88.8.8:53", "77.88.8.1:53",
		"8.8.8.8:53", "8.8.4.4:53",
		"1.1.1.1:53", "1.0.0.1:53",
	}
	udpDNSServersPtr.Store(&def)
}

func udpDNSServers() []string { return *udpDNSServersPtr.Load() }

// SetUDPDNSServers replaces the default UDP/53 server list. Each entry may be
// "ip" or "ip:port" — bare IPs get :53 appended. Empty list keeps the defaults.
// Used on Android to inject the carrier's resolver IPs from
// LinkProperties.dnsServers, which often work where public DoH/DoT do not.
//
// Safe to call concurrently with resolver use — the list pointer is swapped
// atomically. Existing in-flight dials see whichever snapshot they captured.
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

// buildDialer returns a net.Dialer whose internal Go resolver uses the
// chosen DNS transport. In "auto" mode the first total-failure of UDP/53
// sticks the process onto DoH for the rest of its lifetime.
func buildDialer(mode string, r *DohResolver) net.Dialer {
	switch mode {
	case DNSModeUDP:
		return newAppDialer(udpDNSDial)
	case DNSModeDoH:
		return newAppDialer(dohForwarderDial(r))
	case DNSModeAuto:
		return newAppDialer(autoDial(r))
	default:
		panic(fmt.Sprintf("unknown DNS mode %q", mode))
	}
}

// newAppDialer wraps a Resolver.Dial with the timeouts used everywhere in
// the app for outbound TCP/HTTP connections.
func newAppDialer(dial dialFunc) net.Dialer {
	return net.Dialer{
		Timeout:   appDialerTimeout,
		KeepAlive: appDialerKeepAlive,
		Resolver:  &net.Resolver{PreferGo: true, Dial: dial},
	}
}

// udpDNSDial picks the first reachable UDP/53 resolver from udpDNSServers.
func udpDNSDial(ctx context.Context, _ string, _ string) (net.Conn, error) {
	var (
		d       net.Dialer
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

// autoDial returns a Dial that probes UDP/53 once with a real DNS round-trip;
// if the probe fails it latches onto DoH for the rest of the process. Built
// for Android, where the network can flip between Wi-Fi (UDP/53 works) and
// mobile (UDP/53 blocked).
//
// A simple dial-timeout doesn't work for UDP because UDP "dial" is
// connectionless and always succeeds instantly. The only way to know whether
// UDP/53 actually works is to send a real query and wait for a response.
func autoDial(r *DohResolver) dialFunc {
	var (
		probed sync.Once
		useDoH atomic.Bool
		doh    = dohForwarderDial(r)
	)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		probed.Do(func() {
			if udpProbe(autoUDPBudget) {
				Log.Infof("[DNS] UDP/53 probe OK, using UDP")
			} else {
				Log.Warnf("[DNS] UDP/53 unreachable; sticky-switching to DoH")
				useDoH.Store(true)
			}
		})
		if useDoH.Load() {
			return doh(ctx, network, addr)
		}
		return udpDNSDial(ctx, network, addr)
	}
}

// udpProbe sends a real DNS A query for a well-known domain via UDP and
// checks whether any response arrives within the deadline. We try the first
// two servers from udpDNSServers under a shared deadline — if neither
// responds, UDP/53 is blocked.
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
		conn, err := net.DialTimeout("udp", server, remaining)
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
