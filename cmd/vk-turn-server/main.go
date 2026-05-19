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
		log.Panicf("%v", err)
	}
	logger := logx.New(cfg.Log.Debug)
	registry := bondserver.NewRegistry(bondserver.Deps{Log: logger})

	if cfg.Obf.GenWrapKey {
		key, gerr := srtpmimicry.GenKeyHex()
		if gerr != nil {
			log.Panicf("gen-wrap-key: %v", gerr)
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
		log.Printf("Terminating...\n")
		cancel()
		<-signalChan
		log.Fatalf("Exit...\n")
	}()

	addr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Listen)
	if err != nil {
		panic(err)
	}
	log.Printf("Starting server listen=%s connect=%s vless=%t wrap=%t bond-autodetect=true",
		cfg.Proxy.Listen, cfg.Proxy.Connect, cfg.Proxy.Mode == config.ProxyModeTCPFwd, cfg.Obf.WrapMode)

	certificate, genErr := selfsign.GenerateSelfSigned()
	if genErr != nil {
		panic(genErr)
	}

	dtlsOpts := []dtls.ServerOption{
		dtls.WithCertificates(certificate),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
	var listener net.Listener
	if cfg.Obf.WrapMode {
		log.Printf("WRAP mode enabled: listener only accepts clients with matching -wrap-key")
		wrapListener, werr := srtpmimicry.Listen(addr, cfg.Obf.WrapKey)
		if werr != nil {
			panic(werr)
		}
		listener, err = dtls.NewListenerWithOptions(wrapListener, dtlsOpts...)
	} else {
		listener, err = dtls.ListenWithOptions("udp", addr, dtlsOpts...)
	}
	if err != nil {
		panic(err)
	}
	context.AfterFunc(ctx, func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	})

	fmt.Println("Listening")

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
			log.Println(err)
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
			log.Printf("failed to close incoming connection: %s", closeErr)
		}
	}()
	logger.Debugf("Connection from %s\n", conn.RemoteAddr())

	ctx1, cancel1 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel1()

	dtlsConn, ok := conn.(*dtls.Conn)
	if !ok {
		log.Println("Type error: expected *dtls.Conn")
		return
	}
	logger.Debugf("Start handshake")
	if err := dtlsConn.HandshakeContext(ctx1); err != nil {
		log.Printf("Handshake failed: %v", err)
		return
	}
	logger.Debugf("Handshake done")

	if cfg.Proxy.Mode == config.ProxyModeTCPFwd {
		tcpfwdserver.Handle(ctx, logger, registry, dtlsConn, cfg.Proxy.Connect)
	} else {
		udpserver.Handle(ctx, logger, conn, cfg.Proxy.Connect)
	}

	logger.Debugf("Connection closed: %s\n", conn.RemoteAddr())
}
