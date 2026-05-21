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
	"github.com/samosvalishe/btp/internal/wire/srtpmimicry"
)

const (
	dnsModeUDP             = "udp"
	dnsModeDoH             = "doh"
	dnsModeAuto            = "auto"
	defaultStreamsPerCache = 10
)

// ProxyMode выбирает payload прикладного уровня, который идёт через TURN-туннель.
// На клиенте доступны все три; на сервере только UDP / TCPFwd
// (bond определяется автоматически per-stream по magic-префиксу).
type ProxyMode string

const (
	ProxyModeUDP        ProxyMode = "udp"         // -vless=false: UDP-релей пакетов (WireGuard)
	ProxyModeTCPFwd     ProxyMode = "tcpfwd"      // -vless=true: TCP-форвардер через smux
	ProxyModeTCPFwdBond ProxyMode = "tcpfwd-bond" // -vless=true -vless-bond=true: bond TCP по N smux-сессиям
)

// TURNOpts — опции TURN-сервера (куда и как подключаться).
type TURNOpts struct {
	Host string // -turn: переопределить IP/host TURN-сервера
	Port string // -port: переопределить порт TURN
	UDP  bool   // -udp: подключение к TURN по UDP (по умолчанию TCP/TLS)
	N    int    // -n: число TURN-потоков (только клиент)
}

// ObfOpts — опции обфускации TURN-payload (SRTP-mimicry wrap).
type ObfOpts struct {
	WrapMode   bool   // -wrap: включить SRTP-mimicry AEAD-обёртку
	WrapKey    []byte // -wrap-key (декодированный): 32-байтовый общий ключ; nil если WrapMode=false
	GenWrapKey bool   // -gen-wrap-key: напечатать новый ключ и выйти
}

// ProxyOpts — опции прокси прикладного уровня.
type ProxyOpts struct {
	Mode    ProxyMode // udp | tcpfwd | tcpfwd-bond (сервер: udp | tcpfwd)
	Listen  string    // -listen: локальный bind (клиент: WG/TCP entry; сервер: TURN entry)
	Connect string    // -connect: backend (только сервер)
	Peer    string    // -peer: адрес серверного прокси, куда дозванивается клиент (только клиент)
}

// VKOpts — опции VK-учёток и captcha (только клиент).
type VKOpts struct {
	Link           string // -vk-link (нормализован до join-кода)
	StreamsPerCred int    // -streams-per-cred
	ManualCaptcha  bool   // -manual-captcha
}

// DNSOpts — опции DNS-резолвинга (только клиент).
type DNSOpts struct {
	Mode    string   // -dns: udp | doh | auto
	Servers []string // -dns-servers (через запятую); nil если флаг пуст
}

// LogOpts — опции логирования.
type LogOpts struct {
	Debug bool // -debug
}

// KCPOpts — опции KCP-туннеля. Обе стороны должны согласовать Profile и FEC;
// значения берутся из переменных окружения VK_TURN_KCP_*.
type KCPOpts struct {
	Profile kcptun.Profile
	FEC     kcptun.FEC
}

// Client — разобранные и провалидированные CLI-опции клиента.
type Client struct {
	TURN  TURNOpts
	Obf   ObfOpts
	Proxy ProxyOpts
	VK    VKOpts
	DNS   DNSOpts
	Log   LogOpts
	KCP   KCPOpts
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

// ParseServer разбирает args (без имени программы) в Server.
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
