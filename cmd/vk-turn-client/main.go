// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	bondclient "github.com/cacggghp/vk-turn-proxy/internal/bond/client"
	"github.com/cacggghp/vk-turn-proxy/internal/client/captcha"
	manualcaptcha "github.com/cacggghp/vk-turn-proxy/internal/client/captcha/manual"
	"github.com/cacggghp/vk-turn-proxy/internal/client/dnsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/client/vkauth"
	"github.com/cacggghp/vk-turn-proxy/internal/config"
	"github.com/cacggghp/vk-turn-proxy/internal/dtlsdial"
	udpproxy "github.com/cacggghp/vk-turn-proxy/internal/proxy/udp"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/vless"
	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
)

type getCredsFunc func(ctx context.Context, link string, streamID int) (string, string, string, error)

// Global state trackers
var (
	activeLocalPeer  atomic.Value
	connectedStreams atomic.Int32
	udpDtlsDialer    = &dtlsdial.Dialer{
		HandshakeTimeout: 20 * time.Second,
		HandshakeSem:     make(chan struct{}, 3),
	}
	vlessDtlsDialer = &dtlsdial.Dialer{HandshakeTimeout: 30 * time.Second}
	isDebug         bool
)

var appDialer net.Dialer

// vkAuth is the lazily-initialized VK auth client. Set once in main().
var vkAuth *vkauth.Client

func debugf(format string, v ...any) {
	if isDebug {
		log.Printf(format, v...)
	}
}

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

type turnParams struct {
	host     string
	port     string
	link     string
	udp      bool
	wrapKey  []byte
	getCreds getCredsFunc
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signalChan
		log.Printf("Terminating...\n")
		cancel()
		select {
		case <-signalChan:
		case <-time.After(5 * time.Second):
		}
		log.Fatalf("Exit...\n")
	}()

	cfg, err := config.ParseClient(os.Args[1:], os.Stderr)
	if err != nil {
		log.Panicf("%v", err)
	}

	if cfg.DNSServers != nil {
		dnsdial.SetUDPDNSServers(cfg.DNSServers)
		log.Printf("[DNS] using custom UDP servers: %v", cfg.DNSServers)
	}
	appDialer = dnsdial.AppDialer(cfg.DNSMode)
	dnsdial.InstallGlobalResolver(cfg.DNSMode)
	if cfg.GenWrapKey {
		key, gerr := wrap.GenKeyHex()
		if gerr != nil {
			log.Panicf("%v", gerr)
		}
		fmt.Println(key)
		return
	}
	peer, err := net.ResolveUDPAddr("udp", cfg.Peer)
	if err != nil {
		panic(err)
	}
	if cfg.WrapMode {
		log.Printf("WRAP mode enabled: peer server must use matching -wrap-key")
	}

	isDebug = cfg.Debug
	manualcaptcha.Debug = cfg.Debug

	vkAuth = vkauth.New(vkauth.Config{
		Dialer:          appDialer,
		ManualOnly:      cfg.ManualCaptcha,
		StreamsPerCache: cfg.StreamsPerCred,
		StreamsAlive:    func() int32 { return connectedStreams.Load() },
		ManualSolver:    manualCaptchaSolver,
		Debugf:          debugf,
	})

	getCreds := getCredsFunc(vkAuth.GetCredentials)

	params := &turnParams{
		host:     cfg.Host,
		port:     cfg.Port,
		link:     cfg.VKLink,
		udp:      cfg.UDP,
		wrapKey:  cfg.WrapKey,
		getCreds: getCreds,
	}

	if cfg.VLESSMode {
		bondH := &bondclient.Handler{Deps: bondclient.Deps{Debug: isDebug, Debugf: debugf}}
		vlessDeps := &vless.Deps{
			DTLSDialer:  vlessDtlsDialer,
			Debug:       isDebug,
			Debugf:      debugf,
			BondHandler: bondH.Handle,
		}
		vlessParams := &vless.Params{
			Host:     params.host,
			Port:     params.port,
			Link:     params.link,
			UDP:      params.udp,
			WrapKey:  params.wrapKey,
			GetCreds: vless.GetCredsFunc(params.getCreds),
		}
		vless.Run(ctx, vlessDeps, vlessParams, peer, cfg.Listen, cfg.N, cfg.VLESSBond)
		return
	}

	listenConn, err := net.ListenPacket("udp", cfg.Listen)
	if err != nil {
		log.Panicf("Failed to listen: %s", err)
	}
	context.AfterFunc(ctx, func() {
		if closeErr := listenConn.Close(); closeErr != nil {
			log.Printf("Failed to close local connection: %s", closeErr)
		}
	})

	numStreams := cfg.N
	if numStreams <= 0 {
		numStreams = 1
	}

	// Shared Worker Pool Queue for Aggregation
	inboundChan := make(chan *udpproxy.Packet, 2000)

	go func() {
		// Pointer-cache for the last seen local peer addr. Avoids the
		// per-packet addr.String() allocation pair on the hot WG ingest path:
		// most packets come from the same UDPAddr instance, so a pointer
		// equality check covers the fast path. The slow path (new instance
		// from ReadFrom for the same ip:port) does one String compare and
		// then refreshes the cache.
		var lastAddr net.Addr
		var lastAddrStr string
		for {
			pktIface := udpproxy.Pool.Get()
			pkt, ok := pktIface.(*udpproxy.Packet)
			if !ok {
				log.Printf("packetPool returned unexpected type: %T", pktIface)
				continue
			}
			nRead, addr, err := listenConn.ReadFrom(pkt.Data)
			if err != nil {
				return
			}

			if addr != lastAddr {
				s := addr.String()
				if s != lastAddrStr {
					activeLocalPeer.Store(addr)
					lastAddrStr = s
				}
				lastAddr = addr
			}

			pkt.N = nRead

			select {
			case inboundChan <- pkt:
			default:
				// Drop the packet only if the global queue is completely full
				udpproxy.Pool.Put(pkt)
			}
		}
	}()

	wg1 := sync.WaitGroup{}
	t := time.Tick(200 * time.Millisecond)

	udpDeps := &udpproxy.Deps{
		DTLSDialer:       udpDtlsDialer,
		Auth:             vkAuth,
		Debug:            isDebug,
		Debugf:           debugf,
		ActiveLocalPeer:  &activeLocalPeer,
		ConnectedStreams: &connectedStreams,
		AppCancel:        cancel,
	}
	udpParams := &udpproxy.Params{
		Host:     params.host,
		Port:     params.port,
		Link:     params.link,
		UDP:      params.udp,
		WrapKey:  params.wrapKey,
		GetCreds: udpproxy.GetCredsFunc(params.getCreds),
	}

	okchan := make(chan struct{})
	connchan := make(chan net.PacketConn)
	wg1.Go(func() {
		udpproxy.DTLSLoop(ctx, udpDeps, peer, listenConn, inboundChan, connchan, okchan, 1)
	})
	wg1.Go(func() {
		udpproxy.TURNLoop(ctx, udpDeps, udpParams, peer, connchan, t, 1)
	})

	select {
	case <-okchan:
	case <-ctx.Done():
	}

	for i := 1; i < numStreams; i++ {
		cchan := make(chan net.PacketConn)
		streamID := i
		wg1.Go(func() {
			udpproxy.DTLSLoop(ctx, udpDeps, peer, listenConn, inboundChan, cchan, nil, streamID)
		})
		wg1.Go(func() {
			udpproxy.TURNLoop(ctx, udpDeps, udpParams, peer, cchan, t, streamID)
		})
	}

	wg1.Wait()
}
