package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bond"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpserver"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udpserver"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/shutdown"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "clients" {
		handleClientsCommand(os.Args[2:])
		return
	}

	cfg, err := config.ParseServer(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatalf("%v", err)
	}
	logger := logx.New(cfg.Log.Debug)
	logger.Infof("Free Turn Proxy server version=%s", version)

	if cfg.Obf.GenKey {
		key, gerr := rtpopus.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}

	ctx, stop := shutdown.Watch(context.Background(), logger)
	defer stop()

	addr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Listen)
	if err != nil {
		logger.Errorf("resolve listen addr: %v", err)
		os.Exit(1)
	}
	logger.Infof("Starting server listen=%s connect=%s obf-profile=%s",
		cfg.Proxy.Listen, cfg.Proxy.Connect, cfg.Obf.Profile)
	if !cfg.Obf.Enabled() {
		logger.Warnf("running with -obf-profile=none: any client reaching %s can relay to %s (no shared-key auth)", cfg.Proxy.Listen, cfg.Proxy.Connect)
	}

	listener, err := listen(cfg, addr, logger)
	if err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}
	context.AfterFunc(ctx, func() {
		if err := listener.Close(); err != nil {
			logger.Errorf("listener close: %v", err)
		}
	})

	logger.Infof("Listening on %s", cfg.Proxy.Listen)

	var db *clientsdb.DB
	if cfg.ClientsFile != "" {
		d, err := clientsdb.New(cfg.ClientsFile)
		if err != nil {
			logger.Errorf("Failed to open clients-file: %v", err)
			os.Exit(1)
		}
		d.StartHotReload(ctx, 10*time.Second)
		db = d
		logger.Infof("Client ID authorization enabled via %s", cfg.ClientsFile)
	}

	serve(ctx, logger, listener, db, cfg)
}

func listen(cfg *config.Server, addr *net.UDPAddr, logger logx.Logger) (net.Listener, error) {
	certificate, err := dtlsdial.GenerateSelfSignedCert()
	if err != nil {
		return nil, fmt.Errorf("self-signed cert: %w", err)
	}
	dtlsOpts := []dtls.ServerOption{
		dtls.WithCertificates(certificate),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
	var listener net.Listener
	if cfg.Obf.Enabled() {
		logger.Infof("OBF profile=%s: listener only accepts clients with matching -obf-profile and -obf-key", cfg.Obf.Profile)
		obfListener, oerr := wire.Listen(string(cfg.Obf.Profile), addr, cfg.Obf.Key, cfg.Obf.Timing)
		if oerr != nil {
			return nil, fmt.Errorf("obf listen: %w", oerr)
		}
		listener, err = dtls.NewListenerWithOptions(obfListener, dtlsOpts...)
	} else {
		listener, err = dtls.ListenWithOptions("udp", addr, dtlsOpts...)
	}
	if err != nil {
		return nil, fmt.Errorf("dtls listen: %w", err)
	}
	return listener, nil
}

func serve(ctx context.Context, logger logx.Logger, listener net.Listener, db *clientsdb.DB, cfg *config.Server) {
	bonds := &bond.Server{}
	var wg sync.WaitGroup
	var backoff time.Duration
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
			// Отказ обычно устойчив (EMFILE): без паузы цикл сжёг бы ядро на ретраях.
			backoff = nextAcceptBackoff(backoff)
			logger.Warnf("accept: %v, retry in %s", err, backoff)
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0
		wg.Go(func() {
			_ = safego.Run(logger, func() { handleAccepted(ctx, logger, db, conn, cfg, bonds) })
		})
	}
}

const (
	minAcceptBackoff = 5 * time.Millisecond
	maxAcceptBackoff = time.Second
)

func nextAcceptBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return minAcceptBackoff
	}
	if d *= 2; d > maxAcceptBackoff {
		return maxAcceptBackoff
	}
	return d
}

func wireMode(m config.ProxyMode) byte {
	if m == config.ProxyModeTCP {
		return clientsdb.ModeTCP
	}
	return clientsdb.ModeUDP
}

