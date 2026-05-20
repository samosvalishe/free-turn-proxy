// Package config parses CLI flags for the client and server binaries.
//
// Parse* functions are side-effect free: they validate inputs and decode the
// wrap key, but do not touch the network, DNS, or process state. main() is
// responsible for wiring those side effects after Parse* returns.
//
// Options are grouped by domain (TURN, Obf, Proxy, VK, DNS, Log) so the struct
// shape mirrors the conceptual layers of the proxy.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/cacggghp/vk-turn-proxy/internal/transport/kcptun"
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

// KCPOpts groups KCP tunnel options. Both sides of a tunnel must agree on
// Profile and FEC; values currently come from VK_TURN_KCP_* env vars.
type KCPOpts struct {
	Profile kcptun.Profile
	FEC     kcptun.FEC
}

// Client holds parsed and validated client CLI options.
type Client struct {
	TURN  TURNOpts
	Obf   ObfOpts
	Proxy ProxyOpts
	VK    VKOpts
	DNS   DNSOpts
	Log   LogOpts
	KCP   KCPOpts
}

// Server holds parsed and validated server CLI options.
type Server struct {
	Obf   ObfOpts
	Proxy ProxyOpts
	Log   LogOpts
	KCP   KCPOpts
}

// ParseClient parses args (excluding program name) into a Client.
// On flag.ErrHelp it returns (nil, flag.ErrHelp) so the caller can exit cleanly.
func ParseClient(args []string, errOut io.Writer) (*Client, error) {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	host := fs.String("turn", "", "переопределить IP TURN-сервера (по умолчанию берётся из VK Calls ссылки)")
	port := fs.String("port", "", "переопределить порт TURN-сервера (по умолчанию берётся из VK Calls ссылки)")
	listen := fs.String("listen", "127.0.0.1:9000", "локальный адрес ip:port, куда подключается WireGuard или Xray клиент")
	vklink := fs.String("vk-link", "", "ссылка VK Calls вида https://vk.com/call/join/... (обязательно)")
	peerAddr := fs.String("peer", "", "адрес сервера VK TURN Proxy на VPS, host:port (обязательно)")
	n := fs.Int("n", 10, "количество параллельных TURN-потоков (соединений к TURN-реле)")
	udp := fs.Bool("udp", false, "подключаться к TURN-реле по UDP (по умолчанию TCP/TLS)")
	vlessMode := fs.Bool("vless", false, "режим TCP-форвардера (VLESS/Xray) вместо UDP-релея для WireGuard")
	vlessBond := fs.Bool("vless-bond", false, "распределять одно VLESS TCP-соединение по всем активным smux-сессиям (только с -vless)")
	wrapMode := fs.Bool("wrap", false, "маскировать TURN-payload под SRTP (RTP/opus + ChaCha20-Poly1305 AEAD) для обхода content-filter VK; ключ должен совпадать на клиенте и сервере")
	wrapKeyHex := fs.String("wrap-key", "", "общий ключ для -wrap, 32 байта в hex (64 символа)")
	genWrapKey := fs.Bool("gen-wrap-key", false, "напечатать новый ключ для -wrap-key и выйти")
	streamsPerCredFlag := fs.Int("streams-per-cred", defaultStreamsPerCache, "сколько TURN-потоков делят один кеш VK-учёток")
	debugFlag := fs.Bool("debug", false, "включить подробные debug-логи")
	manualCaptchaFlag := fs.Bool("manual-captcha", false, "пропустить авто-решение VK captcha и сразу открыть ручной режим в локальном браузере")
	dnsFlag := fs.String("dns", dnsModeAuto, "режим DNS-резолвинга: udp | doh | auto (auto: сначала UDP/53, sticky-fallback на DoH при полном отказе)")
	dnsServersFlag := fs.String("dns-servers", "", "список UDP/53 DNS-серверов через запятую вместо встроенных (напр. резолверы оператора из Android LinkProperties). Формат: ip[:port][,ip[:port]...]")

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
		KCP: KCPOpts{
			Profile: kcptun.LoadProfileFromEnv(),
			FEC:     kcptun.LoadFECFromEnv(),
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

	listen := fs.String("listen", "0.0.0.0:56000", "локальный адрес прослушивания ip:port")
	connect := fs.String("connect", "", "адрес локального бэкенда, host:port (обязательно: WireGuard 127.0.0.1:51820 или Xray 127.0.0.1:443)")
	vlessMode := fs.Bool("vless", false, "режим TCP-форвардера (VLESS/Xray) вместо UDP-релея для WireGuard; bond определяется автоматически по magic-префиксу в стриме")
	wrapMode := fs.Bool("wrap", false, "маскировать TURN-payload под SRTP (RTP/opus + ChaCha20-Poly1305 AEAD) для обхода content-filter VK; ключ должен совпадать с клиентом")
	wrapKeyHex := fs.String("wrap-key", "", "общий ключ для -wrap, 32 байта в hex (64 символа)")
	genWrapKey := fs.Bool("gen-wrap-key", false, "напечатать новый ключ для -wrap-key и выйти")
	debugFlag := fs.Bool("debug", false, "включить подробные debug-логи")

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
		KCP: KCPOpts{
			Profile: kcptun.LoadProfileFromEnv(),
			FEC:     kcptun.LoadFECFromEnv(),
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
