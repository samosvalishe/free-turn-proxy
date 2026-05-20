package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/config"
	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/bondserver"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/tcpfwdserver"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/udpserver"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

func main() {
	cfg, err := config.ParseServer(os.Args[1:], os.Stderr)
	if err != nil {
		// logger not built yet — config parse failure is the only pre-logger fatal.
		log.Fatalf("%v", err)
	}
	logger := logx.New(cfg.Log.Debug)

	if cfg.Obf.GenWrapKey {
		key, gerr := srtpmimicry.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-wrap-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}

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

	addr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Listen)
	if err != nil {
		logger.Errorf("resolve listen addr: %v", err)
		os.Exit(1)
	}
	logger.Infof("Starting server listen=%s connect=%s vless=%t wrap=%t bond-autodetect=true",
		cfg.Proxy.Listen, cfg.Proxy.Connect, cfg.Proxy.Mode == config.ProxyModeTCPFwd, cfg.Obf.WrapMode)
	if !cfg.Obf.WrapMode {
		logger.Warnf("running without -wrap: any client reaching %s can relay to %s (no shared-key auth)", cfg.Proxy.Listen, cfg.Proxy.Connect)
	}

	certificate, genErr := selfsign.GenerateSelfSigned()
	if genErr != nil {
		logger.Errorf("self-signed cert: %v", genErr)
		os.Exit(1)
	}

	dtlsOpts := []dtls.ServerOption{
		dtls.WithCertificates(certificate),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
	var listener net.Listener
	if cfg.Obf.WrapMode {
		logger.Infof("WRAP mode enabled: listener only accepts clients with matching -wrap-key")
		wrapListener, werr := srtpmimicry.Listen(addr, cfg.Obf.WrapKey)
		if werr != nil {
			logger.Errorf("wrap listen: %v", werr)
			os.Exit(1)
		}
		listener, err = dtls.NewListenerWithOptions(wrapListener, dtlsOpts...)
	} else {
		listener, err = dtls.ListenWithOptions("udp", addr, dtlsOpts...)
	}
	if err != nil {
		logger.Errorf("dtls listen: %v", err)
		os.Exit(1)
	}
	context.AfterFunc(ctx, func() {
		if err = listener.Close(); err != nil {
			logger.Errorf("listener close: %v", err)
		}
	})

	logger.Infof("Listening on %s", cfg.Proxy.Listen)

	registry := bondserver.NewRegistry(bondserver.Deps{Log: logger})

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		conn, err := listener.Accept()
		if err != nil {
			logger.Warnf("accept: %v", err)
			continue
		}
		wg.Go(func() {
			handleAccepted(ctx, logger, registry, conn, cfg)
		})
	}
}

func handleAccepted(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, conn net.Conn, cfg *config.Server) {
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			logger.Warnf("failed to close incoming connection: %s", closeErr)
		}
	}()
	logger.Debugf("Connection from %s", conn.RemoteAddr())

	ctx1, cancel1 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel1()

	dtlsConn, ok := conn.(*dtls.Conn)
	if !ok {
		logger.Errorf("Type error: expected *dtls.Conn")
		return
	}
	logger.Debugf("Start handshake")
	if err := dtlsConn.HandshakeContext(ctx1); err != nil {
		logger.Warnf("Handshake failed: %v", err)
		return
	}
	logger.Debugf("Handshake done")

	if cfg.Proxy.Mode == config.ProxyModeTCPFwd {
		tcpfwdserver.Handle(ctx, logger, registry, dtlsConn, cfg.Proxy.Connect, cfg.KCP.Profile, cfg.KCP.FEC)
	} else {
		udpserver.Handle(ctx, logger, conn, cfg.Proxy.Connect)
	}

	logger.Debugf("Connection closed: %s", conn.RemoteAddr())
}
