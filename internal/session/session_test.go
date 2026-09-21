package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
)

func newTestSession(t *testing.T, opts Options, captcha func() bool) *Session {
	t.Helper()
	cfg := &config.Client{
		TURN: config.TURNOpts{N: 2},
		VK:   config.VKOpts{Links: []string{"a"}},
	}
	s, err := New(cfg, Deps{Options: opts, CaptchaActive: captcha})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s
}

func TestNewTotalStreamsPerLink(t *testing.T) {
	cfg := &config.Client{
		TURN: config.TURNOpts{N: 3},
		VK:   config.VKOpts{Links: []string{"a", "b"}},
	}
	s, err := New(cfg, Deps{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	snap := s.Snapshot()
	if snap.Total != 6 {
		t.Fatalf("Total = %d, want 6", snap.Total)
	}
	if snap.Phase != PhaseConnecting {
		t.Fatalf("Phase = %q, want %q", snap.Phase, PhaseConnecting)
	}
}

func TestNewWithoutLinksCountsOnePool(t *testing.T) {
	cfg := &config.Client{TURN: config.TURNOpts{N: 4}}
	s, err := New(cfg, Deps{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := s.Snapshot().Total; got != 4 {
		t.Fatalf("Total = %d, want 4", got)
	}
}

func TestNewRejectsNilConfig(t *testing.T) {
	if _, err := New(nil, Deps{}); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}

func TestRunIsSingleUse(t *testing.T) {
	s := newTestSession(t, Options{}, nil)
	s.started.Store(true)
	if err := s.Run(context.Background()); err != ErrAlreadyRun {
		t.Fatalf("Run() error = %v, want %v", err, ErrAlreadyRun)
	}
}

func TestWatchFailsWhenNoStreamEverConnects(t *testing.T) {
	s := newTestSession(t, Options{
		ConnectTimeout: 20 * time.Millisecond,
		StatusInterval: 5 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.watch(ctx, cancel) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("watch() error = nil, want timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch() did not fire connect timeout")
	}
	if ctx.Err() == nil {
		t.Fatal("watch() must cancel the session on timeout")
	}
}

func TestWatchDisabledTimeoutKeepsWaiting(t *testing.T) {
	s := newTestSession(t, Options{StatusInterval: 5 * time.Millisecond}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.watch(ctx, cancel) }()

	time.Sleep(50 * time.Millisecond)
	if s.Snapshot().Phase != PhaseConnecting {
		t.Fatalf("Phase = %q, want %q", s.Snapshot().Phase, PhaseConnecting)
	}
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("watch() error = %v, want nil", err)
	}
}

func TestWatchReportsConnected(t *testing.T) {
	s := newTestSession(t, Options{
		ConnectTimeout: 20 * time.Millisecond,
		StatusInterval: 5 * time.Millisecond,
	}, nil)
	s.connected.Store(2)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.watch(ctx, cancel) }()

	time.Sleep(100 * time.Millisecond)
	snap := s.Snapshot()
	if snap.Phase != PhaseConnected || snap.Streams != 2 {
		t.Fatalf("Snapshot = %+v, want connected with 2 streams", snap)
	}
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("watch() error = %v, want nil", err)
	}
}

func TestWatchCaptchaSuspendsTimeout(t *testing.T) {
	s := newTestSession(t, Options{
		ConnectTimeout: 20 * time.Millisecond,
		StatusInterval: 5 * time.Millisecond,
	}, func() bool { return true })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.watch(ctx, cancel) }()

	time.Sleep(100 * time.Millisecond)
	if got := s.Snapshot().Phase; got != PhaseCaptcha {
		t.Fatalf("Phase = %q, want %q", got, PhaseCaptcha)
	}
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("watch() error = %v, want nil", err)
	}
}

type recordingObserver struct {
	mu     sync.Mutex
	phases []Phase
}

func (o *recordingObserver) OnPhase(phase Phase, _, _ int, _ string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.phases = append(o.phases, phase)
}

func (o *recordingObserver) snapshot() []Phase {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Phase(nil), o.phases...)
}

func TestObserverSeesOnlyChanges(t *testing.T) {
	obs := &recordingObserver{}
	s := newTestSession(t, Options{StatusInterval: 5 * time.Millisecond}, nil)
	s.deps.Observer = obs

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.watch(ctx, cancel) }()

	time.Sleep(60 * time.Millisecond)
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("phases = %v, want none while state is unchanged", got)
	}

	s.connected.Store(1)
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-errCh

	got := obs.snapshot()
	if len(got) != 1 || got[0] != PhaseConnected {
		t.Fatalf("phases = %v, want single %q", got, PhaseConnected)
	}
}

func TestRunEmitsFirstPhaseEvenIfUnchanged(t *testing.T) {
	obs := &recordingObserver{}
	s := newTestSession(t, Options{}, nil)
	s.deps.Observer = obs

	s.publish(&statusInfo{phase: PhaseConnecting, total: s.total}, true)

	if got := obs.snapshot(); len(got) != 1 || got[0] != PhaseConnecting {
		t.Fatalf("phases = %v, want single %q", got, PhaseConnecting)
	}
}

