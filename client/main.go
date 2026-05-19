// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
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
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/turnpipe"
	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
	"github.com/cacggghp/vk-turn-proxy/tcputil"
	"github.com/cbeuw/connutil"
	"github.com/xtaci/smux"
)

type getCredsFunc func(ctx context.Context, link string, streamID int) (string, string, string, error)

// Global state trackers
var (
	activeLocalPeer  atomic.Value
	connectedStreams atomic.Int32
	globalAppCancel  context.CancelFunc
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

type UDPPacket struct {
	Data []byte
	N    int
}

var packetPool = sync.Pool{
	New: func() any { return &UDPPacket{Data: make([]byte, 2048)} },
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

func dtlsFunc(ctx context.Context, conn net.PacketConn, peer *net.UDPAddr) (net.Conn, error) {
	return udpDtlsDialer.Dial(ctx, conn, peer)
}

func oneDtlsConnection(ctx context.Context, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *UDPPacket, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) error {
	time.Sleep(time.Duration(rand.Intn(400)+100) * time.Millisecond)

	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()

	conn1, conn2 := connutil.AsyncPacketPipe()
	go func() {
		for {
			select {
			case <-dtlsctx.Done():
				return
			case connchan <- conn2:
			}
		}
	}()
	dtlsConn, err1 := dtlsFunc(dtlsctx, conn1, peer)
	if err1 != nil {
		return fmt.Errorf("failed to connect DTLS: %s", err1)
	}
	defer func() {
		if closeErr := dtlsConn.Close(); closeErr != nil {
			log.Printf("[STREAM %d] failed to close DTLS connection: %s", streamID, closeErr)
		}
		log.Printf("[STREAM %d] Closed DTLS connection\n", streamID)
	}()
	log.Printf("[STREAM %d] Established DTLS connection!\n", streamID)

	if okchan != nil {
		go func() {
			select {
			case okchan <- struct{}{}:
			case <-dtlsctx.Done():
			}
		}()
	}

	wg := sync.WaitGroup{}
	context.AfterFunc(dtlsctx, func() {
		if err := dtlsConn.SetDeadline(time.Now()); err != nil {
			log.Printf("[STREAM %d] Warning: SetDeadline failed: %v", streamID, err)
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		for {
			select {
			case <-dtlsctx.Done():
				return
			case pkt := <-inboundChan:
				_, _ = dtlsConn.Write(pkt.Data[:pkt.N])
				packetPool.Put(pkt)
			}
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		buf := make([]byte, 1600)
		for {
			n, err1 := dtlsConn.Read(buf)
			if err1 != nil {
				return
			}

			// Send back to the active WG client
			if peerAddr := activeLocalPeer.Load(); peerAddr != nil {
				if addr, ok := peerAddr.(net.Addr); ok {
					if _, err := listenConn.WriteTo(buf[:n], addr); err != nil {
						log.Printf("[STREAM %d] failed to forward packet to local peer: %v", streamID, err)
					}
				}
			}
		}
	})

	wg.Wait()
	if err := dtlsConn.SetDeadline(time.Time{}); err != nil {
		log.Printf("[STREAM %d] Failed to clear DTLS deadline: %s", streamID, err)
	}
	return nil
}

type turnParams struct {
	host     string
	port     string
	link     string
	udp      bool
	wrapKey  []byte
	getCreds getCredsFunc
}

func oneTurnConnection(ctx context.Context, turnParams *turnParams, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	time.Sleep(time.Duration(rand.Intn(400)+100) * time.Millisecond)
	var err error
	defer func() { c <- err }()
	user, pass, urlTarget, err1 := turnParams.getCreds(ctx, turnParams.link, streamID)
	if err1 != nil {
		err = fmt.Errorf("failed to get TURN credentials: %s", err1)
		return
	}
	stream, err1 := turnpipe.Open(ctx, turnpipe.Config{
		HostOverride: turnParams.host,
		PortOverride: turnParams.port,
		UDP:          turnParams.udp,
	}, peer, user, pass, urlTarget)
	if err1 != nil {
		if vkauth.IsAuthError(err1) {
			vkAuth.HandleAuthError(streamID)
		}
		err = err1
		return
	}
	relayConn := stream.Relay
	debugf("[STREAM %d] TURN server IP: %s", streamID, stream.ServerUDPAddr.IP)

	// Reset error count on successful allocation
	vkAuth.ResetErrors(streamID)

	// Safely track active streams globally
	connectedStreams.Add(1)
	defer func() {
		connectedStreams.Add(-1)
		if cerr := stream.Close(); cerr != nil {
			err = fmt.Errorf("failed to close TURN stream: %s", cerr)
		}
	}()

	if isDebug {
		log.Printf("[STREAM %d] relayed-address=%s", streamID, relayConn.LocalAddr().String())
	}

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)
	st := stats.New(isDebug)
	go st.LogEvery(turnctx, debugf, fmt.Sprintf("[STREAM %d] TURN", streamID), "to-turn", "from-turn")

	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			log.Printf("Failed to set relay deadline: %s", err)
		}
		// Do not set conn2 deadline (conn2 can sometimes be listenConn if direct mode is used)
	})
	var internalPipeAddr atomic.Value
	var wc *wrap.Conn
	if len(turnParams.wrapKey) == wrap.KeyLen {
		var wcErr error
		wc, wcErr = wrap.NewConn(turnParams.wrapKey, false)
		if wcErr != nil {
			log.Printf("[STREAM %d] WRAP init failed: %v", streamID, wcErr)
			turncancel()
			return
		}
	}

	go func() {
		defer turncancel()
		buf := make([]byte, 1600)
		// Reusable scratch buffer for wrapped wire bytes; sized once per
		// stream so the hot-path TX loop performs zero allocations.
		var wireBuf []byte
		if wc != nil {
			wireBuf = make([]byte, wrap.MaxWire(len(buf)))
		}
		for {
			if turnctx.Err() != nil {
				return
			}
			n, addr1, err1 := conn2.ReadFrom(buf)
			if err1 != nil {
				return
			}
			if turnctx.Err() != nil {
				return
			}

			internalPipeAddr.Store(addr1)

			out := buf[:n]
			if wc != nil {
				written, wrapErr := wc.WrapInto(wireBuf, out)
				if wrapErr != nil {
					log.Printf("[STREAM %d] WRAP failed: %v", streamID, wrapErr)
					return
				}
				out = wireBuf[:written]
			}

			written, err1 := relayConn.WriteTo(out, peer)
			st.AddTx(written)
			if err1 != nil {
				return
			}
		}
	}()

	wg.Go(func() {
		defer turncancel()
		readBufLen := 1600
		if wc != nil {
			readBufLen = wrap.MaxWire(1600)
		}
		buf := make([]byte, readBufLen)
		plain := make([]byte, 1600)
		for {
			n, _, err1 := relayConn.ReadFrom(buf)
			if err1 != nil {
				return
			}
			addr1 := internalPipeAddr.Load()
			if addr1 == nil {
				continue
			}

			if addr, ok := addr1.(net.Addr); ok {
				payload := buf[:n]
				if wc != nil {
					m, wrapErr := wc.Unwrap(payload, plain)
					if wrapErr != nil {
						log.Printf("[STREAM %d] UNWRAP failed: %v (n=%d)", streamID, wrapErr, n)
						continue
					}
					payload = plain[:m]
				}
				st.AddRx(len(payload))
				if _, err := conn2.WriteTo(payload, addr); err != nil {
					return
				}
			}
		}
	})

	wg.Wait()
	if err := relayConn.SetDeadline(time.Time{}); err != nil {
		log.Printf("Failed to clear relay deadline: %s", err)
	}
}

