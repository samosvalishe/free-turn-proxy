package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/samosvalishe/btp/internal/client/captcha"
	manualcaptcha "github.com/samosvalishe/btp/internal/client/captcha/manual"
	"github.com/samosvalishe/btp/internal/client/dnsdial"
	"github.com/samosvalishe/btp/internal/client/vkauth"
	"github.com/samosvalishe/btp/internal/config"
	"github.com/samosvalishe/btp/internal/logx"
	"github.com/samosvalishe/btp/internal/proxy/bondclient"
	"github.com/samosvalishe/btp/internal/proxy/tcpfwd"
	"github.com/samosvalishe/btp/internal/proxy/udprelay"
	"github.com/samosvalishe/btp/internal/transport/dtlsdial"
	"github.com/samosvalishe/btp/internal/wire/srtpmimicry"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

const dtlsHandshakeConcurrency = 3

// manualCaptchaSolver связывает контракт vkauth.ManualSolveFunc
// с локальным captcha-обработчиком (internal/client/captcha/manual).
func manualCaptchaSolver(ctx context.Context, e *captcha.Error, d net.Dialer) (string, string, error) {
	if e.RedirectURI != "" {
		t, err := manualcaptcha.SolveViaProxy(ctx, e.RedirectURI, d)
		return t, "", err
	}
	if e.CaptchaImg != "" {
		k, err := manualcaptcha.SolveViaHTTP(ctx, e.CaptchaImg)
		return "", k, err
	}
	return "", "", fmt.Errorf("no redirect_uri or captcha_img")
}

func main() {
	cfg, err := config.ParseClient(os.Args[1:], os.Stderr)
	if err != nil {
		// логгер ещё не создан — единственный fatal до его инициализации.
		log.Fatalf("%v", err)
	}

	logger := logx.New(cfg.Log.Debug)
	logger.Infof("btp client version=%s", version)
	captcha.SetLogger(logger)
	manualcaptcha.SetLogger(logger)
	dnsdial.SetLogger(logger)
	manualcaptcha.Debug = cfg.Log.Debug

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
		key, gerr := srtpmimicry.GenKeyHex()
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
	if cfg.Obf.Mode {
		logger.Infof("OBF mode enabled: peer server must use matching -obf-key")
	}

	var connectedStreams atomic.Int32

	vkAuth := vkauth.New(vkauth.Config{
		Dialer:          appDialer,
		ManualOnly:      cfg.VK.ManualCaptcha,
		StreamsPerCache: cfg.VK.StreamsPerCred,
		StreamsAlive:    connectedStreams.Load,
		ManualSolver:    manualCaptchaSolver,
		Log:             logger,
	})

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
			Host:       cfg.TURN.Host,
			Port:       cfg.TURN.Port,
			Link:       cfg.VK.Link,
			TransportUDP: cfg.TURN.TransportUDP,
			ObfKey:     cfg.Obf.Key,
			GetCreds:   tcpfwd.GetCredsFunc(vkAuth.GetCredentials),
			KCPProfile: cfg.KCP.Profile,
			KCPFEC:     cfg.KCP.FEC,
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
		Host:     cfg.TURN.Host,
		Port:     cfg.TURN.Port,
		Link:     cfg.VK.Link,
		TransportUDP: cfg.TURN.TransportUDP,
		ObfKey:   cfg.Obf.Key,
		GetCreds: udprelay.GetCredsFunc(vkAuth.GetCredentials),
	}
	if err := udprelay.Run(ctx, udpDtlsDialer, vkAuth, logger, &connectedStreams, udpParams, peer, cfg.Proxy.Listen, cfg.TURN.N); err != nil {
		if errors.Is(err, udprelay.ErrFatal) {
			logger.Errorf("udprelay: fatal: %v", err)
		} else {
			logger.Errorf("udprelay: %v", err)
		}
		os.Exit(1)
	}
}
