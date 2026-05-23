// Package config парсит CLI-флаги клиента и сервера.
//
// Функции Parse* без побочных эффектов: валидируют ввод и декодируют wrap-ключ,
// но не трогают сеть, DNS и состояние процесса. Подключение этих эффектов —
// ответственность main() после возврата Parse*.
//
// Опции сгруппированы по доменам (TURN, Obf, Proxy, VK, DNS, Log) — структура
// зеркалит концептуальные слои прокси.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/samosvalishe/btp/internal/transport/kcptun"
	"github.com/samosvalishe/btp/internal/wire/rtpopus"
)

const (
	dnsModePlain           = "plain"
	dnsModeDoH             = "doh"
	dnsModeAuto            = "auto"
	defaultStreamsPerCache = 10
)

// ProxyMode выбирает payload прикладного уровня, который идёт через TURN-туннель.
// На клиенте доступны все три; на сервере только UDP / TCPFwd
// (bond определяется автоматически per-stream по magic-префиксу).
type ProxyMode string

const (
	ProxyModeUDP        ProxyMode = "udp"         // -mode udp (default): UDP-релей пакетов (WireGuard)
	ProxyModeTCPFwd     ProxyMode = "tcpfwd"      // -mode tcp: TCP-форвардер через smux
	ProxyModeTCPFwdBond ProxyMode = "tcpfwd-bond" // -mode tcp -bond: bond TCP по N smux-сессиям
)

// TURNOpts — опции TURN-сервера (куда и как подключаться).
type TURNOpts struct {
	Host         string // -turn: переопределить IP/host TURN-сервера
	Port         string // -port: переопределить порт TURN
	TransportUDP bool   // -transport udp: подключение к TURN по UDP (по умолчанию TCP/TLS)
	N            int    // -n: число TURN-потоков (только клиент)
}

// ObfProfile выбирает wire-профиль обфускации TURN-payload.
// Профили живут в internal/wire/<profile>/ — сейчас только rtpopus,
// под добавление новых (rtph264, vp8 и т.д.).
type ObfProfile string

const (
	ObfProfileNone    ObfProfile = "none"    // обфускация отключена
	ObfProfileRTPOpus ObfProfile = "rtpopus" // RTP/opus + ChaCha20-Poly1305 AEAD
)

// ObfOpts — опции обфускации TURN-payload.
type ObfOpts struct {
	Profile ObfProfile // -obf-profile: none (default) | rtpopus
	Key     []byte     // -obf-key (декодированный): 32-байтовый общий ключ; nil если Profile=none
	GenKey  bool       // -gen-obf-key: напечатать новый ключ и выйти
}

// Enabled возвращает true когда выбран реальный профиль обфускации.
func (o ObfOpts) Enabled() bool { return o.Profile != ObfProfileNone }

// ProxyOpts — опции прокси прикладного уровня.
type ProxyOpts struct {
	Mode    ProxyMode // udp | tcpfwd | tcpfwd-bond (сервер: udp | tcpfwd)
	Listen  string    // -listen: локальный bind (клиент: WG/TCP entry; сервер: TURN entry)
	Connect string    // -connect: backend (только сервер)
	Peer    string    // -peer: адрес серверного прокси, куда дозванивается клиент (только клиент)
}

// VKOpts — опции VK-учёток и captcha (только клиент, провайдер "vk").
type VKOpts struct {
	Link           string // -link (нормализован до join-кода)
	StreamsPerCred int    // -streams-per-cred
	ManualCaptcha  bool   // -manual-captcha
}

// ProviderOpts выбирает реализацию provider.Provider.
type ProviderOpts struct {
	Name string // -provider: vk (default)
}

// Известные имена провайдеров.
const (
	ProviderVK = "vk"
)

// DNSOpts — опции DNS-резолвинга (только клиент).
type DNSOpts struct {
	Mode    string   // -dns-mode: plain | doh | auto
	Servers []string // -dns-servers (через запятую); nil если флаг пуст
}

// LogOpts — опции логирования.
type LogOpts struct {
	Debug bool // -debug
}

// KCPOpts — параметры KCP-туннеля, хардкодятся из DefaultProfile/FEC{}.
type KCPOpts struct {
	Profile kcptun.Profile
	FEC     kcptun.FEC
}