func oneDtlsConnectionLoop(ctx context.Context, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *UDPPacket, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := oneDtlsConnection(ctx, peer, listenConn, inboundChan, connchan, okchan, streamID)
			if err != nil {
				if time.Now().Unix() < vkAuth.LockoutUntilUnix() && strings.Contains(err.Error(), "context deadline exceeded") {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(10+rand.Intn(20)) * time.Second):
				}
			}
		}
	}
}

func oneTurnConnectionLoop(ctx context.Context, turnParams *turnParams, peer *net.UDPAddr, connchan <-chan net.PacketConn, t <-chan time.Time, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case conn2 := <-connchan:
			select {
			case <-t:
			case <-ctx.Done():
				return
			}
			c := make(chan error)
			go oneTurnConnection(ctx, turnParams, peer, conn2, streamID, c)

			if err := <-c; err != nil {
				if strings.Contains(err.Error(), "FATAL_CAPTCHA") {
					log.Printf("[STREAM %d] Fatal manual captcha error. Shutting down application.", streamID)
					if globalAppCancel != nil {
						globalAppCancel()
					}
					return
				}
				if strings.Contains(err.Error(), "CAPTCHA_WAIT_REQUIRED") {
					if !strings.Contains(err.Error(), "global lockout active") {
						log.Printf("[STREAM %d] Backing off for 60 seconds to avoid IP ban...", streamID)
						select {
						case <-ctx.Done():
							return
						case <-time.After(60 * time.Second):
						}
					} else {
						lockoutEnd := vkAuth.LockoutUntilUnix()
						sleepDuration := time.Until(time.Unix(lockoutEnd, 0))
						if sleepDuration < 0 {
							sleepDuration = 5 * time.Second
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(sleepDuration):
						}
					}
				} else {
					log.Printf("[STREAM %d] %s", streamID, err)
					time.Sleep(2 * time.Second)
				}
			}
		}
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	globalAppCancel = cancel
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
		runVLESSMode(ctx, params, peer, *listen, *n, *vlessBond)
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
	inboundChan := make(chan *UDPPacket, 2000)

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
			pktIface := packetPool.Get()
			pkt, ok := pktIface.(*UDPPacket)
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
				packetPool.Put(pkt)
			}
		}
	}()

	wg1 := sync.WaitGroup{}
	t := time.Tick(200 * time.Millisecond)

	if *direct {
		log.Panicf("Direct mode not supported with dispatcher")
	}

	okchan := make(chan struct{})
	connchan := make(chan net.PacketConn)
	wg1.Go(func() {
		oneDtlsConnectionLoop(ctx, peer, listenConn, inboundChan, connchan, okchan, 1)
	})
	wg1.Go(func() {
		oneTurnConnectionLoop(ctx, params, peer, connchan, t, 1)
	})

	select {
	case <-okchan:
	case <-ctx.Done():
	}

	for i := 1; i < numStreams; i++ {
		cchan := make(chan net.PacketConn)
		streamID := i
		wg1.Go(func() {
			oneDtlsConnectionLoop(ctx, peer, listenConn, inboundChan, cchan, nil, streamID)
		})
		wg1.Go(func() {
			oneTurnConnectionLoop(ctx, params, peer, cchan, t, streamID)
		})
	}

	wg1.Wait()
}

