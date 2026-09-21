package tcprelay

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
)

type fakeAuth struct {
	authErr      error
	backoffUntil int64
	handled      atomic.Int32
	reset        atomic.Int32
	dropped      atomic.Int32
}

func TestRetryDelayAllocationQuota(t *testing.T) {
	for range 100 {
		delay := retryDelay(&fakeAuth{}, errors.Join(errors.New("allocate"), turndial.ErrAllocQuota))
		if delay < 15*time.Second || delay >= 30*time.Second {
			t.Fatalf("quota retry delay = %s", delay)
		}
	}
}

func (a *fakeAuth) IsAuthError(err error) bool {
	return a.authErr != nil && errors.Is(err, a.authErr)
}

func (a *fakeAuth) HandleAuthError(int) bool {
	a.handled.Add(1)
	return true
}

func (a *fakeAuth) ResetErrors(int)     { a.reset.Add(1) }
func (a *fakeAuth) DropCredentials(int) { a.dropped.Add(1) }

func (a *fakeAuth) BackoffUntilUnix() int64 { return a.backoffUntil }

// Фатальная ошибка провайдера обязана останавливать клиента, а не крутиться в ретраях.
func TestMaintainSessionReportsFatal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	auth := &fakeAuth{}
	deps := &Deps{Log: logx.Nop(), Auth: auth}
	params := &Params{
		Dial: func(context.Context, int) (*turndial.Stream, error) {
			return nil, provider.ErrFatalNoStreams
		},
	}

	fatalCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainSession(ctx, deps, params, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			1, newSessionPool(nil), func(err error) { fatalCh <- err })
	}()

	select {
	case err := <-fatalCh:
		if !errors.Is(err, provider.ErrFatalNoStreams) {
			t.Fatalf("fatal err = %v, want ErrFatalNoStreams", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fatal not reported")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("maintainSession did not return after fatal")
	}
	if auth.reset.Load() != 0 {
		t.Errorf("ResetErrors called %d times on failure", auth.reset.Load())
	}
}

// Ошибка авторизации обязана инвалидировать реквизиты, иначе ретраи идут мёртвыми кредами.
func TestMaintainSessionHandlesAuthError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	authErr := errors.New("401 Unauthorized")
	auth := &fakeAuth{authErr: authErr}
	deps := &Deps{Log: logx.Nop(), Auth: auth}
	params := &Params{
		Dial: func(context.Context, int) (*turndial.Stream, error) {
			return nil, authErr
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainSession(ctx, deps, params, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			1, newSessionPool(nil), func(error) {})
	}()

	deadline := time.Now().Add(15 * time.Second)
	for auth.handled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if auth.handled.Load() == 0 {
		t.Fatal("HandleAuthError never called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("maintainSession did not return after cancel")
	}
}

func TestRetryDelayHonorsProviderBackoff(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{backoffUntil: time.Now().Add(42 * time.Second).Unix()}
	got := retryDelay(auth, provider.ErrBackoffActive)
	if got < 30*time.Second || got > 45*time.Second {
		t.Errorf("retryDelay() = %v, want ~42s from provider lockout", got)
	}

	stale := &fakeAuth{backoffUntil: time.Now().Add(-time.Minute).Unix()}
	if got := retryDelay(stale, provider.ErrBackoffActive); got != 5*time.Second {
		t.Errorf("retryDelay(stale lockout) = %v, want 5s", got)
	}

	if got := retryDelay(&fakeAuth{}, provider.ErrBackoffActive); got != providerBackoffDelay {
		t.Errorf("retryDelay(no lockout) = %v, want %v", got, providerBackoffDelay)
	}
}

// Джиттер разводит одновременный отказ всех сессий пула.
func TestRetryDelayJittersOrdinaryErrors(t *testing.T) {
	t.Parallel()

	seen := map[time.Duration]bool{}
	for range 64 {
		d := retryDelay(&fakeAuth{}, errors.New("boom"))
		if d < setupRetryDelay || d >= setupRetryDelay+setupRetryJitter {
			t.Fatalf("retryDelay() = %v, want [%v, %v)", d, setupRetryDelay, setupRetryDelay+setupRetryJitter)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Error("retryDelay() constant, jitter missing")
	}
}

func TestAwaitDeadOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	s := &session{smux: fakeSession(t), permDead: make(chan struct{})}
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	if awaitDead(ctx, logx.Nop(), s, 1) {
		t.Error("awaitDead() = true on ctx cancel, want false")
	}
}

func TestAwaitDeadOnPermDead(t *testing.T) {
	t.Parallel()

	permDead := make(chan struct{})
	s := &session{smux: fakeSession(t), permDead: permDead}
	close(permDead)

	if !awaitDead(context.Background(), logx.Nop(), s, 1) {
		t.Error("awaitDead() = false on permDead, want true")
	}
}

func TestAwaitDeadOnClosedSession(t *testing.T) {
	t.Parallel()

	sess := fakeSession(t)
	s := &session{smux: sess, permDead: make(chan struct{})}
	_ = sess.Close()

	if !awaitDead(context.Background(), logx.Nop(), s, 1) {
		t.Error("awaitDead() = false on closed smux, want true")
	}
}

// Рецикл рвёт сессии, но состав пула не трогает: их переподнимает maintainSession.
func TestWatchRecycleClosesSessions(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := newSessionPool(nil)
	ps := pool.Add(1, fakeSession(t), nil)
	recycle := make(chan struct{}, 1)
	go watchRecycle(ctx, logx.Nop(), pool, recycle)

	recycle <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for !ps.sess.IsClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !ps.sess.IsClosed() {
		t.Fatal("watchRecycle did not close session")
	}
	if pool.Count() != 1 {
		t.Errorf("Count() = %d, want 1", pool.Count())
	}
}

func TestNextBackoffGrowsAndCaps(t *testing.T) {
	t.Parallel()

	d := nextBackoff(0)
	if d != minAcceptBackoff {
		t.Fatalf("nextBackoff(0) = %v, want %v", d, minAcceptBackoff)
	}
	prev := d
	for range 20 {
		d = nextBackoff(d)
		if d < prev {
			t.Fatalf("nextBackoff shrank: %v -> %v", prev, d)
		}
		prev = d
	}
	if d != maxAcceptBackoff {
		t.Errorf("nextBackoff capped at %v, want %v", d, maxAcceptBackoff)
	}
}

// Без Auth в Deps пакет обязан работать: тесты и вызовы без провайдера не должны падать.
func TestDepsAuthDefaultsToNop(t *testing.T) {
	t.Parallel()

	d := &Deps{}
	if d.auth().IsAuthError(errors.New("x")) {
		t.Error("nopAuth.IsAuthError() = true")
	}
	if d.auth().HandleAuthError(1) {
		t.Error("nopAuth.HandleAuthError() = true")
	}
	if d.auth().BackoffUntilUnix() != 0 {
		t.Error("nopAuth.BackoffUntilUnix() != 0")
	}
	d.auth().ResetErrors(1)
	d.auth().DropCredentials(1)
}

// Прикладные счётчики не должны включать оверхед ARQ - его считает WireConn.
func TestProxyConnCountsApplicationBytes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backendAddr := echoBackend(t)
	traffic := stats.New(true)
	pool := newSessionPool(nil)
	ps := pool.Add(1, pairedSession(t, ctx, backendAddr), traffic)

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	go proxyConn(ctx, logx.Nop(), remote, ps, 1)

	payload := []byte("hello over smux")
	if _, err := local.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if err := local.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Read(got); err != nil {
		t.Fatal(err)
	}

	var tx, rx uint64
	for deadline := time.Now().Add(5 * time.Second); ; {
		if tx, rx = traffic.Counters(); tx == uint64(len(payload)) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if tx != uint64(len(payload)) {
		t.Errorf("tx = %d, want %d", tx, len(payload))
	}
	if rx != uint64(len(payload)) {
		t.Errorf("rx = %d, want %d", rx, len(payload))
	}
	if traffic.LivenessRx() != rx {
		t.Errorf("LivenessRx() = %d, want %d without WireConn", traffic.LivenessRx(), rx)
	}
}