// Client — разобранные и провалидированные CLI-опции клиента.
type Client struct {
	TURN     TURNOpts
	Obf      ObfOpts
	Proxy    ProxyOpts
	Provider ProviderOpts
	VK       VKOpts
	DNS      DNSOpts
	Log      LogOpts
	KCP      KCPOpts
}

// Server — разобранные и провалидированные CLI-опции сервера.
type Server struct {
	Obf   ObfOpts
	Proxy ProxyOpts
	Log   LogOpts
	KCP   KCPOpts
}

// ParseClient разбирает args (без имени программы) в Client.
// При flag.ErrHelp возвращает (nil, flag.ErrHelp) — вызывающий выходит штатно.
func ParseClient(args []string, errOut io.Writer) (*Client, error) {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	host := fs.String("turn", "", "переопределить IP TURN-сервера (по умолчанию берётся из credentials провайдера)")
	port := fs.String("port", "", "переопределить порт TURN-сервера (по умолчанию берётся из credentials провайдера)")
	listen := fs.String("listen", "127.0.0.1:9000", "локальный адрес ip:port, куда подключается WireGuard или Xray клиент")
	providerFlag := fs.String("provider", ProviderVK, "источник TURN-реквизитов: vk (default, VK Calls API)")
	vklink := fs.String("link", "", "ссылка VK Calls вида https://vk.com/call/join/... (обязательно для -provider vk)")
	peerAddr := fs.String("peer", "", "адрес сервера VK TURN Proxy на VPS, host:port (обязательно)")
	n := fs.Int("n", 10, "количество параллельных TURN-потоков (соединений к TURN-реле)")
	transportFlag := fs.String("transport", "tcp", "транспорт до TURN-реле: tcp (TCP/TLS, default) | udp")
	modeFlag := fs.String("mode", "udp", "режим туннеля: udp (UDP-релей для WireGuard, default) | tcp (TCP-форвардер для Xray/sing-box)")
	bondFlag := fs.Bool("bond", false, "распределять одно TCP-соединение по всем активным smux-сессиям (только с -mode tcp)")
	obfProfileRaw := fs.String("obf-profile", string(ObfProfileNone), "wire-профиль обфускации TURN-payload: none (default) | rtpopus (RTP/opus + ChaCha20-Poly1305 AEAD для обхода content-filter VK); должен совпадать с сервером")
	obfKeyHex := fs.String("obf-key", "", "общий ключ для -obf-profile != none, 32 байта в hex (64 символа)")
	genObfKey := fs.Bool("gen-obf-key", false, "напечатать новый ключ для -obf-key и выйти")
	streamsPerCredFlag := fs.Int("streams-per-cred", defaultStreamsPerCache, "сколько TURN-потоков делят один кеш VK-учёток (только -provider vk)")
	debugFlag := fs.Bool("debug", false, "включить подробные debug-логи")
	manualCaptchaFlag := fs.Bool("manual-captcha", false, "пропустить авто-решение VK captcha и сразу открыть ручной режим в локальном браузере (только -provider vk)")
	dnsFlag := fs.String("dns-mode", dnsModeAuto, "транспорт резолвера клиента: plain (UDP/53) | doh (DNS-over-HTTPS) | auto (UDP/53 → sticky DoH при отказе)")
	dnsServersFlag := fs.String("dns-servers", "", "список UDP/53 DNS-серверов через запятую вместо встроенных (напр. резолверы оператора из Android LinkProperties). Формат: ip[:port][,ip[:port]...]")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	c := &Client{
		TURN: TURNOpts{
			Host:         *host,
			Port:         *port,
			TransportUDP: *transportFlag == "udp",
			N:            *n,
		},
		Obf: ObfOpts{
			Profile: ObfProfile(*obfProfileRaw),
			GenKey:  *genObfKey,
		},
		Proxy: ProxyOpts{
			Mode:   clientProxyMode(*modeFlag, *bondFlag),
			Listen: *listen,
			Peer:   *peerAddr,
		},
		Provider: ProviderOpts{
			Name: *providerFlag,
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
			Profile: kcptun.DefaultProfile(),
			FEC:     kcptun.FEC{},
		},
	}

	switch *transportFlag {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf("invalid -transport value %q: must be tcp | udp", *transportFlag)
	}
	switch *modeFlag {
	case "udp", "tcp":
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be udp | tcp", *modeFlag)
	}
	if *bondFlag && *modeFlag != "tcp" {
		return nil, fmt.Errorf("-bond requires -mode tcp")
	}
	switch c.DNS.Mode {
	case dnsModePlain, dnsModeDoH, dnsModeAuto:
	default:
		return nil, fmt.Errorf("invalid -dns-mode value %q: must be plain | doh | auto", c.DNS.Mode)
	}
	if *dnsServersFlag != "" {
		c.DNS.Servers = strings.Split(*dnsServersFlag, ",")
	}

	if c.Obf.GenKey {
		return c, nil
	}

	if c.Proxy.Peer == "" {
		return nil, errors.New("need peer address")
	}
	switch c.Provider.Name {
	case ProviderVK:
		if *vklink == "" {
			return nil, errors.New("need -link (required for -provider vk)")
		}
		if c.VK.StreamsPerCred <= 0 {
			return nil, fmt.Errorf("-streams-per-cred must be positive")
		}
		parts := strings.Split(*vklink, "join/")
		link := parts[len(parts)-1]
		if idx := strings.IndexAny(link, "/?#"); idx != -1 {
			link = link[:idx]
		}
		c.VK.Link = link
	default:
		return nil, fmt.Errorf("invalid -provider value %q: must be %s", c.Provider.Name, ProviderVK)
	}
	if err := validateObfProfile(c.Obf.Profile); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(c.Obf.Enabled(), *obfKeyHex)
	if err != nil {
		return nil, err
	}
	c.Obf.Key = key
	if c.TURN.N <= 0 {
		c.TURN.N = 10
	}

	return c, nil
}