// sessionPool manages a pool of smux sessions for round-robin TCP distribution.
type pooledSession struct {
	id          int
	sess        *smux.Session
	active      atomic.Int32
	opened      atomic.Uint64
	closed      atomic.Uint64
	toSession   atomic.Uint64
	fromSession atomic.Uint64
}

type sessionPool struct {
	mu          sync.RWMutex
	sessions    []*pooledSession
	counter     atomic.Uint64
	connCounter atomic.Uint64
}

func (p *sessionPool) add(id int, s *smux.Session) *pooledSession {
	ps := &pooledSession{id: id, sess: s}
	p.mu.Lock()
	p.sessions = append(p.sessions, ps)
	p.mu.Unlock()
	return ps
}

func (p *sessionPool) remove(ps *pooledSession) {
	p.mu.Lock()
	for i, sess := range p.sessions {
		if sess == ps {
			p.sessions = append(p.sessions[:i], p.sessions[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
}

func (p *sessionPool) pick() *pooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.sessions)
	if n == 0 {
		return nil
	}
	idx := (p.counter.Add(1) - 1) % uint64(n)
	return p.sessions[idx]
}

func (p *sessionPool) nextConnID() uint64 {
	return p.connCounter.Add(1)
}

func (p *sessionPool) snapshot() []*pooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*pooledSession, 0, len(p.sessions))
	for _, ps := range p.sessions {
		if !ps.sess.IsClosed() {
			out = append(out, ps)
		}
	}
	return out
}

