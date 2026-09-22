package config

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/internal/uri"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

const uriScheme = "freeturn://"

// PeekSubURL извлекает значение флага -sub из сырых аргументов без полного парсинга.
func PeekSubURL(args []string) string {
	for i := range args {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "-sub="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "--sub="); ok {
			return v
		}
		if (a == "-sub" || a == "--sub") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// ParseClient парсит аргументы командной строки в Client.
func ParseClient(args []string, errOut io.Writer) (*Client, error) {
	r := defaultRaw()

	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}
	fs.StringVar(&r.Turn, "turn", r.Turn, "IP TURN-сервера; override creds провайдера")
	fs.StringVar(&r.Port, "port", r.Port, "порт TURN-сервера; override creds провайдера")
	fs.StringVar(&r.Listen, "listen", r.Listen, "локальный ip:port для WireGuard/Xray клиента")
	fs.StringVar(&r.Provider, "provider", r.Provider, "источник потоков: vk (TURN-реле VK Calls) | direct (напрямую на -peer)")
	fs.StringVar(&r.Link, "link", r.Link, "(устарел) одна ссылка VK Calls, используйте -links")
	fs.StringVar(&r.Links, "links", r.Links, "ссылки VK Calls через запятую: https://vk.ru/call/join/...,https://vk.ru/call/join/...")
	fs.StringVar(&r.Peer, "peer", r.Peer, "адрес сервера на VPS, host:port; обязательно")
	fs.IntVar(&r.N, "n", r.N, "число параллельных потоков; 0 - по провайдеру (vk 12, direct 1)")
	fs.StringVar(&r.Transport, "transport", r.Transport, "транспорт до TURN-реле: tcp | udp")
	fs.StringVar(&r.Mode, "mode", r.Mode, "режим туннеля: udp (WireGuard) | tcp (Xray/sing-box)")
	fs.BoolVar(&r.Bond, "bond", r.Bond, "объединять сессии для одного TCP-соединения; только -mode tcp")
	fs.StringVar(&r.ObfProfile, "obf-profile", r.ObfProfile, "wire-профиль обфускации: none | rtpopus | rtpopus2 | rtpopus3; должен совпадать с сервером")
	fs.StringVar(&r.ObfKey, "obf-key", r.ObfKey, "ключ для -obf-profile != none: 32 байта hex (64 символа)")
	fs.BoolVar(&r.GenObfKey, "gen-obf-key", r.GenObfKey, "напечатать новый -obf-key и выйти")
	fs.DurationVar(&r.ObfTiming, "obf-timing", r.ObfTiming, "межпакетная задержка для RTP-мимикрии (напр. 20ms); 0=выкл")
	fs.IntVar(&r.StreamsPerCred, "streams-per-cred", r.StreamsPerCred, "TURN-потоков на один кеш VK-creds; только -provider vk")
	fs.BoolVar(&r.Debug, "debug", r.Debug, "подробные debug-логи")
	fs.BoolVar(&r.ManualCaptcha, "manual-captcha", r.ManualCaptcha, "ручная VK captcha в браузере вместо авто; только -provider vk")
	fs.StringVar(&r.Platform, "platform", r.Platform, "класс устройства персоны VK-auth: desktop | mobile; только -provider vk")
	fs.StringVar(&r.DNSMode, "dns-mode", r.DNSMode, "резолвер клиента: plain | doh | auto")
	fs.StringVar(&r.DNSServers, "dns-servers", r.DNSServers, "свои UDP/53 DNS через запятую: ip[:port][,ip[:port]...]")
	fs.StringVar(&r.ClientID, "client-id", r.ClientID, "уникальный ID клиента (автогенерация если не задан)")
	fs.StringVar(&r.SubURL, "sub", r.SubURL, "URL подписки (sub.md) для получения списка серверов")
	fs.BoolVar(&r.Routes, "routes", r.Routes, "автоматическое управление маршрутами к TURN-серверам; требует прав администратора")
	kcp := registerKCPFlags(fs, r.KCP)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if fs.NArg() > 0 && strings.HasPrefix(fs.Arg(0), uriScheme) {
		u, err := uri.Parse(fs.Arg(0))
		if err != nil {
			return nil, fmt.Errorf("failed to parse freeturn:// URI: %w", err)
		}
		r.applyURI(u)
	}
	r.KCP = kcp.profile()

	c, err := assemble(r)
	if err != nil {
		return nil, err
	}
	if c.Obf.GenKey {
		return c, nil
	}
	if err := Validate(c); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseServer парсит аргументы командной строки в Server.
func ParseServer(args []string, errOut io.Writer) (*Server, error) {
	def := ServerDefaults()

	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	if errOut != nil {
		fs.SetOutput(errOut)
	}
	listen := fs.String("listen", def.Proxy.Listen, "локальный адрес прослушивания ip:port")
	connect := fs.String("connect", "", "локальный бэкенд host:port; обязательно: WG 127.0.0.1:51820 | Xray 127.0.0.1:443")
	mode := fs.String("mode", string(def.Proxy.Mode), "режим туннеля: udp (WireGuard) | tcp (Xray/sing-box)")
	obfProfile := fs.String("obf-profile", string(def.Obf.Profile), "wire-профиль обфускации: none | rtpopus | rtpopus2 | rtpopus3; должен совпадать с клиентом")
	obfKey := fs.String("obf-key", "", "ключ для -obf-profile != none: 32 байта hex (64 символа)")
	genObfKey := fs.Bool("gen-obf-key", false, "напечатать новый -obf-key и выйти")
	obfTiming := fs.Duration("obf-timing", 0, "межпакетная задержка для RTP-мимикрии (напр. 10ms); 0=выкл")
	debug := fs.Bool("debug", false, "подробные debug-логи")
	clientsFile := fs.String("clients-file", "", "путь к файлу clients.json для авторизации по Client ID")
	kcp := registerKCPFlags(fs, def.KCP.Profile)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	switch *mode {
	case ModeUDP, ModeTCP:
	default:
		return nil, fmt.Errorf("invalid -mode value %q: must be %s | %s", *mode, ModeUDP, ModeTCP)
	}

	s := &Server{
		Obf: ObfOpts{
			Profile: ObfProfile(*obfProfile),
			GenKey:  *genObfKey,
			Timing:  *obfTiming,
		},
		Proxy: ProxyOpts{
			Mode:    ProxyMode(*mode),
			Listen:  *listen,
			Connect: *connect,
		},
		Log:         LogOpts{Debug: *debug},
		KCP:         KCPOpts{Profile: kcp.profile()},
		ClientsFile: *clientsFile,
	}

	if s.Obf.GenKey {
		return s, nil
	}
	if err := ValidateServer(s); err != nil {
		return nil, err
	}
	key, err := rtpopus.DecodeKey(s.Obf.Enabled(), *obfKey)
	if err != nil {
		return nil, err
	}
	s.Obf.Key = key

	return s, nil
}
