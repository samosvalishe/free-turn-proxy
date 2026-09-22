package tcprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/ish"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcpmux"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/shape"
	"github.com/xtaci/smux"
)

const (
	setupRetryDelay      = 3 * time.Second
	setupRetryJitter     = 4 * time.Second
	providerBackoffDelay = 60 * time.Second
	reconnectDelay       = 2 * time.Second
	minAcceptBackoff     = 5 * time.Millisecond
	maxAcceptBackoff     = time.Second
)

// ErrFatal возвращается при фатальных ошибках провайдера, требующих остановки клиента.
var ErrFatal = errors.New("tcprelay: fatal error")

// DialFunc переиспользуется из udprelay: подъём потока один на оба режима.
type DialFunc = udprelay.DialFunc

// AuthHandler переиспользуется из udprelay: жизненный цикл реквизитов один на оба режима.
type AuthHandler = udprelay.AuthHandler

type Params struct {
	Bond         bool
	Dial         DialFunc
	Profile      string
	ObfKey       []byte
	ObfTiming    time.Duration
	KCPProfile   kcpmux.Profile
	ClientID     string
	TrafficStats *stats.Stats
}

type Deps struct {
	DTLSDialer       *dtlsdial.Dialer
	Auth             AuthHandler
	Log              logx.Logger
	ConnectedStreams *atomic.Int32
	OnTURNServer     func(ip net.IP)
	// Recycle рвёт сессии пула; listener переживает рецикл, иначе на каждом
	// пробуждении рвались бы живые соединения VLESS-клиента.
	Recycle <-chan struct{}
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

func (d *Deps) auth() AuthHandler {
	if d.Auth == nil {
		return udprelay.NopAuth{}
	}
	return d.Auth
}

// Run поднимает пул сессий и блокирует вызывающую горутину до отмены ctx.
func Run(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenAddr string, numSessions int) error {
	if numSessions <= 0 {
		numSessions = 1
	}
	log := deps.log()
	pool := newSessionPool(deps.ConnectedStreams)
	pool.bond = params.Bond

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Порт поднимается до первой сессии: иначе Xray получал бы connection refused всё
	// время перебора реквизитов и решения капчи и успевал пометить outbound мёртвым.
	listener, err := listen(runCtx, listenAddr, log)
	if err != nil {
		return err
	}
	stopCloser := context.AfterFunc(runCtx, func() { _ = listener.Close() })
	defer stopCloser()
	log.Infof("TCP mode: listening on %s (sessions=%d bond=%t)", listenAddr, numSessions, params.Bond)

	fatalCh := make(chan error, 1)
	fatal := func(err error) {
		select {
		case fatalCh <- fmt.Errorf("%w: %w", ErrFatal, err):
		default:
		}
	}

	var wgBG sync.WaitGroup
	for i := range numSessions {
		id := i + 1
		wgBG.Go(func() {
			_ = safego.Run(log, func() { maintainSession(runCtx, deps, params, peer, id, pool, fatal) })
		})
	}
	if deps.Recycle != nil {
		wgBG.Go(func() {
			_ = safego.Run(log, func() { watchRecycle(runCtx, log, pool, deps.Recycle) })
		})
	}
	wgBG.Go(func() {
		_ = safego.Run(log, func() { announceReady(runCtx, log, pool, numSessions) })
	})

	var fatalErr atomic.Pointer[error]
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case ferr := <-fatalCh:
			fatalErr.Store(&ferr)
			cancel()
		case <-runCtx.Done():
		}
	}()

	acceptLoop(runCtx, deps, listener, pool)
	cancel()
	wgBG.Wait()
	<-watcherDone
	if p := fatalErr.Load(); p != nil {
		return *p
	}
	return nil
}

func announceReady(ctx context.Context, log logx.Logger, pool *sessionPool, numSessions int) {
	log.Infof("TCP mode: waiting for sessions to connect (total: %d)...", numSessions)
	select {
	case <-ctx.Done():
	case <-pool.Ready():
		log.Infof("TCP mode: pool serving traffic (active: %d)", pool.Count())
	}
}

