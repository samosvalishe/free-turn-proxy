package uri

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const scheme = "freeturn://"

const currentVersion = 1

// Config представляет параметры подключения из share-ссылки freeturn://.
type Config struct {
	Version        int
	Provider       string
	Peer           string
	Transport      string
	Mode           string
	ObfProfile     string
	ObfKey         string
	ObfTimingMs    int
	N              int
	StreamsPerCred int
	ClientID       string
	Listen         string
	DNSMode        string
	DNSServers     string
	ManualCaptcha  bool
	KCP            *KCP
	Comment        string
	VKLink         string
	WGConf         string
}

// wire - JSON-схема freeturn:// ссылок.
type wire struct {
	V              int    `json:"v"`
	Provider       string `json:"provider"`
	Peer           string `json:"peer"`
	Transport      string `json:"transport,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Obf            string `json:"obf,omitempty"`
	Key            string `json:"key,omitempty"`
	TimingMs       int    `json:"timing,omitempty"`
	N              int    `json:"n,omitempty"`
	StreamsPerCred int    `json:"spc,omitempty"`
	ClientID       string `json:"cid,omitempty"`
	Listen         string `json:"listen,omitempty"`
	DNSMode        string `json:"dns,omitempty"`
	DNSServers     string `json:"dnss,omitempty"`
	ManualCaptcha  bool   `json:"mcap,omitempty"`
	KCP            *KCP   `json:"kcp,omitempty"`
	Name           string `json:"name,omitempty"`
	VK             string `json:"vk,omitempty"`
	WGConf         string `json:"wg,omitempty"`
}

// Parse разбирает строку freeturn://<base64url(json)>.
func Parse(s string) (*Config, error) {
	if !strings.HasPrefix(s, scheme) {
		return nil, errors.New("invalid scheme, expected freeturn://")
	}
	payload := strings.TrimPrefix(s, scheme)
	if payload == "" {
		return nil, errors.New("empty payload")
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("invalid base64 payload")
	}

	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, errors.New("invalid json payload")
	}
	if w.V != currentVersion {
		return nil, errors.New("unsupported link version")
	}
	if w.Provider == "" {
		return nil, errors.New("missing provider")
	}
	if w.Peer == "" {
		return nil, errors.New("missing peer")
	}

	return &Config{
		Version:        w.V,
		Provider:       w.Provider,
		Peer:           w.Peer,
		Transport:      w.Transport,
		Mode:           w.Mode,
		ObfProfile:     w.Obf,
		ObfKey:         w.Key,
		ObfTimingMs:    w.TimingMs,
		N:              w.N,
		StreamsPerCred: w.StreamsPerCred,
		ClientID:       w.ClientID,
		Listen:         w.Listen,
		DNSMode:        w.DNSMode,
		DNSServers:     w.DNSServers,
		ManualCaptcha:  w.ManualCaptcha,
		KCP:            w.KCP,
		Comment:        w.Name,
		VKLink:         w.VK,
		WGConf:         w.WGConf,
	}, nil
}

// String кодирует Config в freeturn://<base64url(json)>.
func (c *Config) String() string {
	w := wire{
		V:              currentVersion,
		Provider:       c.Provider,
		Peer:           c.Peer,
		Transport:      c.Transport,
		Mode:           c.Mode,
		N:              c.N,
		StreamsPerCred: c.StreamsPerCred,
		ClientID:       c.ClientID,
		Listen:         c.Listen,
		DNSMode:        c.DNSMode,
		DNSServers:     c.DNSServers,
		ManualCaptcha:  c.ManualCaptcha,
		KCP:            c.KCP,
		Name:           c.Comment,
		VK:             c.VKLink,
		WGConf:         c.WGConf,
	}
	if c.ObfProfile != "" && c.ObfProfile != "none" {
		w.Obf = c.ObfProfile
		w.Key = c.ObfKey
		w.TimingMs = c.ObfTimingMs
	}

	raw, err := json.Marshal(w)
	if err != nil {
		return ""
	}
	return scheme + base64.RawURLEncoding.EncodeToString(raw)
}

type KCP struct {
	NoDelay    int  `json:"nodelay"`
	Interval   int  `json:"interval"`
	Resend     int  `json:"resend"`
	NC         int  `json:"nc"`
	SndWnd     int  `json:"sndwnd"`
	RcvWnd     int  `json:"rcvwnd"`
	MTU        int  `json:"mtu"`
	ACKNoDelay bool `json:"acknodelay"`
}