func (p *sessionPool) count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}

type bondClientLane struct {
	ps     *pooledSession
	stream *smux.Stream
	mu     sync.Mutex
	dead   atomic.Bool
}

func handleBondedTCP(ctx context.Context, tcpConn net.Conn, connID uint64, candidates []*pooledSession) {
	defer func() { _ = tcpConn.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lanes := make([]*bondClientLane, 0, len(candidates))
	laneIDs := make([]string, 0, len(candidates))
	for i, ps := range candidates {
		if ps.sess.IsClosed() {
			continue
		}
		stream, err := ps.sess.OpenStream()
		if err != nil {
			log.Printf("[bond %d] session %d open stream error: %s", connID, ps.id, err)
			continue
		}
		if err := bond.WriteHello(stream, connID, uint16(i), uint16(len(candidates))); err != nil {
			log.Printf("[bond %d] session %d hello error: %s", connID, ps.id, err)
			_ = stream.Close()
			continue
		}
		ps.opened.Add(1)
		ps.active.Add(1)
		lanes = append(lanes, &bondClientLane{ps: ps, stream: stream})
		laneIDs = append(laneIDs, strconv.Itoa(ps.id))
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
				log.Printf("[bond %d] session %d stream deadline error: %v", connID, lane.ps.id, err)
			}
		}
	})

	debugf("[bond %d] TCP accept from=%s lanes=%d [%s]", connID, tcpConn.RemoteAddr(), len(lanes), strings.Join(laneIDs, ","))
	defer func() {
		for _, lane := range lanes {
			_ = lane.stream.Close()
			active := lane.ps.active.Add(-1)
			closed := lane.ps.closed.Add(1)
			debugf("[bond %d] lane session %d close active=%d closed=%d totals: to-session=%s from-session=%s",
				connID, lane.ps.id, active, closed,
				stats.FormatByteCount(lane.ps.toSession.Load()), stats.FormatByteCount(lane.ps.fromSession.Load()))
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
							debugf("[bond %d] session %d read frame error: %v", connID, l.ps.id, err)
						}
					}
					return
				}
				if f.Type == bond.FrameData {
					l.ps.fromSession.Add(uint64(len(f.Data)))
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
			lane.ps.toSession.Add(uint64(n))
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
					log.Printf("[bond %d] session %d write FIN error: %v", connID, lane.ps.id, writeErr)
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

// runVLESSMode implements TCP forwarding with round-robin across N TURN sessions.
func runVLESSMode(ctx context.Context, tp *turnParams, peer *net.UDPAddr, listenAddr string, numSessions int, useBond bool) {
	pool := &sessionPool{}

	// Start N session maintainers with staggered startup
	var wgMaint sync.WaitGroup
	for id := range numSessions {
		wgMaint.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(id) * 300 * time.Millisecond):
			}
			maintainVLESSSession(ctx, tp, peer, id, pool)
		})
	}

	// Wait for at least one session
	log.Printf("VLESS mode: waiting for sessions to connect (total: %d)...", numSessions)
	for {
		select {
		case <-ctx.Done():
			wgMaint.Wait()
			return
		case <-time.After(100 * time.Millisecond):
		}
		if pool.count() > 0 {
			break
		}
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Panicf("TCP listen: %s", err)
	}

	wrappedListener, err := wrapISHListener(listener)
	if err != nil {
		log.Printf("Warning: failed to wrap listener: %v", err)
		wrappedListener = listener
	}

	context.AfterFunc(ctx, func() { _ = wrappedListener.Close() })
	if useBond {
		log.Printf("VLESS bond mode: listening on %s (striping each TCP connection across active sessions)", listenAddr)
	} else {
		log.Printf("VLESS mode: listening on %s (round-robin across %d sessions)", listenAddr, numSessions)
	}

	var wgConn sync.WaitGroup
	for {
		tcpConn, err := wrappedListener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				wgConn.Wait()
				wgMaint.Wait()
				return
			default:
			}
			log.Printf("TCP accept error: %s", err)
			continue
		}

		if useBond {
			connID := (uint64(time.Now().UnixNano()) << 16) ^ pool.nextConnID()
			lanes := pool.snapshot()
			if len(lanes) == 0 {
				log.Printf("No active sessions, rejecting connection")
				_ = tcpConn.Close()
				continue
			}

			tc, cid, lns := tcpConn, connID, lanes
			wgConn.Go(func() {
				handleBondedTCP(ctx, tc, cid, lns)
			})
			continue
		}

		ps := pool.pick()
		if ps == nil || ps.sess.IsClosed() {
			log.Printf("No active sessions, rejecting connection")
			_ = tcpConn.Close()
			continue
		}

		connID := pool.nextConnID()
		opened := ps.opened.Add(1)
		active := ps.active.Add(1)
		debugf("[session %d] TCP accept #%d from=%s active=%d opened=%d pool=%d",
			ps.id, connID, tcpConn.RemoteAddr(), active, opened, pool.count())

		tc, sessRef, cid := tcpConn, ps, connID
		wgConn.Go(func() {
			defer func() { _ = tc.Close() }()
			defer func() {
				active := sessRef.active.Add(-1)
				closed := sessRef.closed.Add(1)
				debugf("[session %d] TCP close #%d active=%d closed=%d totals: to-session=%s from-session=%s",
					sessRef.id, cid, active, closed,
					stats.FormatByteCount(sessRef.toSession.Load()), stats.FormatByteCount(sessRef.fromSession.Load()))
			}()

			stream, err := sessRef.sess.OpenStream()
			if err != nil {
				log.Printf("[session %d] smux open stream error for TCP #%d: %s", sessRef.id, cid, err)
				return
			}
			defer func() { _ = stream.Close() }()
			fromSession, toSession := pipe(ctx, tc, stream)
			sessRef.fromSession.Add(uint64(fromSession))
			sessRef.toSession.Add(uint64(toSession))
			debugf("[session %d] TCP done #%d local<-session=%s local->session=%s",
				sessRef.id, cid, stats.FormatByteCount(uint64(fromSession)), stats.FormatByteCount(uint64(toSession)))
		})
	}
}

