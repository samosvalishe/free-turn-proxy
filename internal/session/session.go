// Package session управляет жизненным циклом клиентской сессии и релея трафика.
package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/routemgr"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/wake"
)

// Phase - текущая стадия подключения сессии.
type Phase string

const (
	PhaseIdle       Phase = "idle"
	PhaseConnecting Phase = "connecting"
	PhaseConnected  Phase = "connected"
	// PhaseCaptcha - ожидание ручного ввода captcha пользователем.
	PhaseCaptcha Phase = "captcha"
	PhaseError   Phase = "error"
)

var (
	ErrAlreadyRun     = errors.New("session: already run")
	ErrConnectTimeout = errors.New("session: connect timeout: no stream connected within the deadline")
)

const (
	defaultStatusInterval       = 500 * time.Millisecond
	defaultUDPHandshakeTimeout  = 20 * time.Second
	defaultTCPHandshakeTimeout  = 30 * time.Second
	defaultHandshakeConcurrency = 3

	// Порог вдвое больше тика: сон короче порога всё равно пропускаем, а тик почаще
	// стоил бы пробуждений процесса на всё время сессии.
	wakeTick      = 30 * time.Second
	wakeThreshold = 60 * time.Second

	// Окно проверки живости после пробуждения: рецикл - только если за него не пришло
	// ни байта. Больше периода keepalive туннеля (25 c у WireGuard по умолчанию).
	wakeProbeWindow = 30 * time.Second
)

type Options struct {
	ConnectTimeout time.Duration

	StatusInterval       time.Duration
	UDPHandshakeTimeout  time.Duration
	TCPHandshakeTimeout  time.Duration
	HandshakeConcurrency int
	// WakeProbeWindow - окно проверки живости канала после пробуждения.
	WakeProbeWindow time.Duration

	Traffic bool
}

func (o Options) withDefaults() Options {
	if o.StatusInterval <= 0 {
		o.StatusInterval = defaultStatusInterval
	}
	if o.UDPHandshakeTimeout <= 0 {
		o.UDPHandshakeTimeout = defaultUDPHandshakeTimeout
	}
	if o.TCPHandshakeTimeout <= 0 {
		o.TCPHandshakeTimeout = defaultTCPHandshakeTimeout
	}
	if o.HandshakeConcurrency <= 0 {
		o.HandshakeConcurrency = defaultHandshakeConcurrency
	}
	if o.WakeProbeWindow <= 0 {
		o.WakeProbeWindow = wakeProbeWindow
	}
	return o
}

type Deps struct {
	Logger        logx.Logger
	Observer      Observer
	Solver        vk.ManualSolverFunc
	CaptchaActive func() bool
	// LocalPipe подменяет локальный UDP-сокет прямым каналом в памяти.
	LocalPipe net.PacketConn
	Options   Options
}

// Snapshot - моментальный снимок состояния сессии для UI.
type Snapshot struct {
	Phase   Phase
	Streams int
	Total   int
	Err     string
	TxTotal uint64
	RxTotal uint64
	TxRate  int64
	RxRate  int64
}

type statusInfo struct {
	phase   Phase
	streams int
	total   int
	err     string
}

// Session инкапсулирует состояние и управление клиентской сессией.
type Session struct {
	cfg     *config.Client
	deps    Deps
	opts    Options
	total   int
	traffic *traffic

	connected   atomic.Int32
	status      atomic.Pointer[statusInfo]
	started     atomic.Bool
	reconnectCh chan struct{}
	recycleCh   chan struct{}
	wakeCh      chan struct{}
}

func New(cfg *config.Client, deps Deps) (*Session, error) {
	if cfg == nil {
		return nil, errors.New("session: nil config")
	}
	if deps.Logger == nil {
		deps.Logger = logx.Nop()
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("session diagnostic ID: %w", err)
	}
	deps.Logger = logx.WithPrefix(deps.Logger, "[session="+id.String()+"] ")

	total := cfg.TURN.N * max(len(cfg.VK.Links), 1)

	s := &Session{
		cfg:         cfg,
		deps:        deps,
		opts:        deps.Options.withDefaults(),
		total:       total,
		reconnectCh: make(chan struct{}, 1),
		recycleCh:   make(chan struct{}, 1),
		wakeCh:      make(chan struct{}, 1),
	}
	if s.opts.Traffic {
		s.traffic = newTraffic()
	}
	s.status.Store(&statusInfo{phase: PhaseConnecting, total: total})
	return s, nil
}