func listen(ctx context.Context, addr string, log logx.Logger) (net.Listener, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcprelay listen %s: %w", addr, err)
	}
	wrapped, err := ish.WrapListener(ln)
	if err != nil {
		log.Warnf("tcprelay: failed to wrap listener: %v", err)
		return ln, nil
	}
	return wrapped, nil
}

func watchRecycle(ctx context.Context, log logx.Logger, pool *sessionPool, recycle <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-recycle:
			log.Warnf("TCP mode: recycling %d session(s), listener stays up", pool.Count())
			pool.CloseAll()
		}
	}
}

func acceptLoop(ctx context.Context, deps *Deps, listener net.Listener, pool *sessionPool) {
	log := deps.log()
	var wg sync.WaitGroup
	defer wg.Wait()

	var backoff time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Отказ обычно устойчив (EMFILE): без паузы цикл сжёг бы ядро на ретраях.
			backoff = nextBackoff(backoff)
			log.Errorf("TCP accept error: %s, retry in %s", err, backoff)
			if !sleep(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		if pool.bond {
			wg.Go(func() {
				_ = safego.Run(log, func() { proxyBond(ctx, log, conn, pool) })
			})
			continue
		}

		ps := pool.Pick()
		if ps == nil {
			log.Errorf("No active sessions, rejecting connection from %s", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		connID := pool.NextConnID()
		log.Debugf("[session %d] TCP accept #%d from=%s active=%d pool=%d",
			ps.id, connID, conn.RemoteAddr(), ps.active.Add(1), pool.Count())

		wg.Go(func() {
			_ = safego.Run(log, func() { proxyConn(ctx, log, conn, ps, connID) })
		})
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return minAcceptBackoff
	}
	if d *= 2; d > maxAcceptBackoff {
		return maxAcceptBackoff
	}
	return d
}

func proxyConn(ctx context.Context, log logx.Logger, conn net.Conn, ps *pooledSession, connID uint64) {
	defer func() { _ = conn.Close() }()
	defer func() { log.Debugf("[session %d] TCP close #%d active=%d", ps.id, connID, ps.active.Add(-1)) }()

	stream, err := ps.sess.OpenStream()
	if err != nil {
		log.Errorf("[session %d] smux open stream for TCP #%d: %s", ps.id, connID, err)
		return
	}
	defer func() { _ = stream.Close() }()

	// Считаем на потоке smux, а не на проводе: пользователю нужны прикладные байты, без
	// ARQ-оверхеда и ретрансмитов.
	var counted net.Conn = stream
	if ps.traffic != nil {
		counted = &stats.CountingConn{Conn: stream, Stats: ps.traffic}
	}

	// Ошибки копирования шумят на каждом закрытии соединения - только под -debug.
	errf := func(format string, v ...any) {
		if log.DebugEnabled() {
			log.Debugf(format, v...)
		}
	}
	fromSession, toSession := netconn.BiCopy(ctx, conn, counted, errf)
	log.Debugf("[session %d] TCP done #%d local<-session=%s local->session=%s",
		ps.id, connID, stats.FormatByteCount(nonNeg(fromSession)), stats.FormatByteCount(nonNeg(toSession)))
}

// session - живой стек одной сессии; permDead закрывается при смерти TURN channel-bind.
type session struct {
	smux     *smux.Session
	permDead <-chan struct{}
	cleanup  func()
}

func maintainSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int, pool *sessionPool, fatal func(error)) {
	log := deps.log()
	auth := deps.auth()
	for ctx.Err() == nil {
		s, err := createSession(ctx, deps, params, peer, id)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, provider.ErrFatalNoStreams) {
				log.Errorf("[session %d] Fatal provider error. Shutting down application.", id)
				fatal(err)
				return
			}
			if auth.IsAuthError(err) {
				auth.HandleAuthError(id)
			}
			d := retryDelay(auth, err)
			log.Errorf("[session %d] setup error: %s, retry in %s", id, err, d.Truncate(time.Millisecond))
			if !sleep(ctx, d) {
				return
			}
			continue
		}
		auth.ResetErrors(id)

		ps := pool.Add(id, s.smux, params.TrafficStats)
		log.Infof("[session %d] connected (active: %d)", id, pool.Count())

		dead := awaitDead(ctx, log, s, id)
		pool.Remove(ps)
		s.cleanup()
		if !dead {
			return
		}

		log.Infof("[session %d] disconnected (active: %d), reconnecting...", id, pool.Count())
		if !sleep(ctx, reconnectDelay) {
			return
		}
	}
}