func TestObserverGetsTerminalPhase(t *testing.T) {
	obs := &recordingObserver{}
	s := newTestSession(t, Options{}, nil)
	s.deps.Observer = obs
	s.started.Store(true)

	if err := s.Run(context.Background()); err != ErrAlreadyRun {
		t.Fatalf("Run() error = %v", err)
	}
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("phases = %v, want none", got)
	}

	s.setStatus(PhaseError, 0, "boom")
	got := obs.snapshot()
	if len(got) != 1 || got[0] != PhaseError {
		t.Fatalf("phases = %v, want %q", got, PhaseError)
	}
	if snap := s.Snapshot(); snap.Err != "boom" {
		t.Fatalf("Snapshot().Err = %q, want boom", snap.Err)
	}
}

func TestLocalConnPrefersPipe(t *testing.T) {
	pipe, peer := netconn.PacketPipe(0, 0)
	t.Cleanup(func() {
		_ = pipe.Close()
		_ = peer.Close()
	})

	s := newTestSession(t, Options{}, nil)
	s.deps.LocalPipe = pipe

	got, err := s.localConn(context.Background())
	if err != nil {
		t.Fatalf("localConn() error = %v", err)
	}
	if got != pipe {
		t.Fatalf("localConn() = %v, want the injected pipe", got)
	}
}

func TestLocalConnBindsListenAddr(t *testing.T) {
	s := newTestSession(t, Options{}, nil)
	s.cfg.Proxy.Listen = "127.0.0.1:0"

	conn, err := s.localConn(context.Background())
	if err != nil {
		t.Fatalf("localConn() error = %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.LocalAddr().Network() != "udp" {
		t.Fatalf("LocalAddr() = %v, want a udp socket", conn.LocalAddr())
	}
}

func TestLocalConnReportsBindFailure(t *testing.T) {
	s := newTestSession(t, Options{}, nil)
	s.cfg.Proxy.Listen = "256.256.256.256:9"

	if _, err := s.localConn(context.Background()); err == nil {
		t.Fatal("localConn() error = nil for unusable address")
	}
}

func TestSnapshotWithoutTrafficStaysZero(t *testing.T) {
	s := newTestSession(t, Options{}, nil)
	if s.traffic != nil {
		t.Fatal("traffic must be nil when Options.Traffic is false")
	}
	if snap := s.Snapshot(); snap.TxTotal != 0 || snap.RxRate != 0 {
		t.Fatalf("Snapshot = %+v, want zero traffic", snap)
	}
}

func TestTrafficRates(t *testing.T) {
	tr := newTraffic()
	// Первый вызов только запоминает отметку - скорость считать не с чего.
	if tx, rx := tr.rates(); tx != 0 || rx != 0 {
		t.Fatalf("first rates = tx %d rx %d, want zero", tx, rx)
	}

	tr.stats.AddTx(1000)
	tr.stats.AddRx(500)
	time.Sleep(50 * time.Millisecond)

	tx, rx := tr.rates()
	if tx < 1000 || rx < 500 || rx >= tx {
		t.Fatalf("rates = tx %d rx %d, want tx >= 1000 > rx >= 500", tx, rx)
	}

	// Без нового трафика скорость обязана упасть в ноль.
	time.Sleep(10 * time.Millisecond)
	if tx, rx := tr.rates(); tx != 0 || rx != 0 {
		t.Fatalf("idle rates = tx %d rx %d, want zero", tx, rx)
	}
}

// Паника в попытке релея не должна валить процесс приложения: ядро - библиотека внутри
// процесса UI, перехватить снаружи нечем.
func TestRelayLoopTurnsPanicIntoError(t *testing.T) {
	s := newTestSession(t, Options{}, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- s.relayLoop(context.Background(), func(context.Context) error { panic("boom") }) }()

	select {
	case err := <-errCh:
		if !errors.Is(err, safego.ErrPanic) {
			t.Fatalf("relayLoop() error = %v, want ErrPanic", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relayLoop() hung on panic")
	}
}

// В tcp-режиме рецикл идёт в пул, а не в отмену попытки: отмена закрыла бы listener.
func TestReconnectRoutesByMode(t *testing.T) {
	t.Parallel()

	tcp, err := New(&config.Client{Proxy: config.ProxyOpts{Mode: config.ProxyModeTCP}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	tcp.Reconnect()
	select {
	case <-tcp.recycleCh:
	default:
		t.Error("tcp mode: recycleCh empty")
	}
	select {
	case <-tcp.reconnectCh:
		t.Error("tcp mode: reconnectCh must stay empty")
	default:
	}

	udp, err := New(&config.Client{Proxy: config.ProxyOpts{Mode: config.ProxyModeUDP}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	udp.Reconnect()
	select {
	case <-udp.reconnectCh:
	default:
		t.Error("udp mode: reconnectCh empty")
	}
}

func TestNewDirectIgnoresLinks(t *testing.T) {
	cfg := &config.Client{
		TURN:     config.TURNOpts{N: 2},
		Provider: config.ProviderOpts{Name: config.ProviderDirect},
		VK:       config.VKOpts{Links: []string{"a", "b"}},
	}
	s, err := New(cfg, Deps{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := s.Snapshot().Total; got != 2 {
		t.Fatalf("Total = %d, want 2", got)
	}
}
