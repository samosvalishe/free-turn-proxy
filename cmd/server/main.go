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

	"github.com/samosvalishe/btp/internal/config"
	"github.com/samosvalishe/btp/internal/logx"
	"github.com/samosvalishe/btp/internal/proxy/bondserver"
	"github.com/samosvalishe/btp/internal/proxy/tcpfwdserver"
	"github.com/samosvalishe/btp/internal/proxy/udpserver"
	"github.com/samosvalishe/btp/internal/transport/dtlsdial"
	"github.com/samosvalishe/btp/internal/wire/srtpmimicry"
	"github.com/pion/dtls/v3"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.ParseServer(os.Args[1:], os.Stderr)
	if err != nil {
		// логгер ещё не создан — единственный fatal до его инициализации.
		log.Fatalf("%v", err)
	}
	logger := logx.New(cfg.Log.Debug)
	logger.Infof("btp server version=%s", version)

	if cfg.Obf.GenKey {
		key, gerr := srtpmimicry.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
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
		logger.Warnf("Forced exit after shutdown timeout")
		cancel()
		os.Exit(1)
	}()

	addr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Listen)
	if err != nil {
		logger.Errorf("resolve listen addr: %v", err)
		os.Exit(1)
	}
	logger.Infof("Starting server listen=%s connect=%s mode=%s obf=%t bond-autodetect=true",
		cfg.Proxy.Listen, cfg.Proxy.Connect, cfg.Proxy.Mode, cfg.Obf.Mode)
	if !cfg.Obf.Mode {
		logger.Warnf("running without -obf: any client reaching %s can relay to %s (no shared-key auth)", cfg.Proxy.Listen, cfg.Proxy.Connect)
	}

	certificate, genErr := dtlsdial.GenerateSelfSignedCert()
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
	if cfg.Obf.Mode {
		logger.Infof("OBF mode enabled: listener only accepts clients with matching -obf-key")
		wrapListener, werr := srtpmimicry.Listen(addr, cfg.Obf.Key)
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
		if err := listener.Close(); err != nil {
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
			if ctx.Err() != nil {
				wg.Wait()
				return
			}
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
