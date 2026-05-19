// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cacggghp/vk-turn-proxy/client/internal/captcha"
	"github.com/cacggghp/vk-turn-proxy/client/internal/dnsdial"
	"github.com/cacggghp/vk-turn-proxy/client/internal/vkauth"
	bondclient "github.com/cacggghp/vk-turn-proxy/internal/bond/client"
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
// local captcha bouncer (see manual_captcha.go).
func manualCaptchaSolver(_ context.Context, e *captcha.Error, d net.Dialer) (string, string, error) {
	if e.RedirectURI != "" {
		t, err := solveCaptchaViaProxy(e.RedirectURI, d)
		return t, "", err
	}
	if e.CaptchaImg != "" {
		k, err := solveCaptchaViaHTTP(e.CaptchaImg)
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

	host := flag.String("turn", "", "override TURN server ip")
	port := flag.String("port", "", "override TURN port")
	listen := flag.String("listen", "127.0.0.1:9000", "listen on ip:port")
	vklink := flag.String("vk-link", "", "VK calls invite link \"https://vk.com/call/join/...\"")
	peerAddr := flag.String("peer", "", "peer server address (host:port)")
	n := flag.Int("n", 10, "connections to TURN")
	udp := flag.Bool("udp", false, "connect to TURN with UDP")
	direct := flag.Bool("no-dtls", false, "connect without obfuscation. DO NOT USE")
	vlessMode := flag.Bool("vless", false, "VLESS mode: forward TCP connections (for VLESS) instead of UDP packets")
	vlessBond := flag.Bool("vless-bond", false, "bond one VLESS TCP connection across all active smux sessions")
	wrapMode := flag.Bool("wrap", false, "WRAP mode: ChaCha20-XOR obfuscate DTLS packets before they reach TURN ChannelData")
	wrapKeyHex := flag.String("wrap-key", "", "32-byte hex-encoded shared key for -wrap (64 hex chars)")
	genWrapKey := flag.Bool("gen-wrap-key", false, "print a fresh 64-character hex key for -wrap-key and exit")
	streamsPerCredFlag := flag.Int("streams-per-cred", vkauth.DefaultStreamsPerCache, "number of TURN streams sharing one VK credential cache")
	debugFlag := flag.Bool("debug", false, "enable debug logging")
	manualCaptchaFlag := flag.Bool("manual-captcha", false, "skip auto captcha solving, use manual mode immediately")
	dnsFlag := flag.String("dns", dnsdial.DNSModeAuto, "DNS resolution mode: udp | doh | auto (auto tries UDP/53 first, sticky-fallback to DoH on total failure)")
	dnsServersFlag := flag.String("dns-servers", "", "comma-separated UDP/53 DNS servers to use instead of built-in defaults (e.g. carrier resolvers from Android LinkProperties). Format: ip[:port][,ip[:port]...].")
	flag.Parse()

	switch *dnsFlag {
	case dnsdial.DNSModeUDP, dnsdial.DNSModeDoH, dnsdial.DNSModeAuto:
	default:
		log.Panicf("invalid -dns value %q: must be udp | doh | auto", *dnsFlag)
	}
	if *dnsServersFlag != "" {
		servers := strings.Split(*dnsServersFlag, ",")
		dnsdial.SetUDPDNSServers(servers)
		log.Printf("[DNS] using custom UDP servers: %v", servers)
	}
	appDialer = dnsdial.AppDialer(*dnsFlag)
	dnsdial.InstallGlobalResolver(*dnsFlag)
	if *genWrapKey {
		key, err := wrap.GenKeyHex()
		if err != nil {
			log.Panicf("%v", err)
		}
		fmt.Println(key)
		return
	}
	if *peerAddr == "" {
		log.Panicf("Need peer address!")
	}
	peer, err := net.ResolveUDPAddr("udp", *peerAddr)
	if err != nil {
		panic(err)
	}
	if *vklink == "" {
		log.Panicf("Need vk-link!")
	}
	if *wrapMode && *direct {
		log.Panicf("-wrap requires DTLS; remove -no-dtls")
	}
	wrapKey, err := wrap.DecodeKey(*wrapMode, *wrapKeyHex)
	if err != nil {
		log.Panicf("%v", err)
	}
	if *wrapMode {
		log.Printf("WRAP mode enabled: peer server must use matching -wrap-key")
	}
	if *streamsPerCredFlag <= 0 {
		log.Panicf("-streams-per-cred must be positive")
	}

	isDebug = *debugFlag

	vkAuth = vkauth.New(vkauth.Config{
		Dialer:          appDialer,
		ManualOnly:      *manualCaptchaFlag,
		StreamsPerCache: *streamsPerCredFlag,
		StreamsAlive:    func() int32 { return connectedStreams.Load() },
		ManualSolver:    manualCaptchaSolver,
		Debugf:          debugf,
	})

	parts := strings.Split(*vklink, "join/")
	link := parts[len(parts)-1]

	getCreds := getCredsFunc(vkAuth.GetCredentials)
	if *n <= 0 {
		*n = 10
	}
	if idx := strings.IndexAny(link, "/?#"); idx != -1 {
		link = link[:idx]
	}

	params := &turnParams{
		host:     *host,
		port:     *port,
		link:     link,
		udp:      *udp,
		wrapKey:  wrapKey,
		getCreds: getCreds,
	}

	if *vlessMode {
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
		vless.Run(ctx, vlessDeps, vlessParams, peer, *listen, *n, *vlessBond)
		return
	}

	listenConn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Panicf("Failed to listen: %s", err)
	}
	context.AfterFunc(ctx, func() {
		if closeErr := listenConn.Close(); closeErr != nil {
			log.Printf("Failed to close local connection: %s", closeErr)
		}
	})

	numStreams := *n
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

	if *direct {
		log.Panicf("Direct mode not supported with dispatcher")
	}

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
