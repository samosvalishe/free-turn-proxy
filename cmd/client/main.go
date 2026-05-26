package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/samosvalishe/free-turn-proxy/internal/clientrun"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/sub"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.SubURL != "" {
		s, ferr := sub.Fetch(ctx, cfg.SubURL)
		if ferr != nil {
			log.Fatalf("failed to fetch subscription: %v", ferr)
		}
		if len(s.Nodes) == 0 {
			log.Fatalf("no nodes found in subscription")
		}

		// Берем первый сервер из подписки
		node := s.Nodes[0]
		ucfg := node.URI
		if ucfg.Provider != "" {
			cfg.Provider.Name = ucfg.Provider
		}
		if ucfg.Transport != "" {
			cfg.TURN.TransportUDP = ucfg.Transport == "udp"
		}
		if ucfg.Mode != "" {
			cfg.Proxy.Mode = config.ClientProxyMode(ucfg.Mode, ucfg.Bond)
		}
		if ucfg.ObfProfile != "" {
			cfg.Obf.Profile = config.ObfProfile(ucfg.ObfProfile)
		}
		if ucfg.ObfKey != "" {
			if k, derr := hex.DecodeString(ucfg.ObfKey); derr == nil {
				cfg.Obf.Key = k
			} else {
				log.Fatalf("invalid hex in obf-key: %v", derr)
			}
		}
		if ucfg.Peer != "" {
			cfg.Proxy.Peer = ucfg.Peer
		}
	}

	cfg.ClientID = resolveClientID(cfg.ClientID)

	logger := logx.New(cfg.Log.Debug)
	logger.Infof("Free Turn Proxy client version=%s", version)
	logger.Infof("Client ID: %s", cfg.ClientID)
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

	if cfg.Obf.GenKey {
		key, gerr := rtpopus.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}

	run, err := clientrun.New(cfg, logger, clientrun.Options{})
	if err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}

	if err := run.Serve(ctx); err != nil {
		if errors.Is(err, udprelay.ErrFatal) {
			logger.Errorf("fatal: %v", err)
		} else {
			logger.Errorf("%v", err)
		}
		os.Exit(1)
	}
}

func resolveClientID(cliID string) string {
	if cliID != "" {
		return cliID
	}

	type localCfg struct {
		ClientID string `json:"client_id"`
	}

	path := filepath.Join(filepath.Dir(os.Args[0]), "client_config.json")
	b, err := os.ReadFile(path) //nolint:gosec // фиксированное имя рядом с бинарём, не пользовательский ввод
	if err == nil {
		var lc localCfg
		if err := json.Unmarshal(b, &lc); err == nil && lc.ClientID != "" {
			return lc.ClientID
		}
	}

	// Generate 16 bytes hex ID
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		log.Fatalf("failed to generate random client ID: %v", err)
	}
	newID := hex.EncodeToString(idBytes)

	lc := localCfg{ClientID: newID}
	b, _ = json.MarshalIndent(lc, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // path фиксирован рядом с бинарём; 0o600 для auth-токена
		log.Printf("warning: failed to save client ID to %s: %v", path, err) //nolint:gosec // path не пользовательский ввод
	}

	return newID
}