func modeName(b byte) string {
	if b == clientsdb.ModeTCPBond {
		return "tcp-bond"
	}
	if b == clientsdb.ModeTCP {
		return string(config.ProxyModeTCP)
	}
	return string(config.ProxyModeUDP)
}

func handleAccepted(ctx context.Context, logger logx.Logger, db *clientsdb.DB, conn net.Conn, cfg *config.Server, bonds *bond.Server) {
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
		// Адрес обязателен: пачка таймаутов с новых адресов - признак того, что клиент
		// сменил relayed-адрес, а его сессия сюда не смигрировала.
		logger.Warnf("Handshake failed from %s: %v", conn.RemoteAddr(), err)
		return
	}
	logger.Debugf("Handshake done")

	var clientMode byte
	clientID, data, err := clientsdb.AcceptClientID(dtlsConn, func(id string, mode byte) error {
		clientMode = mode
		return admitClient(cfg, db, id, mode)
	})
	if err != nil {
		logger.Warnf("Client ID from %s rejected: %v", conn.RemoteAddr(), err)
		return
	}

	logger.Infof("Session up: client=%s from=%s", clientID, conn.RemoteAddr())
	switch {
	case clientMode == clientsdb.ModeTCPBond:
		tcpserver.HandleBond(ctx, logger, data, cfg.Proxy.Connect, cfg.KCP.Profile, bonds, clientID)
	case cfg.Proxy.Mode == config.ProxyModeTCP:
		tcpserver.Handle(ctx, logger, data, cfg.Proxy.Connect, cfg.KCP.Profile)
	default:
		udpserver.Handle(ctx, logger, data, cfg.Proxy.Connect)
	}
	logger.Infof("Session down: client=%s from=%s", clientID, conn.RemoteAddr())
}

// admitClient: режим клиента обязан совпасть с сервером, ID - быть в allowlist (если он есть).
func admitClient(cfg *config.Server, db *clientsdb.DB, id string, mode byte) error {
	if mode == clientsdb.ModeTCPBond {
		mode = clientsdb.ModeTCP
	}
	if want := wireMode(cfg.Proxy.Mode); mode != clientsdb.ModeUnset && mode != want {
		return fmt.Errorf("mode mismatch: клиент %s, сервер %s - приведите -mode к одному значению",
			modeName(mode), cfg.Proxy.Mode)
	}
	if mode == clientsdb.ModeUnset && cfg.Proxy.Mode == config.ProxyModeTCP {
		return errors.New("mode mismatch: клиент без тега режима (udp), сервер tcp")
	}
	if db != nil && !db.IsAuthorized(id) {
		return fmt.Errorf("unauthorized client ID %s", id)
	}
	return nil
}

func handleClientsCommand(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: server clients <add|remove|list> [args...]")
		os.Exit(1)
	}

	dbPath := "clients.json"
	if envPath := os.Getenv("CLIENTS_FILE"); envPath != "" {
		dbPath = envPath
	}

	db, err := clientsdb.New(dbPath)
	if err != nil {
		fmt.Printf("Failed to open %s: %v\n", dbPath, err)
		os.Exit(1)
	}

	cmd := args[0]
	switch cmd {
	case "add":
		if len(args) < 2 {
			fmt.Println("Usage: server clients add <client_id> [comment]")
			os.Exit(1)
		}
		clientID := args[1]
		comment := ""
		if len(args) > 2 {
			comment = args[2]
		}
		if err := db.Add(clientID, comment); err != nil {
			fmt.Printf("Failed to add client: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Client %s added successfully to %s\n", clientID, dbPath)
	case "remove":
		if len(args) < 2 {
			fmt.Println("Usage: server clients remove <client_id>")
			os.Exit(1)
		}
		clientID := args[1]
		if err := db.Remove(clientID); err != nil {
			fmt.Printf("Failed to remove client: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Client %s removed successfully from %s\n", clientID, dbPath)
	case "list":
		clients := db.List()
		fmt.Printf("Found %d clients in %s:\n", len(clients), dbPath)
		for id, info := range clients {
			fmt.Printf(" - %s (Comment: %s)\n", id, info.Comment)
		}
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