// Run запускает сессию и блокирует вызывающую горутину до завершения или ошибки.
func (s *Session) Run(ctx context.Context) (err error) {
	if !s.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	startedAt := time.Now()
	s.deps.Logger.Infof("lifecycle start mode=%s streams=%d", s.cfg.Proxy.Mode, s.total)
	defer func() {
		s.deps.Logger.Infof("lifecycle stop elapsed=%s error=%v", time.Since(startedAt).Truncate(time.Millisecond), err)
	}()
	s.publish(&statusInfo{phase: PhaseConnecting, total: s.total}, true)
	defer func() {
		if err != nil {
			s.setStatus(PhaseError, 0, err.Error())
			return
		}
		s.setStatus(PhaseIdle, 0, "")
	}()

	log := s.deps.Logger
	dnsdial.SetLogger(log)

	if len(s.cfg.DNS.Servers) > 0 {
		dnsdial.SetUDPDNSServers(s.cfg.DNS.Servers)
		log.Infof("[DNS] using custom UDP servers: %v", s.cfg.DNS.Servers)
	} else {
		dnsdial.ResetUDPDNSServers()
	}
	appDialer := dnsdial.AppDialer(s.cfg.DNS.Mode)
	dnsdial.InstallGlobalResolver(s.cfg.DNS.Mode)

	prov, err := buildProvider(s.cfg, appDialer, &s.connected, s.deps.Solver, log, s.total)
	if err != nil {
		return fmt.Errorf("provider init: %w", err)
	}
	log.Infof("provider=%s", prov.Name())
	if s.cfg.Obf.Enabled() {
		log.Infof("OBF profile=%s: peer server must use matching -obf-profile and -obf-key", s.cfg.Obf.Profile)
	}

	peer, err := net.ResolveUDPAddr("udp", s.cfg.Proxy.Peer)
	if err != nil {
		return fmt.Errorf("resolve peer addr: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var bg sync.WaitGroup
	var bgErr atomic.Pointer[error]
	guard := func(fn func()) func() {
		return func() {
			if err := safego.Run(log, fn); err != nil {
				bgErr.CompareAndSwap(nil, &err)
				cancel()
			}
		}
	}
	bg.Go(guard(func() {
		wake.Watch(runCtx, wakeTick, wakeThreshold, func(gap time.Duration) {
			log.Warnf("device slept for %s - checking TURN allocations", gap.Truncate(time.Second))
			s.Wake()
		})
	}))
	bg.Go(guard(func() { s.watchWake(runCtx) }))

	var watchdogErr error
	bg.Go(guard(func() {
		watchdogErr = s.watch(runCtx, cancel)
	}))

	relayErr := s.runRelayLoop(runCtx, prov, peer)
	stopped := runCtx.Err() != nil
	cancel()
	bg.Wait()

	if p := bgErr.Load(); p != nil {
		return *p
	}
	if relayErr != nil && !stopped {
		return relayErr
	}
	return watchdogErr
}

func (s *Session) runRelayLoop(ctx context.Context, prov provider.Provider, peer *net.UDPAddr) error {
	return s.relayLoop(ctx, func(ctx context.Context) error { return s.relay(ctx, prov, peer) })
}

func (s *Session) relayLoop(ctx context.Context, attempt func(context.Context) error) error {
	log := s.deps.Logger
	for generation := uint64(1); ; generation++ {
		startedAt := time.Now()
		log.Infof("relay start generation=%d", generation)
		attemptCtx, attemptCancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- safego.Call(log, func() error { return attempt(attemptCtx) }) }()

		var err error
		reason := "finished"
		select {
		case err = <-done:
		case <-s.reconnectCh:
			reason = "reconnect"
			attemptCancel()
			err = <-done
		}
		attemptCancel()
		if ctx.Err() != nil {
			reason = "cancelled"
		}
		log.Infof("relay stop generation=%d reason=%s elapsed=%s error=%v", generation, reason,
			time.Since(startedAt).Truncate(time.Millisecond), err)

		if ctx.Err() != nil {
			return err
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
}

// Wake сообщает о подозрении на сон или смену сети. Рецикл не мгновенный: сначала
// watchWake проверяет, молчит ли канал - пересоздание живых аллокаций стоит похода в VK
// за реквизитами и решения капчи.
func (s *Session) Wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Session) Reconnect() {
	ch := s.reconnectCh
	if s.cfg.Proxy.Mode == config.ProxyModeTCP {
		ch = s.recycleCh
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Session) watchWake(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wakeCh:
		}
		if s.wakeNeedsRecycle(ctx) {
			s.Reconnect()
		}
	}
}

func (s *Session) wakeNeedsRecycle(ctx context.Context) bool {
	log := s.deps.Logger
	// Подключение ещё идёт: рецикл отменил бы перебор реквизитов и решение капчи.
	if s.connected.Load() == 0 {
		log.Warnf("wake: сессия ещё поднимается - рецикл пропущен")
		return false
	}
	// Без счётчиков трафика подтвердить живость нечем.
	if s.traffic == nil {
		return true
	}
	before := s.traffic.stats.LivenessRx()
	select {
	case <-ctx.Done():
		return false
	case <-time.After(s.opts.WakeProbeWindow):
	}
	// Пока ждали, могло прилететь ещё одно пробуждение - оно про тот же сон.
	select {
	case <-s.wakeCh:
	default:
	}
	after := s.traffic.stats.LivenessRx()
	if after > before {
		log.Warnf("wake: канал жив (+%d B за %s) - рецикл не нужен", after-before, s.opts.WakeProbeWindow)
		return false
	}
	log.Warnf("wake: тишина %s - рецикл TURN-аллокаций", s.opts.WakeProbeWindow)
	return true
}

// Snapshot возвращает текущий снимок состояния сессии.
func (s *Session) Snapshot() Snapshot {
	st := s.status.Load()
	if st == nil {
		st = &statusInfo{phase: PhaseIdle}
	}
	snap := Snapshot{Phase: st.phase, Streams: st.streams, Total: st.total, Err: st.err}
	if s.traffic != nil {
		snap.TxTotal, snap.RxTotal = s.traffic.stats.Counters()
		snap.TxRate, snap.RxRate = s.traffic.rates()
	}
	return snap
}

func (s *Session) setStatus(phase Phase, streams int, errMsg string) {
	s.publish(&statusInfo{phase: phase, streams: streams, total: s.total, err: errMsg}, false)
}

func (s *Session) publish(next *statusInfo, force bool) {
	prev := s.status.Swap(next)
	if !force && prev != nil && *prev == *next {
		return
	}
	if s.deps.Observer != nil {
		s.deps.Observer.OnPhase(next.phase, next.streams, next.total, next.err)
	}
}

func (s *Session) captchaActive() bool {
	return s.deps.CaptchaActive != nil && s.deps.CaptchaActive()
}

// watch публикует стадию подключения и следит за ConnectTimeout.
func (s *Session) watch(ctx context.Context, cancel context.CancelFunc) error {
	tick := time.NewTicker(s.opts.StatusInterval)
	defer tick.Stop()

	deadline := time.Now().Add(s.opts.ConnectTimeout)
	everConnected := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			n := int(s.connected.Load())
			if n > 0 {
				everConnected = true
			}

			if s.captchaActive() {
				deadline = time.Now().Add(s.opts.ConnectTimeout)
				s.setStatus(PhaseCaptcha, n, "")
				continue
			}

			phase := PhaseConnecting
			if n > 0 {
				phase = PhaseConnected
			}
			s.setStatus(phase, n, "")

			if s.opts.ConnectTimeout > 0 && !everConnected && time.Now().After(deadline) {
				cancel()
				return fmt.Errorf("%w (timeout=%s)", ErrConnectTimeout, s.opts.ConnectTimeout)
			}
		}
	}
}

