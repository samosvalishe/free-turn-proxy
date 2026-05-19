package tcpfwd

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/client/ish"
	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/common"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/transport/dtlsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/transport/kcptun"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
	"github.com/xtaci/smux"
)

// GetCredsFunc is an alias of common.GetCredsFunc kept for cmd/main wiring
// stability.
type GetCredsFunc = common.GetCredsFunc

// Params is the per-pool TURN/wrap configuration.
type Params struct {
	Host     string
	Port     string
	Link     string
	UDP      bool
	WrapKey  []byte
	GetCreds GetCredsFunc
}

// BondHandler stripes one accepted TCP connection across all currently-live
// pool sessions. Nil disables bond mode (callers will then use round-robin).
// Bond client lives in main during stage 4.2; it will move to internal/bond/client
// in stage 5.1.
type BondHandler func(ctx context.Context, tcpConn net.Conn, connID uint64, lanes []*PooledSession)

// Deps groups host-process dependencies needed by the VLESS loop.
type Deps struct {
	DTLSDialer  *dtlsdial.Dialer
	Log         logx.Logger
	BondHandler BondHandler
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Run is the VLESS-mode entrypoint. It spawns numSessions maintainers, waits
// for at least one to connect, then accepts local TCP connections and forwards
// each as a smux stream (round-robin) or bonded across all live sessions.
func Run(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenAddr string, numSessions int, useBond bool) {
	pool := &SessionPool{}

	var wgMaint sync.WaitGroup
	for id := range numSessions {
		wgMaint.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(id) * 300 * time.Millisecond):
			}
			maintainSession(ctx, deps, params, peer, id, pool)
		})
	}

	log.Printf("VLESS mode: waiting for sessions to connect (total: %d)...", numSessions)
	for {
		select {
		case <-ctx.Done():
			wgMaint.Wait()
			return
		case <-time.After(100 * time.Millisecond):
		}
		if pool.Count() > 0 {
			break
		}
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Panicf("TCP listen: %s", err)
	}

	wrappedListener, err := ish.WrapListener(listener)
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
			if deps.BondHandler == nil {
				log.Printf("bond requested but no BondHandler set, rejecting")
				_ = tcpConn.Close()
				continue
			}
			connID := (uint64(time.Now().UnixNano()) << 16) ^ pool.NextConnID()
			lanes := pool.Snapshot()
			if len(lanes) == 0 {
				log.Printf("No active sessions, rejecting connection")
				_ = tcpConn.Close()
				continue
			}

			tc, cid, lns := tcpConn, connID, lanes
			wgConn.Go(func() {
				deps.BondHandler(ctx, tc, cid, lns)
			})
			continue
		}

		ps := pool.Pick()
		if ps == nil || ps.Sess.IsClosed() {
			log.Printf("No active sessions, rejecting connection")
			_ = tcpConn.Close()
			continue
		}

		connID := pool.NextConnID()
		opened := ps.Opened.Add(1)
		active := ps.Active.Add(1)
		deps.log().Debugf("[session %d] TCP accept #%d from=%s active=%d opened=%d pool=%d",
			ps.ID, connID, tcpConn.RemoteAddr(), active, opened, pool.Count())

		tc, sessRef, cid := tcpConn, ps, connID
		wgConn.Go(func() {
			defer func() { _ = tc.Close() }()
			defer func() {
				active := sessRef.Active.Add(-1)
				closed := sessRef.Closed.Add(1)
				deps.log().Debugf("[session %d] TCP close #%d active=%d closed=%d totals: to-session=%s from-session=%s",
					sessRef.ID, cid, active, closed,
					stats.FormatByteCount(sessRef.ToSession.Load()), stats.FormatByteCount(sessRef.FromSession.Load()))
			}()

			stream, err := sessRef.Sess.OpenStream()
			if err != nil {
				log.Printf("[session %d] smux open stream error for TCP #%d: %s", sessRef.ID, cid, err)
				return
			}
			defer func() { _ = stream.Close() }()
			fromSession, toSession := pipe(deps, ctx, tc, stream)
			sessRef.FromSession.Add(uint64(fromSession))
			sessRef.ToSession.Add(uint64(toSession))
			deps.log().Debugf("[session %d] TCP done #%d local<-session=%s local->session=%s",
				sessRef.ID, cid, stats.FormatByteCount(uint64(fromSession)), stats.FormatByteCount(uint64(toSession)))
		})
	}
}

