// Package config parses CLI flags for the client and server binaries.
//
// Parse* functions are side-effect free: they validate inputs and decode the
// wrap key, but do not touch the network, DNS, or process state. main() is
// responsible for wiring those side effects after Parse* returns.
//
// V2-7: options are grouped by domain (TURN, Obf, Proxy, VK, DNS, Log) so the
// struct shape mirrors the conceptual layers of the proxy instead of being a
// flat bag of bools. CLI flag names, defaults, and behavior are bit-exact
// compatible with the pre-V2-7 surface.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
)

const (
	dnsModeUDP             = "udp"
	dnsModeDoH             = "doh"
	dnsModeAuto            = "auto"
	defaultStreamsPerCache = 10
)

// ProxyMode selects the app-layer payload carried over the TURN tunnel.
// On the client it can be all three; on the server only UDP / TCPFwd
// (bond is auto-detected per stream by the magic prefix).
type ProxyMode string

const (
	ProxyModeUDP        ProxyMode = "udp"         // -vless=false: UDP packet relay (WireGuard)
	ProxyModeTCPFwd     ProxyMode = "tcpfwd"      // -vless=true: TCP forwarder over smux
	ProxyModeTCPFwdBond ProxyMode = "tcpfwd-bond" // -vless=true -vless-bond=true: bonded TCP across N smux sessions
)

// TURNOpts groups TURN-server-side options (where and how to reach TURN).
type TURNOpts struct {
	Host string // -turn: override the TURN server IP/host
	Port string // -port: override the TURN port
	UDP  bool   // -udp: dial TURN via UDP (default: TCP/TLS)
	N    int    // -n: number of TURN streams (client only)
}

// ObfOpts groups TURN-payload obfuscation options (SRTP-mimicry wrap).
type ObfOpts struct {
	WrapMode   bool   // -wrap: enable SRTP-mimicry AEAD wrap on TURN payload
	WrapKey    []byte // -wrap-key (decoded): 32-byte shared key; nil unless WrapMode
	GenWrapKey bool   // -gen-wrap-key: print a fresh key and exit
}

// ProxyOpts groups app-layer proxy options.
type ProxyOpts struct {
	Mode    ProxyMode // udp | tcpfwd | tcpfwd-bond (server: udp | tcpfwd)
	Listen  string    // -listen: local bind addr (client: WG/TCP entry; server: TURN entry)
	Connect string    // -connect: upstream backend addr (server only)
	Peer    string    // -peer: server-side proxy addr the client dials (client only)
}

// VKOpts groups VK-credentials and captcha options (client only).
type VKOpts struct {
	Link           string // -vk-link (sanitized to the join-code suffix)
	StreamsPerCred int    // -streams-per-cred
	ManualCaptcha  bool   // -manual-captcha
}

// DNSOpts groups DNS-resolution options (client only).
type DNSOpts struct {
	Mode    string   // -dns: udp | doh | auto
	Servers []string // -dns-servers (comma-split); nil when flag empty
}

// LogOpts groups logging options.
type LogOpts struct {
	Debug bool // -debug
}

// Client holds parsed and validated client CLI options.
type Client struct {
	TURN  TURNOpts
	Obf   ObfOpts
	Proxy ProxyOpts
	VK    VKOpts
	DNS   DNSOpts
	Log   LogOpts
}

// Server holds parsed and validated server CLI options.
type Server struct {
	Obf   ObfOpts
	Proxy ProxyOpts
	Log   LogOpts
}

