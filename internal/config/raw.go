package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcpmux"
	"github.com/samosvalishe/free-turn-proxy/internal/tunnel"
	"github.com/samosvalishe/free-turn-proxy/internal/uri"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// raw хранит неразобранные строковые опции до этапа assemble.
type raw struct {
	Turn   string
	Port   string
	Listen string
	Peer   string

	Provider string
	Link     string
	Links    string

	N              int
	StreamsPerCred int
	Transport      string
	Mode           string
	Bond           bool

	ObfProfile string
	ObfKey     string
	ObfTiming  time.Duration
	GenObfKey  bool

	ManualCaptcha bool
	Platform      string

	DNSMode    string
	DNSServers string

	Debug    bool
	ClientID string
	SubURL   string
	Routes   bool

	TunnelMode   string
	TunnelConfig string
	TunnelMTU    int

	KCP kcpmux.Profile
}

// applyURI применяет параметры freeturn:// поверх raw-опций.
func (r *raw) applyURI(u *uri.Config) {
	override(&r.Provider, u.Provider)
	override(&r.Transport, u.Transport)
	override(&r.Mode, u.Mode)
	r.Bond = r.Bond || u.Bond
	if u.KCP != nil {
		r.KCP = kcpmux.Profile{
			NoDelay:    u.KCP.NoDelay,
			Interval:   u.KCP.Interval,
			Resend:     u.KCP.Resend,
			NC:         u.KCP.NC,
			SndWnd:     u.KCP.SndWnd,
			RcvWnd:     u.KCP.RcvWnd,
			MTU:        u.KCP.MTU,
			ACKNoDelay: u.KCP.ACKNoDelay,
		}
	}
	if u.N > 0 {
		r.N = u.N
	}
	if u.StreamsPerCred > 0 {
		r.StreamsPerCred = u.StreamsPerCred
	}
	override(&r.ObfProfile, u.ObfProfile)
	override(&r.ObfKey, u.ObfKey)

	if u.ObfTimingMs > 0 && u.ObfProfile != "" && u.ObfProfile != string(ObfProfileNone) {
		r.ObfTiming = time.Duration(u.ObfTimingMs) * time.Millisecond
	}

	if u.VKLink != "" && r.Links == "" && r.Link == "" {
		r.Links = u.VKLink
	}
	override(&r.Peer, u.Peer)
	override(&r.ClientID, u.ClientID)
	override(&r.Listen, u.Listen)
	override(&r.DNSMode, u.DNSMode)
	override(&r.DNSServers, u.DNSServers)
	r.ManualCaptcha = r.ManualCaptcha || u.ManualCaptcha
	if u.WGConf != "" {
		r.TunnelMode = "awg"
		r.TunnelConfig = u.WGConf
	}
}

func override[T comparable](dst *T, v T) {
	var zero T
	if v != zero {
		*dst = v
	}
}

func assemble(r raw) (*Client, error) {
	switch r.Transport {
	case TransportTCP, TransportUDP:
	default:
		return nil, fmt.Errorf("invalid -transport value %q: must be %s | %s", r.Transport, TransportTCP, TransportUDP)
	}
	switch r.Mode {
	case ModeUDP, ModeTCP:
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be %s | %s", r.Mode, ModeUDP, ModeTCP)
	}
	n := r.N
	if n <= 0 {
		n = DefaultStreams
		if r.Provider == ProviderDirect {
			n = DefaultDirectStreams
		}
	}
	platform := Platform(r.Platform)
	if platform == "" {
		platform = DefaultPlatform
	}
	tunnelMTU := r.TunnelMTU
	if tunnelMTU <= 0 {
		tunnelMTU = tunnel.DefaultMTU
	}

	c := &Client{
		TURN: TURNOpts{
			Host:         r.Turn,
			Port:         r.Port,
			TransportUDP: r.Transport == TransportUDP,
			N:            n,
		},
		Obf: ObfOpts{
			Profile: ObfProfile(r.ObfProfile),
			GenKey:  r.GenObfKey,
			Timing:  r.ObfTiming,
		},
		Proxy: ProxyOpts{
			Bond:   r.Bond,
			Mode:   ProxyMode(r.Mode),
			Listen: r.Listen,
			Peer:   r.Peer,
		},
		Provider: ProviderOpts{Name: r.Provider},
		VK: VKOpts{
			Links:          normalizeVKLinks(r.Links, r.Link),
			StreamsPerCred: r.StreamsPerCred,
			ManualCaptcha:  r.ManualCaptcha,
			Platform:       platform,
		},
		DNS: DNSOpts{Mode: r.DNSMode},
		Log: LogOpts{Debug: r.Debug},
		KCP: KCPOpts{Profile: r.KCP},
		Tunnel: TunnelOpts{
			Mode:   tunnel.Mode(r.TunnelMode),
			Config: r.TunnelConfig,
			MTU:    tunnelMTU,
		},
		ClientID: r.ClientID,
		SubURL:   r.SubURL,
		Routes:   r.Routes,
	}

	if r.DNSServers != "" {
		c.DNS.Servers = strings.Split(r.DNSServers, ",")
	}

	if c.Obf.GenKey {
		return c, nil
	}
	if err := validateObfProfile(c.Obf.Profile); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(c.Obf.Enabled(), r.ObfKey)
	if err != nil {
		return nil, err
	}
	c.Obf.Key = key

	return c, nil
}

// normalizeVKLinks извлекает join-код звонка из URL или строки кода.
func normalizeVKLinks(links, link string) []string {
	items := strings.Split(links, ",")
	if len(items) == 1 && items[0] == "" {
		items = []string{link}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.Split(item, "join/")
		code := parts[len(parts)-1]
		if idx := strings.IndexAny(code, "/?#"); idx != -1 {
			code = code[:idx]
		}
		if code != "" {
			out = append(out, code)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
