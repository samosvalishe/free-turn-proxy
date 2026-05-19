// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cacggghp/vk-turn-proxy/client/internal/captcha"
	"github.com/cacggghp/vk-turn-proxy/client/internal/dnsdial"
	"github.com/cacggghp/vk-turn-proxy/client/internal/vkauth"
	"github.com/cacggghp/vk-turn-proxy/internal/bond"
	"github.com/cacggghp/vk-turn-proxy/internal/dtlsdial"
	udpproxy "github.com/cacggghp/vk-turn-proxy/internal/proxy/udp"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/vless"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
	"github.com/xtaci/smux"
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
		vlessDeps := &vless.Deps{
			DTLSDialer:  vlessDtlsDialer,
			Debug:       isDebug,
			Debugf:      debugf,
			BondHandler: handleBondedTCP,
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

// bondClientLane is one striped smux stream within a bonded TCP connection.
// Stays in main during stage 4.2; moves to internal/bond/client in stage 5.1.
type bondClientLane struct {
	ps     *vless.PooledSession
	stream *smux.Stream
	mu     sync.Mutex
	dead   atomic.Bool
}

func handleBondedTCP(ctx context.Context, tcpConn net.Conn, connID uint64, candidates []*vless.PooledSession) {
	defer func() { _ = tcpConn.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lanes := make([]*bondClientLane, 0, len(candidates))
	laneIDs := make([]string, 0, len(candidates))
	for i, ps := range candidates {
		if ps.Sess.IsClosed() {
			continue
		}
		stream, err := ps.Sess.OpenStream()
		if err != nil {
			log.Printf("[bond %d] session %d open stream error: %s", connID, ps.ID, err)
			continue
		}
		if err := bond.WriteHello(stream, connID, uint16(i), uint16(len(candidates))); err != nil {
			log.Printf("[bond %d] session %d hello error: %s", connID, ps.ID, err)
			_ = stream.Close()
			continue
		}
		ps.Opened.Add(1)
		ps.Active.Add(1)
		lanes = append(lanes, &bondClientLane{ps: ps, stream: stream})
		laneIDs = append(laneIDs, strconv.Itoa(ps.ID))
	}

	if len(lanes) == 0 {
		log.Printf("[bond %d] no usable lanes, rejecting TCP from %s", connID, tcpConn.RemoteAddr())
		return
	}
	context.AfterFunc(ctx, func() {
		now := time.Now()
		if err := tcpConn.SetDeadline(now); err != nil && isDebug {
			log.Printf("[bond %d] local TCP deadline error: %v", connID, err)
		}
		for _, lane := range lanes {
			if err := lane.stream.SetDeadline(now); err != nil && isDebug {
				log.Printf("[bond %d] session %d stream deadline error: %v", connID, lane.ps.ID, err)
			}
		}
	})

	debugf("[bond %d] TCP accept from=%s lanes=%d [%s]", connID, tcpConn.RemoteAddr(), len(lanes), strings.Join(laneIDs, ","))
	defer func() {
		for _, lane := range lanes {
			_ = lane.stream.Close()
			active := lane.ps.Active.Add(-1)
			closed := lane.ps.Closed.Add(1)
			debugf("[bond %d] lane session %d close active=%d closed=%d totals: to-session=%s from-session=%s",
				connID, lane.ps.ID, active, closed,
				stats.FormatByteCount(lane.ps.ToSession.Load()), stats.FormatByteCount(lane.ps.FromSession.Load()))
		}
	}()

	recvCh := make(chan bond.Frame, 1024)
	var readWG sync.WaitGroup
	for _, lane := range lanes {
		l := lane
		readWG.Go(func() {
			for {
				f, err := bond.ReadFrame(l.stream)
				if err != nil {
					l.dead.Store(true)
					select {
					case <-ctx.Done():
					default:
						if err != io.EOF {
							debugf("[bond %d] session %d read frame error: %v", connID, l.ps.ID, err)
						}
					}
					return
				}
				if f.Type == bond.FrameData {
					l.ps.FromSession.Add(uint64(len(f.Data)))
				}
				select {
				case recvCh <- f:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		readWG.Wait()
		close(recvCh)
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		copyTCPToBond(ctx, connID, tcpConn, lanes)
	})
	wg.Go(func() {
		copyBondToTCP(ctx, connID, tcpConn, recvCh)
		cancel()
	})
	wg.Wait()
}

func copyTCPToBond(ctx context.Context, connID uint64, tcpConn net.Conn, lanes []*bondClientLane) {
	buf := make([]byte, bond.MaxChunk)
	var seq uint64
	var laneIdx uint64
	for {
		n, err := tcpConn.Read(buf)
		if n > 0 {
			lane, writeErr := writeBondFrameToNextLane(ctx, lanes, bond.FrameData, seq, buf[:n], &laneIdx)
			if writeErr != nil {
				log.Printf("[bond %d] write data error: %v", connID, writeErr)
				return
			}
			lane.ps.ToSession.Add(uint64(n))
			seq++
		}
		if err != nil {
			if isDebug && err != io.EOF {
				log.Printf("[bond %d] local TCP read finished with error: %v", connID, err)
			}
			for _, lane := range lanes {
				if lane.dead.Load() {
					continue
				}
				lane.mu.Lock()
				writeErr := bond.WriteFrame(lane.stream, bond.FrameFIN, seq, nil)
				lane.mu.Unlock()
				if writeErr != nil && ctx.Err() == nil {
					log.Printf("[bond %d] session %d write FIN error: %v", connID, lane.ps.ID, writeErr)
				}
			}
			debugf("[bond %d] upload finished chunks=%d", connID, seq)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func writeBondFrameToNextLane(ctx context.Context, lanes []*bondClientLane, typ byte, seq uint64, data []byte, laneIdx *uint64) (*bondClientLane, error) {
	for range lanes {
		idx := *laneIdx % uint64(len(lanes))
		*laneIdx++
		lane := lanes[idx]
		if lane.dead.Load() {
			continue
		}
		lane.mu.Lock()
		err := bond.WriteFrame(lane.stream, typ, seq, data)
		lane.mu.Unlock()
		if err == nil {
			return lane, nil
		}
		lane.dead.Store(true)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("no live bond lanes")
}

func copyBondToTCP(ctx context.Context, connID uint64, tcpConn net.Conn, recvCh <-chan bond.Frame) {
	pending := make(map[uint64][]byte)
	var expect uint64
	var finSeq *uint64

	for {
		if finSeq != nil && expect == *finSeq {
			bond.CloseWrite(tcpConn, debugf)
			debugf("[bond %d] download finished chunks=%d", connID, expect)
			return
		}

		select {
		case <-ctx.Done():
			return
		case f, ok := <-recvCh:
			if !ok {
				return
			}
			switch f.Type {
			case bond.FrameData:
				if len(pending) >= 1024 {
					log.Printf("[bond %d] pending map overflow (>1024), closing", connID)
					return
				}
				pending[f.Seq] = f.Data
			case bond.FrameFIN:
				v := f.Seq
				if finSeq == nil || v < *finSeq {
					finSeq = &v
				}
			default:
				log.Printf("[bond %d] unknown frame type %d", connID, f.Type)
				return
			}

			for {
				data, ok := pending[expect]
				if !ok {
					break
				}
				delete(pending, expect)
				if len(data) > 0 {
					if _, err := tcpConn.Write(data); err != nil {
						log.Printf("[bond %d] local TCP write error: %v", connID, err)
						return
					}
				}
				expect++
			}
		}
	}
}
