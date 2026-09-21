// Package config описывает структуры конфигурации клиента и сервера.
package config

import (
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/tunnel"
)

type TURNOpts struct {
	Host         string
	Port         string
	TransportUDP bool
	N            int
}

// ObfProfile - wire-профиль обфускации TURN-payload (internal/wire/<profile>/).
type ObfProfile string

const (
	ObfProfileNone     ObfProfile = "none"
	ObfProfileRTPOpus  ObfProfile = "rtpopus"  // RTP/opus + ChaCha20-Poly1305 AEAD
	ObfProfileRTPOpus2 ObfProfile = "rtpopus2" // rtpopus + RTP header extension (мимикрия под WebRTC)
	ObfProfileRTPOpus3 ObfProfile = "rtpopus3" // rtpopus2 + abs-send-time + VAD + loss simulation + variable ts
)

type ObfOpts struct {
	Profile ObfProfile
	Key     []byte
	GenKey  bool
	Timing  time.Duration
}

func (o ObfOpts) Enabled() bool { return o.Profile != ObfProfileNone }

// ProxyMode задаёт тип прикладного трафика в TURN-туннеле.
type ProxyMode string

const (
	ProxyModeUDP ProxyMode = "udp" // UDP-релей датаграмм (WireGuard)
	ProxyModeTCP ProxyMode = "tcp" // TCP-форвардер через KCP+smux (Xray/sing-box)
)

type ProxyOpts struct {
	Mode    ProxyMode
	Listen  string
	Connect string
	Peer    string
}

type Platform string

const (
	PlatformDesktop Platform = "desktop"
	PlatformMobile  Platform = "mobile"
)

type VKOpts struct {
	Links          []string
	StreamsPerCred int
	ManualCaptcha  bool
	Platform       Platform
}

type ProviderOpts struct {
	Name string
}

const (
	ProviderVK     = "vk"
	ProviderDirect = "direct"
)

type DNSOpts struct {
	Mode    string
	Servers []string
}

type LogOpts struct {
	Debug bool
}

// TunnelOpts - параметры встроенного WireGuard/AmneziaWG userspace-туннеля.
type TunnelOpts struct {
	Mode   tunnel.Mode
	Config string
	MTU    int
}

func (t TunnelOpts) Enabled() bool { return t.Mode != "" && t.Mode != tunnel.ModeNone }

type Client struct {
	TURN     TURNOpts
	Obf      ObfOpts
	Proxy    ProxyOpts
	Provider ProviderOpts
	VK       VKOpts
	DNS      DNSOpts
	Log      LogOpts
	KCP      KCPOpts
	Tunnel   TunnelOpts
	ClientID string
	SubURL   string
	Routes   bool
}

type Server struct {
	Obf         ObfOpts
	Proxy       ProxyOpts
	Log         LogOpts
	KCP         KCPOpts
	ClientsFile string
}