// ParseServer разбирает args (без имени программы) в Server.
func ParseServer(args []string, errOut io.Writer) (*Server, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}

	listen := fs.String("listen", "0.0.0.0:56000", "локальный адрес прослушивания ip:port")
	connect := fs.String("connect", "", "адрес локального бэкенда, host:port (обязательно: WireGuard 127.0.0.1:51820 или Xray 127.0.0.1:443)")
	modeFlag := fs.String("mode", "udp", "режим туннеля: udp (UDP-релей для WireGuard, default) | tcp (TCP-форвардер для Xray/sing-box; bond определяется автоматически)")
	obfProfileRaw := fs.String("obf-profile", string(ObfProfileNone), "wire-профиль обфускации TURN-payload: none (default) | rtpopus (RTP/opus + ChaCha20-Poly1305 AEAD для обхода content-filter VK); должен совпадать с клиентом")
	obfKeyHex := fs.String("obf-key", "", "общий ключ для -obf-profile != none, 32 байта в hex (64 символа)")
	genObfKey := fs.Bool("gen-obf-key", false, "напечатать новый ключ для -obf-key и выйти")
	debugFlag := fs.Bool("debug", false, "включить подробные debug-логи")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	s := &Server{
		Obf: ObfOpts{
			Profile: ObfProfile(*obfProfileRaw),
			GenKey:  *genObfKey,
		},
		Proxy: ProxyOpts{
			Mode:    serverProxyMode(*modeFlag),
			Listen:  *listen,
			Connect: *connect,
		},
		Log: LogOpts{
			Debug: *debugFlag,
		},
		KCP: KCPOpts{
			Profile: kcptun.DefaultProfile(),
			FEC:     kcptun.FEC{},
		},
	}

	switch *modeFlag {
	case "udp", "tcp":
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be udp | tcp", *modeFlag)
	}

	if s.Obf.GenKey {
		return s, nil
	}

	if s.Proxy.Connect == "" {
		return nil, fmt.Errorf("server address is required")
	}
	if err := validateObfProfile(s.Obf.Profile); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(s.Obf.Enabled(), *obfKeyHex)
	if err != nil {
		return nil, err
	}
	s.Obf.Key = key

	return s, nil
}

// validateObfProfile проверяет что -obf-profile содержит известное значение.
func validateObfProfile(p ObfProfile) error {
	switch p {
	case ObfProfileNone, ObfProfileRTPOpus:
		return nil
	default:
		return fmt.Errorf("invalid -obf-profile value %q: must be %s | %s", p, ObfProfileNone, ObfProfileRTPOpus)
	}
}

func clientProxyMode(mode string, bond bool) ProxyMode {
	switch {
	case mode == "tcp" && bond:
		return ProxyModeTCPFwdBond
	case mode == "tcp":
		return ProxyModeTCPFwd
	default:
		return ProxyModeUDP
	}
}

func serverProxyMode(mode string) ProxyMode {
	if mode == "tcp" {
		return ProxyModeTCPFwd
	}
	return ProxyModeUDP
}