func (s *Session) relay(ctx context.Context, prov provider.Provider, peer *net.UDPAddr) error {
	log := s.deps.Logger
	getCreds := udprelay.GetCredsFunc(func(ctx context.Context, streamID int) (string, string, []string, error) {
		c, err := prov.GetCredentials(ctx, streamID)
		if err != nil {
			return "", "", nil, err
		}
		return c.User, c.Pass, c.ServerAddrs, nil
	})

	// host-route для IP TURN-серверов в обход VPN-туннеля.
	var routeCallback func(net.IP)
	if s.cfg.Routes && !s.cfg.Tunnel.Enabled() {
		rm, rmErr := routemgr.New(log)
		if rmErr != nil {
			log.Warnf("route manager disabled: %v", rmErr)
		} else if rm != nil {
			defer func() {
				_ = rm.Close()
			}()
			routeCallback = rm.Callback()
			log.Infof("route manager: gateway=%s", rm.Gateway())
		}
	}

	if s.cfg.Proxy.Mode == config.ProxyModeTCP {
		return s.relayTCP(ctx, prov, getCreds, peer, routeCallback)
	}
	return s.relayUDP(ctx, prov, getCreds, peer, routeCallback)
}

func (s *Session) relayUDP(ctx context.Context, prov provider.Provider, getCreds udprelay.GetCredsFunc, peer *net.UDPAddr, routeCallback func(net.IP)) error {
	log := s.deps.Logger
	local, err := s.localConn(ctx)
	if err != nil {
		return err
	}
	if s.deps.LocalPipe == nil {
		defer func() { _ = local.Close() }()
	}

	dialer := &dtlsdial.Dialer{
		HandshakeTimeout: s.opts.UDPHandshakeTimeout,
		HandshakeSem:     make(chan struct{}, s.opts.HandshakeConcurrency),
	}
	params := &udprelay.Params{
		Host:         s.cfg.TURN.Host,
		Port:         s.cfg.TURN.Port,
		TransportUDP: s.cfg.TURN.TransportUDP,
		Profile:      string(s.cfg.Obf.Profile),
		ObfKey:       s.cfg.Obf.Key,
		ObfTiming:    s.cfg.Obf.Timing,
		GetCreds:     getCreds,
		ClientID:     s.cfg.ClientID,
		TrafficStats: s.trafficStats(),
	}
	return udprelay.Run(ctx, dialer, prov, log, &s.connected, routeCallback, params, peer, local, s.total)
}

