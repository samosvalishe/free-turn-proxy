package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
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
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

const dtlsHandshakeConcurrency = 3

func main() {
	cfg, err := config.ParseClient(os.Args[1:], os.Stderr)
	if err != nil {
		// -help/-h: usage уже напечатан в ParseClient, выходим штатно.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// логгер ещё не создан — единственный fatal до его инициализации.
		log.Fatalf("%v", err)
	}

	logger := logx.New(cfg.Log.Debug)
	logger.Infof("Free Turn Proxy client version=%s", version)
	dnsdial.SetLogger(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signalChan
		logger.Infof("Terminating...")
		cancel()
		select {
		case <-signalChan:
		case <-time.After(5 * time.Second):
		}
		logger.Errorf("Exit...")
		cancel()
		os.Exit(1)
	}()

	if cfg.DNS.Servers != nil {
		dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		logger.Infof("[DNS] using custom UDP servers: %v", cfg.DNS.Servers)
	}
	appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
	dnsdial.InstallGlobalResolver(cfg.DNS.Mode)
	if cfg.Obf.GenKey {
		key, gerr := rtpopus.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}
	peer, err := net.ResolveUDPAddr("udp", cfg.Proxy.Peer)
	if err != nil {
		logger.Errorf("resolve peer addr: %v", err)
		os.Exit(1)
	}
	if cfg.Obf.Enabled() {
		logger.Infof("OBF profile=%s: peer server must use matching -obf-profile and -obf-key", cfg.Obf.Profile)
	}

	var connectedStreams atomic.Int32

	prov, err := buildProvider(cfg, appDialer, &connectedStreams, logger)
	if err != nil {
		logger.Errorf("provider init: %v", err)
		os.Exit(1)
	}
	logger.Infof("provider=%s", prov.Name())

	getCreds := func(ctx context.Context, streamID int) (string, string, string, error) {
		c, err := prov.GetCredentials(ctx, streamID)
		if err != nil {
			return "", "", "", err
		}
		return c.User, c.Pass, c.ServerAddr, nil
	}

	if cfg.Proxy.Mode != config.ProxyModeUDP {
		tcpDtlsDialer := &dtlsdial.Dialer{
			HandshakeTimeout: 30 * time.Second,
			HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
		}
		bondH := &bondclient.Handler{Deps: bondclient.Deps{Log: logger}}
		tcpDeps := &tcpfwd.Deps{
			DTLSDialer:  tcpDtlsDialer,
			Log:         logger,
			BondHandler: bondH.Handle,
		}
		tcpParams := &tcpfwd.Params{
			Host:         cfg.TURN.Host,
			Port:         cfg.TURN.Port,
			TransportUDP: cfg.TURN.TransportUDP,
			ObfKey:       cfg.Obf.Key,
			GetCreds:     tcpfwd.GetCredsFunc(getCreds),
			KCPProfile:   cfg.KCP.Profile,
			KCPFEC:       cfg.KCP.FEC,
		}
		if err := tcpfwd.Run(ctx, tcpDeps, tcpParams, peer, cfg.Proxy.Listen, cfg.TURN.N, cfg.Proxy.Mode == config.ProxyModeTCPFwdBond); err != nil {
			logger.Errorf("tcpfwd: %v", err)
			os.Exit(1)
		}
		return
	}

	udpDtlsDialer := &dtlsdial.Dialer{
		HandshakeTimeout: 20 * time.Second,
		HandshakeSem:     make(chan struct{}, dtlsHandshakeConcurrency),
	}
	udpParams := &udprelay.Params{
		Host:         cfg.TURN.Host,
		Port:         cfg.TURN.Port,
		TransportUDP: cfg.TURN.TransportUDP,
		ObfKey:       cfg.Obf.Key,
		GetCreds:     udprelay.GetCredsFunc(getCreds),
	}
	if err := udprelay.Run(ctx, udpDtlsDialer, prov, logger, &connectedStreams, udpParams, peer, cfg.Proxy.Listen, cfg.TURN.N); err != nil {
		if errors.Is(err, udprelay.ErrFatal) {
			logger.Errorf("udprelay: fatal: %v", err)
		} else {
			logger.Errorf("udprelay: %v", err)
		}
		os.Exit(1)
	}
}

// buildProvider выбирает реализацию provider.Provider по cfg.Provider.Name.
// Валидация имени уже выполнена в config.ParseClient.
func buildProvider(cfg *config.Client, dialer net.Dialer, connected *atomic.Int32, logger logx.Logger) (provider.Provider, error) {
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
		}, vk.DefaultManualSolver)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider.Name)
	}
}