// maintainVLESSSession keeps one TURN+DTLS+KCP+smux session alive, reconnecting on failure.
func maintainVLESSSession(ctx context.Context, tp *turnParams, peer *net.UDPAddr, id int, pool *sessionPool) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		smuxSess, cleanup, err := createSmuxSession(ctx, tp, peer, id)
		if err != nil {
			log.Printf("[session %d] setup error: %s, retrying...", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		ps := pool.add(id, smuxSess)
		log.Printf("[session %d] connected (active: %d)", id, pool.count())

		for !smuxSess.IsClosed() {
			select {
			case <-ctx.Done():
				pool.remove(ps)
				cleanup()
				return
			case <-time.After(1 * time.Second):
			}
		}

		pool.remove(ps)
		cleanup()
		log.Printf("[session %d] disconnected (active: %d), reconnecting...", id, pool.count())

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// createSmuxSession establishes a full TURN+DTLS+KCP+smux pipeline and returns
// the smux session along with a cleanup function to tear down all layers.
func createSmuxSession(ctx context.Context, tp *turnParams, peer *net.UDPAddr, id int) (*smux.Session, func(), error) {
	var cleanupFns []func()
	cleanup := func() {
		for i := len(cleanupFns) - 1; i >= 0; i-- {
			cleanupFns[i]()
		}
	}

	// 1. Get TURN credentials
	user, pass, rawURL, err := tp.getCreds(ctx, tp.link, id)
	if err != nil {
		return nil, nil, fmt.Errorf("get TURN creds: %w", err)
	}

	// 2-3. Dial TURN and allocate relay.
	stream, err := turnpipe.Open(ctx, turnpipe.Config{
		HostOverride: tp.host,
		PortOverride: tp.port,
		UDP:          tp.udp,
	}, peer, user, pass, rawURL)
	if err != nil {
		return nil, nil, err
	}
	cleanupFns = append(cleanupFns, func() { _ = stream.Close() })
	relayConn := stream.Relay
	debugf("[session %d] TURN server IP: %s", id, stream.ServerUDPAddr.IP)
	debugf("relayed-address=%s", relayConn.LocalAddr().String())

	// 4. Establish DTLS over TURN relay
	var relayWC *wrap.Conn
	if len(tp.wrapKey) == wrap.KeyLen {
		relayWC, err = wrap.NewConn(tp.wrapKey, false)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("wrap init: %w", err)
		}
	}
	dtlsPC := &wrap.RelayPacketConn{Relay: relayConn, Peer: peer, Conn: relayWC}
	dtlsConn, err := vlessDtlsDialer.Dial(ctx, dtlsPC, peer)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("DTLS handshake: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = dtlsConn.Close() })
	debugf("DTLS connection established")

	// 5. Create KCP session over DTLS
	statsCtx, statsCancel := context.WithCancel(ctx)
	cleanupFns = append(cleanupFns, statsCancel)
	st := stats.New(isDebug)
	go st.LogEvery(statsCtx, debugf, fmt.Sprintf("[session %d] VLESS", id), "to-turn", "from-turn")

	kcpSess, err := tcputil.NewKCPOverDTLS(&stats.CountingConn{Conn: dtlsConn, Stats: st}, false)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("KCP session: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = kcpSess.Close() })
	debugf("KCP session established")

	// 6. Create smux client session over KCP
	smuxSess, err := smux.Client(kcpSess, tcputil.DefaultSmuxConfig())
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("smux client: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = smuxSess.Close() })
	debugf("smux session established")

	return smuxSess, cleanup, nil
}

// pipe copies data bidirectionally between two connections.
// It returns bytes copied as c1<-c2 and c2<-c1.
func pipe(ctx context.Context, c1, c2 net.Conn) (int64, int64) {
	ctx2, cancel := context.WithCancel(ctx)
	context.AfterFunc(ctx2, func() {
		if err := c1.SetDeadline(time.Now()); err != nil {
			log.Printf("pipe: failed to set deadline c1: %v", err)
		}
		if err := c2.SetDeadline(time.Now()); err != nil {
			log.Printf("pipe: failed to set deadline c2: %v", err)
		}
	})

	var wg sync.WaitGroup
	var c1FromC2 int64
	var c2FromC1 int64
	wg.Go(func() {
		defer cancel()
		n, err := io.Copy(c1, c2)
		c1FromC2 = n
		if err != nil {
			if isDebug {
				log.Printf("pipe: c1<-c2 copy error: %v", err)
			}
		}
	})
	wg.Go(func() {
		defer cancel()
		n, err := io.Copy(c2, c1)
		c2FromC1 = n
		if err != nil {
			if isDebug {
				log.Printf("pipe: c2<-c1 copy error: %v", err)
			}
		}
	})
	wg.Wait()
	if err := c1.SetDeadline(time.Time{}); err != nil {
		if isDebug {
			log.Printf("pipe: failed to reset deadline c1: %v", err)
		}
	}
	if err := c2.SetDeadline(time.Time{}); err != nil {
		if isDebug {
			log.Printf("pipe: failed to reset deadline c2: %v", err)
		}
	}
	return c1FromC2, c2FromC1
}