func (s *Session) relayTCP(ctx context.Context, prov provider.Provider, getCreds udprelay.GetCredsFunc, peer *net.UDPAddr, routeCallback func(net.IP)) error {
	deps := &tcprelay.Deps{
		DTLSDialer: &dtlsdial.Dialer{
			HandshakeTimeout: s.opts.TCPHandshakeTimeout,
			HandshakeSem:     make(chan struct{}, s.opts.HandshakeConcurrency),
		},
		Auth:             prov,
		Log:              s.deps.Logger,
		ConnectedStreams: &s.connected,
		OnTURNServer:     routeCallback,
		Recycle:          s.recycleCh,
	}
	params := &tcprelay.Params{
		Host:         s.cfg.TURN.Host,
		Port:         s.cfg.TURN.Port,
		TransportUDP: s.cfg.TURN.TransportUDP,
		Profile:      string(s.cfg.Obf.Profile),
		ObfKey:       s.cfg.Obf.Key,
		ObfTiming:    s.cfg.Obf.Timing,
		GetCreds:     getCreds,
		KCPProfile:   s.cfg.KCP.Profile,
		ClientID:     s.cfg.ClientID,
		TrafficStats: s.trafficStats(),
	}
	return tcprelay.Run(ctx, deps, params, peer, s.cfg.Proxy.Listen, s.total)
}

func (s *Session) localConn(ctx context.Context) (net.PacketConn, error) {
	if s.deps.LocalPipe != nil {
		s.deps.Logger.Infof("local peer: in-process pipe (no loopback socket)")
		return s.deps.LocalPipe, nil
	}
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", s.cfg.Proxy.Listen)
	if err != nil {
		return nil, fmt.Errorf("udprelay listen %s: %w", s.cfg.Proxy.Listen, err)
	}
	return conn, nil
}

func (s *Session) trafficStats() *stats.Stats {
	if s.traffic == nil {
		return nil
	}
	return s.traffic.stats
}
