package config

import (
	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcpmux"
	"github.com/samosvalishe/free-turn-proxy/internal/tunnel"
)

const (
	TransportTCP = "tcp"
	TransportUDP = "udp"

	ModeUDP = "udp"
	ModeTCP = "tcp"
)

const (
	DNSModePlain = dnsdial.DNSModePlain
	DNSModeDoH   = dnsdial.DNSModeDoH
	DNSModeAuto  = dnsdial.DNSModeAuto
)

const (
	DefaultListen         = "127.0.0.1:9000"
	DefaultStreams        = 12
	DefaultStreamsPerCred = 12
	DefaultTransport      = TransportTCP
	DefaultMode           = ModeUDP
	DefaultDNSMode        = DNSModeAuto
	DefaultProvider       = ProviderVK
	DefaultPlatform       = PlatformDesktop
	DefaultObfProfile     = ObfProfileNone

	DefaultServerListen = "0.0.0.0:56000"
)

func defaultRaw() raw {
	return raw{
		Listen:         DefaultListen,
		Provider:       DefaultProvider,
		N:              DefaultStreams,
		StreamsPerCred: DefaultStreamsPerCred,
		Transport:      DefaultTransport,
		Mode:           DefaultMode,
		ObfProfile:     string(DefaultObfProfile),
		Platform:       string(DefaultPlatform),
		DNSMode:        DefaultDNSMode,
		Routes:         false,
		TunnelMode:     string(tunnel.ModeNone),
		TunnelMTU:      tunnel.DefaultMTU,
		KCP:            kcpmux.DefaultProfile(),
	}
}

// Defaults возвращает конфигурацию клиента с дефолтными значениями (без peer/links).
func Defaults() Client {
	c, err := assemble(defaultRaw())
	if err != nil {
		panic("config: defaults must assemble: " + err.Error())
	}
	return *c
}

// ServerDefaults возвращает конфигурацию сервера с дефолтными значениями.
func ServerDefaults() Server {
	return Server{
		Obf:   ObfOpts{Profile: DefaultObfProfile},
		Proxy: ProxyOpts{Mode: ProxyModeUDP, Listen: DefaultServerListen},
		KCP:   KCPOpts{Profile: kcpmux.DefaultProfile()},
	}
}