// maintainSession keeps one TURN+DTLS+KCP+smux session alive, reconnecting on
// failure with a fixed 2-3s backoff.
func maintainSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int, pool *SessionPool) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		smuxSess, cleanup, err := createSmuxSession(ctx, deps, params, peer, id)
		if err != nil {
			log.Printf("[session %d] setup error: %s, retrying...", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		ps := pool.Add(id, smuxSess)
		log.Printf("[session %d] connected (active: %d)", id, pool.Count())

		for !smuxSess.IsClosed() {
			select {
			case <-ctx.Done():
				pool.Remove(ps)
				cleanup()
				return
			case <-time.After(1 * time.Second):
			}
		}

		pool.Remove(ps)
		cleanup()
		log.Printf("[session %d] disconnected (active: %d), reconnecting...", id, pool.Count())

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// createSmuxSession establishes a full TURN+DTLS+KCP+smux pipeline and returns
// the smux session along with a cleanup function (LIFO teardown).
func createSmuxSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int) (*smux.Session, func(), error) {
	var cleanupFns []func()
	cleanup := func() {
		for i := len(cleanupFns) - 1; i >= 0; i-- {
			cleanupFns[i]()
		}
	}

	stream, err := common.DialTURN(ctx, params.Host, params.Port, params.UDP, peer, params.Link, id, params.GetCreds)
	if err != nil {
		return nil, nil, err
	}
	cleanupFns = append(cleanupFns, func() { _ = stream.Close() })
	relayConn := stream.Relay
	deps.log().Debugf("[session %d] TURN server IP: %s", id, stream.ServerUDPAddr.IP)
	deps.log().Debugf("relayed-address=%s", relayConn.LocalAddr().String())

	relayWC, err := common.NewClientWrap(params.WrapKey)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("wrap init: %w", err)
	}
	dtlsPC := &srtpmimicry.RelayPacketConn{Relay: relayConn, Peer: peer, Conn: relayWC}
	dtlsConn, err := deps.DTLSDialer.Dial(ctx, dtlsPC, peer)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("DTLS handshake: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = dtlsConn.Close() })
	deps.log().Debugf("DTLS connection established")

	statsCtx, statsCancel := context.WithCancel(ctx)
	cleanupFns = append(cleanupFns, statsCancel)
	st := stats.New(deps.log().DebugEnabled())
	go st.LogEvery(statsCtx, deps.log().Debugf, fmt.Sprintf("[session %d] VLESS", id), "to-turn", "from-turn")

	kcpSess, err := kcptun.NewKCPOverDTLS(&stats.CountingConn{Conn: dtlsConn, Stats: st}, false)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("KCP session: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = kcpSess.Close() })
	deps.log().Debugf("KCP session established")

	smuxSess, err := smux.Client(kcpSess, kcptun.DefaultSmuxConfig())
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("smux client: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = smuxSess.Close() })
	deps.log().Debugf("smux session established")

	return smuxSess, cleanup, nil
}

// pipe copies data bidirectionally between two connections, cancelling both
// sides as soon as either copy finishes. Returns (c1<-c2, c2<-c1) bytes.
func pipe(deps *Deps, ctx context.Context, c1, c2 net.Conn) (int64, int64) {
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
		if err != nil && deps.log().DebugEnabled() {
			log.Printf("pipe: c1<-c2 copy error: %v", err)
		}
	})
	wg.Go(func() {
		defer cancel()
		n, err := io.Copy(c2, c1)
		c2FromC1 = n
		if err != nil && deps.log().DebugEnabled() {
			log.Printf("pipe: c2<-c1 copy error: %v", err)
		}
	})
	wg.Wait()
	if err := c1.SetDeadline(time.Time{}); err != nil && deps.log().DebugEnabled() {
		log.Printf("pipe: failed to reset deadline c1: %v", err)
	}
	if err := c2.SetDeadline(time.Time{}); err != nil && deps.log().DebugEnabled() {
		log.Printf("pipe: failed to reset deadline c2: %v", err)
	}
	return c1FromC2, c2FromC1
}
