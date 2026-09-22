package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/uri"
)

// ClientJSON - JSON-схема конфигурации клиента (gomobile).
type ClientJSON struct {
	Peer     string `json:"peer"`
	ClientID string `json:"clientId"`
	SubURL   string `json:"subUrl"`
	Provider string `json:"provider"`
	Routes   bool   `json:"routes"`

	TURN   turnJSON   `json:"turn"`
	Proxy  proxyJSON  `json:"proxy"`
	VK     vkJSON     `json:"vk"`
	Obf    obfJSON    `json:"obf"`
	DNS    dnsJSON    `json:"dns"`
	Log    logJSON    `json:"log"`
	KCP    kcpJSON    `json:"kcp"`
	Tunnel tunnelJSON `json:"tunnel"`
}

// tunnelJSON - параметры встроенного wg-quick туннеля.
type tunnelJSON struct {
	Mode   string `json:"mode"`
	Config string `json:"config"`
	MTU    int    `json:"mtu"`
}

type turnJSON struct {
	N         int    `json:"n"`
	Transport string `json:"transport"`
	Host      string `json:"host"`
	Port      string `json:"port"`
}

type proxyJSON struct {
	Bond   bool   `json:"bond,omitempty"`
	Mode   string `json:"mode"`
	Listen string `json:"listen"`
}

type vkJSON struct {
	Links          []string `json:"links"`
	StreamsPerCred int      `json:"streamsPerCred"`
	ManualCaptcha  bool     `json:"manualCaptcha"`
	Platform       string   `json:"platform"`
}

type obfJSON struct {
	Profile  string `json:"profile"`
	Key      string `json:"key"`
	TimingMs int    `json:"timingMs"`
}

type dnsJSON struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
}

type logJSON struct {
	Debug bool `json:"debug"`
}

// DefaultClientJSON возвращает дефолтную конфигурацию клиента в формате JSON.
func DefaultClientJSON() string {
	b, err := json.MarshalIndent(defaultClientJSON(), "", "  ")
	if err != nil {
		panic("config: default JSON must marshal: " + err.Error())
	}
	return string(b)
}

// PeekSubURLJSON извлекает subUrl из сырого JSON без полной валидации.
func PeekSubURLJSON(data []byte) string {
	var peek struct {
		SubURL string `json:"subUrl"`
	}
	if json.Unmarshal(data, &peek) != nil {
		return ""
	}
	return peek.SubURL
}

// ParseClientJSON парсит JSON-конфигурацию клиента с опциональным overlayURI подписки.
func ParseClientJSON(data []byte, overlayURI string) (*Client, error) {
	dto := defaultClientJSON()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dto); err != nil {
		return nil, fmt.Errorf("config json: %w", err)
	}

	r := dto.toRaw()
	if overlayURI != "" {
		u, err := uri.Parse(overlayURI)
		if err != nil {
			return nil, fmt.Errorf("failed to parse freeturn:// URI: %w", err)
		}
		r.applyURI(u)
	}

	c, err := assemble(r)
	if err != nil {
		return nil, err
	}
	if err := Validate(c); err != nil {
		return nil, err
	}
	return c, nil
}

func defaultClientJSON() ClientJSON {
	r := defaultRaw()
	return ClientJSON{
		Peer:     r.Peer,
		ClientID: r.ClientID,
		SubURL:   r.SubURL,
		Provider: r.Provider,
		Routes:   r.Routes,
		TURN: turnJSON{
			N:         r.N,
			Transport: r.Transport,
			Host:      r.Turn,
			Port:      r.Port,
		},
		Proxy: proxyJSON{Mode: r.Mode, Listen: r.Listen},
		KCP:   kcpJSONFrom(r.KCP),
		VK: vkJSON{
			StreamsPerCred: r.StreamsPerCred,
			ManualCaptcha:  r.ManualCaptcha,
			Platform:       r.Platform,
		},
		Obf: obfJSON{
			Profile:  r.ObfProfile,
			Key:      r.ObfKey,
			TimingMs: int(r.ObfTiming / time.Millisecond),
		},
		DNS: dnsJSON{Mode: r.DNSMode},
		Log: logJSON{Debug: r.Debug},
		Tunnel: tunnelJSON{
			Mode:   r.TunnelMode,
			Config: r.TunnelConfig,
			MTU:    r.TunnelMTU,
		},
	}
}

func (j ClientJSON) toRaw() raw {
	return raw{
		Turn:   j.TURN.Host,
		Port:   j.TURN.Port,
		Listen: j.Proxy.Listen,
		Peer:   j.Peer,

		Provider: j.Provider,
		Links:    strings.Join(j.VK.Links, ","),

		N:              j.TURN.N,
		StreamsPerCred: j.VK.StreamsPerCred,
		Transport:      j.TURN.Transport,
		Mode:           j.Proxy.Mode,
		Bond:           j.Proxy.Bond,

		ObfProfile: j.Obf.Profile,
		ObfKey:     j.Obf.Key,
		ObfTiming:  time.Duration(j.Obf.TimingMs) * time.Millisecond,

		ManualCaptcha: j.VK.ManualCaptcha,
		Platform:      j.VK.Platform,

		DNSMode:    j.DNS.Mode,
		DNSServers: strings.Join(j.DNS.Servers, ","),

		Debug:    j.Log.Debug,
		ClientID: j.ClientID,
		SubURL:   j.SubURL,
		Routes:   j.Routes,

		TunnelMode:   j.Tunnel.Mode,
		TunnelConfig: j.Tunnel.Config,
		TunnelMTU:    j.Tunnel.MTU,

		KCP: j.KCP.profile(),
	}
}