// ParseClient parses args (excluding program name) into a Client.
// On flag.ErrHelp it returns (nil, flag.ErrHelp) so the caller can exit cleanly.
func ParseClient(args []string, errOut io.Writer) (*Client, error) {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	host := fs.String("turn", "", "override TURN server ip")
	port := fs.String("port", "", "override TURN port")
	listen := fs.String("listen", "127.0.0.1:9000", "listen on ip:port")
	vklink := fs.String("vk-link", "", "VK calls invite link \"https://vk.com/call/join/...\"")
	peerAddr := fs.String("peer", "", "peer server address (host:port)")
	n := fs.Int("n", 10, "connections to TURN")
	udp := fs.Bool("udp", false, "connect to TURN with UDP")
	vlessMode := fs.Bool("vless", false, "VLESS mode: forward TCP connections (for VLESS) instead of UDP packets")
	vlessBond := fs.Bool("vless-bond", false, "bond one VLESS TCP connection across all active smux sessions")
	wrapMode := fs.Bool("wrap", false, "WRAP mode: ChaCha20-XOR obfuscate DTLS packets before they reach TURN ChannelData")
	wrapKeyHex := fs.String("wrap-key", "", "32-byte hex-encoded shared key for -wrap (64 hex chars)")
	genWrapKey := fs.Bool("gen-wrap-key", false, "print a fresh 64-character hex key for -wrap-key and exit")
	streamsPerCredFlag := fs.Int("streams-per-cred", defaultStreamsPerCache, "number of TURN streams sharing one VK credential cache")
	debugFlag := fs.Bool("debug", false, "enable debug logging")
	manualCaptchaFlag := fs.Bool("manual-captcha", false, "skip auto captcha solving, use manual mode immediately")
	dnsFlag := fs.String("dns", dnsModeAuto, "DNS resolution mode: udp | doh | auto (auto tries UDP/53 first, sticky-fallback to DoH on total failure)")
	dnsServersFlag := fs.String("dns-servers", "", "comma-separated UDP/53 DNS servers to use instead of built-in defaults (e.g. carrier resolvers from Android LinkProperties). Format: ip[:port][,ip[:port]...].")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	c := &Client{
		TURN: TURNOpts{
			Host: *host,
			Port: *port,
			UDP:  *udp,
			N:    *n,
		},
		Obf: ObfOpts{
			WrapMode:   *wrapMode,
			GenWrapKey: *genWrapKey,
		},
		Proxy: ProxyOpts{
			Mode:   clientProxyMode(*vlessMode, *vlessBond),
			Listen: *listen,
			Peer:   *peerAddr,
		},
		VK: VKOpts{
			StreamsPerCred: *streamsPerCredFlag,
			ManualCaptcha:  *manualCaptchaFlag,
		},
		DNS: DNSOpts{
			Mode: *dnsFlag,
		},
		Log: LogOpts{
			Debug: *debugFlag,
		},
	}

	switch c.DNS.Mode {
	case dnsModeUDP, dnsModeDoH, dnsModeAuto:
	default:
		return nil, fmt.Errorf("invalid -dns value %q: must be udp | doh | auto", c.DNS.Mode)
	}
	if *dnsServersFlag != "" {
		c.DNS.Servers = strings.Split(*dnsServersFlag, ",")
	}

	if c.Obf.GenWrapKey {
		return c, nil
	}

	if c.Proxy.Peer == "" {
		return nil, errors.New("need peer address")
	}
	if *vklink == "" {
		return nil, errors.New("need vk-link")
	}
	key, err := srtpmimicry.DecodeKey(c.Obf.WrapMode, *wrapKeyHex)
	if err != nil {
		return nil, err
	}
	c.Obf.WrapKey = key
	if c.VK.StreamsPerCred <= 0 {
		return nil, fmt.Errorf("-streams-per-cred must be positive")
	}
	if c.TURN.N <= 0 {
		c.TURN.N = 10
	}

	parts := strings.Split(*vklink, "join/")
	link := parts[len(parts)-1]
	if idx := strings.IndexAny(link, "/?#"); idx != -1 {
		link = link[:idx]
	}
	c.VK.Link = link

	return c, nil
}

// ParseServer parses args (excluding program name) into a Server.
func ParseServer(args []string, errOut io.Writer) (*Server, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	listen := fs.String("listen", "0.0.0.0:56000", "listen on ip:port")
	connect := fs.String("connect", "", "connect to ip:port")
	vlessMode := fs.Bool("vless", false, "VLESS mode: forward TCP connections (for VLESS) instead of UDP packets")
	wrapMode := fs.Bool("wrap", false, "WRAP mode: ChaCha20-XOR obfuscate DTLS packets before they reach TURN ChannelData")
	wrapKeyHex := fs.String("wrap-key", "", "32-byte hex-encoded shared key for -wrap (64 hex chars)")
	genWrapKey := fs.Bool("gen-wrap-key", false, "print a fresh 64-character hex key for -wrap-key and exit")
	debugFlag := fs.Bool("debug", false, "enable debug logging")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	s := &Server{
		Obf: ObfOpts{
			WrapMode:   *wrapMode,
			GenWrapKey: *genWrapKey,
		},
		Proxy: ProxyOpts{
			Mode:    serverProxyMode(*vlessMode),
			Listen:  *listen,
			Connect: *connect,
		},
		Log: LogOpts{
			Debug: *debugFlag,
		},
	}

	if s.Obf.GenWrapKey {
		return s, nil
	}

	if s.Proxy.Connect == "" {
		return nil, fmt.Errorf("server address is required")
	}
	key, err := srtpmimicry.DecodeKey(s.Obf.WrapMode, *wrapKeyHex)
	if err != nil {
		return nil, err
	}
	s.Obf.WrapKey = key

	return s, nil
}

func clientProxyMode(vless, bond bool) ProxyMode {
	switch {
	case vless && bond:
		return ProxyModeTCPFwdBond
	case vless:
		return ProxyModeTCPFwd
	default:
		return ProxyModeUDP
	}
}

func serverProxyMode(vless bool) ProxyMode {
	if vless {
		return ProxyModeTCPFwd
	}
	return ProxyModeUDP
}
