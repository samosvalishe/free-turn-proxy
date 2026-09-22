package udprelay

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
)

type deadlineRecorder struct {
	net.PacketConn
	mu   sync.Mutex
	last time.Time
	set  bool
}

func TestRunStopsOnLocalReadFailure(t *testing.T) {
	dialer, params, peer, local := runFatalDeps(t)
	params.Dial = func(ctx context.Context, _ int) (*turndial.Stream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_ = local.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var connected atomic.Int32
	err := Run(ctx, dialer, NopAuth{}, logx.Nop(), &connected, nil, params, peer, local, 1)
	if !errors.Is(err, ErrLocalRead) || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Run = %v, want local read failure wrapping closed connection", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Run waited for outer cancellation")
	}
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	d.last, d.set = t, true
	d.mu.Unlock()
	return d.PacketConn.SetReadDeadline(t)
}

func (d *deadlineRecorder) lastDeadline() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last, d.set
}

func runFatalDeps(t *testing.T) (*dtlsdial.Dialer, *Params, *net.UDPAddr, *deadlineRecorder) {
	t.Helper()

	pipe, peerSide := netconn.PacketPipe(1500, 4)
	t.Cleanup(func() { _ = pipe.Close(); _ = peerSide.Close() })

	params := &Params{
		Dial: func(context.Context, int) (*turndial.Stream, error) {
			return nil, provider.ErrFatalNoStreams
		},
	}
	return &dtlsdial.Dialer{HandshakeTimeout: 100 * time.Millisecond},
		params,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9},
		&deadlineRecorder{PacketConn: pipe}
}

// Фатальная ошибка провайдера отменяет только runCtx, поэтому будить runListener обязан
// сам Run: на молчащем LocalPipe тот сидит в ReadFrom, и без этого wg.Wait висит вечно.
func TestRunReturnsOnFatalProviderError(t *testing.T) {
	t.Parallel()
	dialer, params, peer, local := runFatalDeps(t)

	var connected atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), dialer, NopAuth{}, logx.Nop(), &connected, nil, params, peer, local, 1)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrFatal) {
			t.Fatalf("err = %v, want ErrFatal", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run hung on fatal provider error")
	}

	// Тот же LocalPipe достаётся следующей попытке: просроченный дедлайн порвал бы её чтение.
	last, set := local.lastDeadline()
	if !set {
		t.Fatal("read deadline was never touched")
	}
	if !last.IsZero() {
		t.Fatalf("read deadline left at %v, want cleared", last)
	}
}

// Паника в горутине стрима эквивалентна фатальной ошибке: продолжать релей нельзя, а
// ронять процесс приложения (ядро линкуется в него) - тем более.
func TestRunReturnsOnStreamPanic(t *testing.T) {
	t.Parallel()
	dialer, params, peer, local := runFatalDeps(t)
	params.Dial = func(context.Context, int) (*turndial.Stream, error) {
		panic("boom")
	}

	var connected atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), dialer, NopAuth{}, logx.Nop(), &connected, nil, params, peer, local, 1)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, safego.ErrPanic) {
			t.Fatalf("err = %v, want ErrPanic", err)
		}
		if !errors.Is(err, ErrFatal) {
			t.Fatalf("err = %v, want ErrFatal", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run hung on panic in stream goroutine")
	}
}

func TestRunFatalDoesNotWaitWarmupBarrier(t *testing.T) {
	t.Parallel()
	dialer, params, peer, local := runFatalDeps(t)

	var connected atomic.Int32
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- Run(context.Background(), dialer, NopAuth{}, logx.Nop(), &connected, nil, params, peer, local, 4)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrFatal) {
			t.Fatalf("err = %v, want ErrFatal", err)
		}
		if elapsed := time.Since(start); elapsed >= streamStartBarrier {
			t.Fatalf("Run took %v, want less than warm-up barrier %v", elapsed, streamStartBarrier)
		}
	case <-time.After(2 * streamStartBarrier):
		t.Fatal("Run hung on fatal provider error")
	}
}
