// Package config parses CLI flags for the client and server binaries.
//
// Parse* functions are side-effect free: they validate inputs and decode the
// wrap key, but do not touch the network, DNS, or process state. main() is
// responsible for wiring those side effects after Parse* returns.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
)

// Mirrors of constants defined in internal/client/* packages, duplicated here
// because internal/config cannot import internal/client/* (those packages
// import internal/config indirectly through dependents — avoid the cycle).
const (
	dnsModeUDP             = "udp"
	dnsModeDoH             = "doh"
	dnsModeAuto            = "auto"
	defaultStreamsPerCache = 10
)

// Client holds parsed and validated client CLI options.
type Client struct {
	Host           string
	Port           string
	Listen         string
	VKLink         string // sanitized: stripped past "join/" and trimmed at /?#
	Peer           string
	N              int
	UDP            bool
	VLESSMode      bool
	VLESSBond      bool
	WrapMode       bool
	WrapKey        []byte // nil unless WrapMode
	GenWrapKey     bool
	StreamsPerCred int
	Debug          bool
	ManualCaptcha  bool
	DNSMode        string
	DNSServers     []string // nil when -dns-servers empty
}

// Server holds parsed and validated server CLI options.
type Server struct {
	Listen     string
	Connect    string
	VLESSMode  bool
	WrapMode   bool
	WrapKey    []byte
	GenWrapKey bool
	Debug      bool
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
		Host:           *host,
		Port:           *port,
		Listen:         *listen,
		Peer:           *peerAddr,
		N:              *n,
		UDP:            *udp,
		VLESSMode:      *vlessMode,
		VLESSBond:      *vlessBond,
		WrapMode:       *wrapMode,
		GenWrapKey:     *genWrapKey,
		StreamsPerCred: *streamsPerCredFlag,
		Debug:          *debugFlag,
		ManualCaptcha:  *manualCaptchaFlag,
		DNSMode:        *dnsFlag,
	}

	switch c.DNSMode {
	case dnsModeUDP, dnsModeDoH, dnsModeAuto:
	default:
		return nil, fmt.Errorf("invalid -dns value %q: must be udp | doh | auto", c.DNSMode)
	}
	if *dnsServersFlag != "" {
		c.DNSServers = strings.Split(*dnsServersFlag, ",")
	}

	// gen-wrap-key short-circuits: caller emits the key and exits, so skip
	// the rest of validation (no peer / vk-link needed for key gen).
	if c.GenWrapKey {
		return c, nil
	}

	if c.Peer == "" {
		return nil, errors.New("need peer address")
	}
	if *vklink == "" {
		return nil, errors.New("need vk-link")
	}
	key, err := wrap.DecodeKey(c.WrapMode, *wrapKeyHex)
	if err != nil {
		return nil, err
	}
	c.WrapKey = key
	if c.StreamsPerCred <= 0 {
		return nil, fmt.Errorf("-streams-per-cred must be positive")
	}
	if c.N <= 0 {
		c.N = 10
	}

	parts := strings.Split(*vklink, "join/")
	link := parts[len(parts)-1]
	if idx := strings.IndexAny(link, "/?#"); idx != -1 {
		link = link[:idx]
	}
	c.VKLink = link

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
		Listen:     *listen,
		Connect:    *connect,
		VLESSMode:  *vlessMode,
		WrapMode:   *wrapMode,
		GenWrapKey: *genWrapKey,
		Debug:      *debugFlag,
	}

	if s.GenWrapKey {
		return s, nil
	}

	if s.Connect == "" {
		return nil, fmt.Errorf("server address is required")
	}
	key, err := wrap.DecodeKey(s.WrapMode, *wrapKeyHex)
	if err != nil {
		return nil, err
	}
	s.WrapKey = key

	return s, nil
}