// retryDelay: пауза провайдера важнее собственной - ретрай в её середине только продлевает
// локаут и жжёт персону. Джиттер разводит одновременный отказ всех сессий пула.
func retryDelay(auth AuthHandler, err error) time.Duration {
	if errors.Is(err, turndial.ErrAllocQuota) {
		return turndial.QuotaBackoff()
	}
	if errors.Is(err, provider.ErrBackoffActive) {
		if until := auth.BackoffUntilUnix(); until > 0 {
			if d := time.Until(time.Unix(until, 0)); d > 0 {
				return d
			}
			return 5 * time.Second
		}
		return providerBackoffDelay
	}

	return setupRetryDelay + time.Duration(randx.Intn(int(setupRetryJitter/time.Millisecond)))*time.Millisecond
}

// awaitDead: false - вышли по отмене ctx, а не по смерти сессии.
func awaitDead(ctx context.Context, log logx.Logger, s *session, id int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.permDead:
		log.Warnf("[session %d] TURN channel-bind умер - рецикл allocation", id)
		return true
	case <-s.smux.CloseChan():
		return true
	}
}

// createSession поднимает стек TURN -> obf -> DTLS -> KCP -> smux.
func createSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int) (*session, error) {
	log := deps.log()
	var closers []func()
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	stream, err := params.Dial(ctx, id)
	if err != nil {
		return nil, err
	}
	relayedAddr := stream.Relay.LocalAddr().String()
	closers = append(closers, func() {
		// Недошедший deallocate держит квоту VK до конца её lifetime, и следующий
		// Allocate по тем же кредам ловит 486 - такие креды переиспользовать нельзя.
		cerr := stream.Close()
		log.Infof("[session %d] TURN allocation released: relayed=%s deallocate=%v", id, relayedAddr, cerr)
		if cerr != nil {
			deps.auth().DropCredentials(id)
		}
	})
	if deps.OnTURNServer != nil {
		deps.OnTURNServer(stream.ServerUDPAddr.IP)
	}
	log.Debugf("[session %d] relayed-address=%s", id, relayedAddr)

	codec, err := wire.NewClientCodec(params.Profile, params.ObfKey)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("obf init: %w", err)
	}
	relayConn := stream.Relay
	if params.ObfTiming > 0 {
		relayConn = shape.WrapPacketConn(relayConn, params.ObfTiming)
		log.Debugf("[session %d] obf-timing=%s", id, params.ObfTiming)
	}
	dtlsConn, err := deps.DTLSDialer.Dial(ctx, &wire.RelayPacketConn{Relay: relayConn, Peer: peer, Codec: codec}, peer)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("DTLS handshake: %w", err)
	}
	closers = append(closers, func() { _ = dtlsConn.Close() })

	// Wire-контракт: Client ID первой app-record, до KCP.
	mode := clientsdb.ModeTCP
	if params.Bond {
		mode = clientsdb.ModeTCPBond
	}
	if err = clientsdb.WriteClientID(ctx, dtlsConn, params.ClientID, mode); err != nil {
		cleanup()
		return nil, fmt.Errorf("send client ID: %w", err)
	}

	var counted net.Conn = dtlsConn
	if params.TrafficStats != nil {
		counted = &stats.WireConn{Conn: counted, Stats: params.TrafficStats}
	}
	kcpSess, err := kcpmux.Dial(counted, params.KCPProfile)
	if err != nil {
		cleanup()
		return nil, err
	}
	closers = append(closers, func() { _ = kcpSess.Close() })

	smuxSess, err := smux.Client(kcpSess, kcpmux.SmuxConfig())
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("smux client: %w", err)
	}
	closers = append(closers, func() { _ = smuxSess.Close() })
	log.Debugf("[session %d] smux session established", id)

	return &session{smux: smuxSess, permDead: stream.PermDead, cleanup: cleanup}, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nonNeg(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}
