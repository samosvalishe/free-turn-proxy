// Package clientrun содержит отменяемый клиентский сервис: сборку провайдера,
// диалеров и DNS из *config.Client и запуск пайплайна (udprelay | tcpfwd).
//
// Здесь нет os.Exit и нет
// разбора флагов — Serve блокирующе работает до отмены ctx или фатала и
// возвращает error. Это позволяет переиспользовать одну логику в CLI
// (cmd/client) и в GUI (cmd/gui), где os.Exit недопустим.
package clientrun

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bondclient"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpfwd"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
)

const dtlsHandshakeConcurrency = 3

// Options — необязательные хуки сборки клиента.
type Options struct {
	// OnCaptchaURL, если задан, вызывается с локальным URL manual-captcha
	// вместо запуска системного браузера. GUI использует для показа iframe;
	// nil → дефолтное поведение (открыть браузер).
	OnCaptchaURL func(localURL string)
}

// Run — собранный, но ещё не запущенный клиент. Один Run рассчитан на один
// вызов Serve; для переподключения создавайте новый через New.
type Run struct {
	cfg       *config.Client
	logger    logx.Logger
	prov      provider.Provider
	appDialer net.Dialer
	peer      *net.UDPAddr
	connected atomic.Int32
}

// New собирает DNS, провайдера и резолвит peer из cfg. Побочные эффекты
// ограничены тем же набором, что и прежний cmd/client/main.go (установка
// глобального резолвера, кастомных UDP DNS). Возвращает ошибку вместо os.Exit.
//
// cfg.ClientID и cfg.SubURL должны быть уже разрешены вызывающим (resolveClientID,
// применение подписки) — clientrun их не трогает.
func New(cfg *config.Client, logger logx.Logger, opts Options) (*Run, error) {
	logger = logx.OrNop(logger)
	dnsdial.SetLogger(logger)

	if cfg.DNS.Servers != nil {
		dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		logger.Infof("[DNS] using custom UDP servers: %v", cfg.DNS.Servers)
	}
	appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
	dnsdial.InstallGlobalResolver(cfg.DNS.Mode)

	peer, err := net.ResolveUDPAddr("udp", cfg.Proxy.Peer)
	if err != nil {
		return nil, fmt.Errorf("resolve peer addr: %w", err)
	}
	if cfg.Obf.Enabled() {
		logger.Infof("OBF profile=%s: peer server must use matching -obf-profile and -obf-key", cfg.Obf.Profile)
	}

	r := &Run{cfg: cfg, logger: logger, appDialer: appDialer, peer: peer}

	prov, err := buildProvider(cfg, appDialer, &r.connected, logger, opts)
	if err != nil {
		return nil, fmt.Errorf("provider init: %w", err)
	}
	r.prov = prov
	logger.Infof("provider=%s", prov.Name())

	return r, nil
}

// Serve блокирующе гоняет пайплайн до отмены ctx или фатальной ошибки.
// Возвращает error (в т.ч. udprelay.ErrFatal). Никаких os.Exit.
func (r *Run) Serve(ctx context.Context) error {
	getCreds := func(ctx context.Context, streamID int) (string, string, string, error) {
		c, err := r.prov.GetCredentials(ctx, streamID)
		if err != nil {
			return "", "", "", err
		}
		return c.User, c.Pass, c.ServerAddr, nil
	}

	if r.cfg.Proxy.Mode != config.ProxyModeUDP {
		tcpDtlsDialer := &dtlsdial.Dialer{
			HandshakeTimeout: 30 * time.Second,
			HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
		}
		bondH := &bondclient.Handler{Deps: bondclient.Deps{Log: r.logger}}
		tcpDeps := &tcpfwd.Deps{
			DTLSDialer:  tcpDtlsDialer,
			Log:         r.logger,
			BondHandler: bondH.Handle,
		}
		tcpParams := &tcpfwd.Params{
			Host:         r.cfg.TURN.Host,
			Port:         r.cfg.TURN.Port,
			TransportUDP: r.cfg.TURN.TransportUDP,
			ObfKey:       r.cfg.Obf.Key,
			GetCreds:     tcpfwd.GetCredsFunc(getCreds),
			KCPProfile:   r.cfg.KCP.Profile,
			KCPFEC:       r.cfg.KCP.FEC,
			ClientID:     r.cfg.ClientID,
		}
		return tcpfwd.Run(ctx, tcpDeps, tcpParams, r.peer, r.cfg.Proxy.Listen, r.cfg.TURN.N, r.cfg.Proxy.Mode == config.ProxyModeTCPFwdBond)
	}

	udpDtlsDialer := &dtlsdial.Dialer{
		HandshakeTimeout: 20 * time.Second,
		HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
	}
	udpParams := &udprelay.Params{
		Host:         r.cfg.TURN.Host,
		Port:         r.cfg.TURN.Port,
		TransportUDP: r.cfg.TURN.TransportUDP,
		ObfKey:       r.cfg.Obf.Key,
		GetCreds:     udprelay.GetCredsFunc(getCreds),
		ClientID:     r.cfg.ClientID,
	}
	return udprelay.Run(ctx, udpDtlsDialer, r.prov, r.logger, &r.connected, udpParams, r.peer, r.cfg.Proxy.Listen, r.cfg.TURN.N)
}

// ConnectedStreams возвращает текущее число живых TURN-потоков (для UI N/M alive).
func (r *Run) ConnectedStreams() int32 { return r.connected.Load() }

// Provider даёт доступ к провайдеру (UI читает BackoffUntilUnix для countdown).
func (r *Run) Provider() provider.Provider { return r.prov }

// buildProvider выбирает реализацию provider.Provider по cfg.Provider.Name.
// Валидация имени уже выполнена в config.ParseClient.
func buildProvider(cfg *config.Client, dialer net.Dialer, connected *atomic.Int32, logger logx.Logger, opts Options) (provider.Provider, error) {
	switch cfg.Provider.Name {
	case config.ProviderVK:
		return vk.New(vk.Config{
			Link:            cfg.VK.Link,
			Dialer:          dialer,
			ManualOnly:      cfg.VK.ManualCaptcha,
			StreamsPerCache: cfg.VK.StreamsPerCred,
			StreamsAlive:    connected.Load,
			Log:             logger,
			Debug:           cfg.Log.Debug,
			OnCaptchaURL:    opts.OnCaptchaURL,
		}, vk.DefaultManualSolver)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider.Name)
	}
}
