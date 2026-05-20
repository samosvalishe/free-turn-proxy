package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/client/captcha"
	manualcaptcha "github.com/cacggghp/vk-turn-proxy/internal/client/captcha/manual"
	"github.com/cacggghp/vk-turn-proxy/internal/client/dnsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/client/vkauth"
	"github.com/cacggghp/vk-turn-proxy/internal/config"
	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/bondclient"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/tcpfwd"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/udprelay"
	"github.com/cacggghp/vk-turn-proxy/internal/transport/dtlsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
)

// manualCaptchaSolver bridges the vkauth.ManualSolveFunc contract to the
// local captcha bouncer (internal/client/captcha/manual).
func manualCaptchaSolver(_ context.Context, e *captcha.Error, d net.Dialer) (string, string, error) {
	if e.RedirectURI != "" {
		t, err := manualcaptcha.SolveViaProxy(e.RedirectURI, d)
		return t, "", err
	}
	if e.CaptchaImg != "" {
		k, err := manualcaptcha.SolveViaHTTP(e.CaptchaImg)
		return "", k, err
	}
	return "", "", fmt.Errorf("no redirect_uri or captcha_img")
}

func main() {
	cfg, err := config.ParseClient(os.Args[1:], os.Stderr)
	if err != nil {
		// logger not built yet — config parse failure is the only pre-logger fatal.
		log.Fatalf("%v", err)
	}

	logger := logx.New(cfg.Log.Debug)
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
		os.Exit(1)
	}()

	if cfg.DNS.Servers != nil {
		dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		logger.Infof("[DNS] using custom UDP servers: %v", cfg.DNS.Servers)
	}
	appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
	dnsdial.InstallGlobalResolver(cfg.DNS.Mode)
	if cfg.Obf.GenWrapKey {
		key, gerr := srtpmimicry.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-wrap-key: %v", gerr)
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
	if cfg.Obf.WrapMode {
		logger.Infof("WRAP mode enabled: peer server must use matching -wrap-key")
	}

	var connectedStreams atomic.Int32

	vkAuth := vkauth.New(vkauth.Config{
		Dialer:          appDialer,
		ManualOnly:      cfg.VK.ManualCaptcha,
		StreamsPerCache: cfg.VK.StreamsPerCred,
		StreamsAlive:    func() int32 { return connectedStreams.Load() },
		ManualSolver:    manualCaptchaSolver,
		Log:             logger,
	})

	if cfg.Proxy.Mode != config.ProxyModeUDP {
		vlessDtlsDialer := &dtlsdial.Dialer{HandshakeTimeout: 30 * time.Second}
		bondH := &bondclient.Handler{Deps: bondclient.Deps{Log: logger}}
		vlessDeps := &tcpfwd.Deps{
			DTLSDialer:  vlessDtlsDialer,
			Log:         logger,
			BondHandler: bondH.Handle,
		}
		vlessParams := &tcpfwd.Params{
			Host:       cfg.TURN.Host,
			Port:       cfg.TURN.Port,
			Link:       cfg.VK.Link,
			UDP:        cfg.TURN.UDP,
			WrapKey:    cfg.Obf.WrapKey,
			GetCreds:   tcpfwd.GetCredsFunc(vkAuth.GetCredentials),
			KCPProfile: cfg.KCP.Profile,
			KCPFEC:     cfg.KCP.FEC,
		}
		if err := tcpfwd.Run(ctx, vlessDeps, vlessParams, peer, cfg.Proxy.Listen, cfg.TURN.N, cfg.Proxy.Mode == config.ProxyModeTCPFwdBond); err != nil {
			logger.Errorf("tcpfwd: %v", err)
			os.Exit(1)
		}
		return
	}

	udpDtlsDialer := &dtlsdial.Dialer{
		HandshakeTimeout: 20 * time.Second,
		HandshakeSem:     make(chan struct{}, 3),
	}
	udpParams := &udprelay.Params{
		Host:     cfg.TURN.Host,
		Port:     cfg.TURN.Port,
		Link:     cfg.VK.Link,
		UDP:      cfg.TURN.UDP,
		WrapKey:  cfg.Obf.WrapKey,
		GetCreds: udprelay.GetCredsFunc(vkAuth.GetCredentials),
	}
	if err := udprelay.Run(ctx, udpDtlsDialer, vkAuth, logger, &connectedStreams, cancel, udpParams, peer, cfg.Proxy.Listen, cfg.TURN.N); err != nil {
		logger.Errorf("udprelay: %v", err)
		os.Exit(1)
	}
}
