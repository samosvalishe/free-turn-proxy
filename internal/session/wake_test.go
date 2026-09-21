package session

import (
	"context"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/config"
)

func newWakeSession(t *testing.T, window time.Duration) *Session {
	t.Helper()
	s, err := New(&config.Client{}, Deps{
		Options: Options{Traffic: true, WakeProbeWindow: window},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Пробуждение при живом канале не должно стоить рецикла: аллокация переживает сон, а её
// пересоздание тянет за собой поход в VK и капчу.
func TestWakeSkipsRecycleWhenTrafficFlows(t *testing.T) {
	t.Parallel()
	// Окно с запасом: пакет обязан лечь между снимком счётчика и концом окна, а не
	// проиграть гранулярности таймера.
	s := newWakeSession(t, 2*time.Second)
	s.connected.Store(1)

	done := make(chan bool, 1)
	go func() { done <- s.wakeNeedsRecycle(context.Background()) }()

	// Имитируем keepalive туннеля, идущий через relay.
	time.Sleep(50 * time.Millisecond)
	s.traffic.stats.AddRx(64)

	select {
	case need := <-done:
		if need {
			t.Fatal("recycle requested while traffic was flowing")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wakeNeedsRecycle did not finish")
	}
}

func TestWakeRecyclesOnSilence(t *testing.T) {
	t.Parallel()
	s := newWakeSession(t, 50*time.Millisecond)
	s.connected.Store(1)

	if !s.wakeNeedsRecycle(context.Background()) {
		t.Fatal("silent channel must be recycled")
	}
}

// Пробуждение во время подъёма сессии отменило бы перебор реквизитов и решение капчи.
func TestWakeSkipsRecycleWhileConnecting(t *testing.T) {
	t.Parallel()
	s := newWakeSession(t, 50*time.Millisecond)

	start := time.Now()
	if s.wakeNeedsRecycle(context.Background()) {
		t.Fatal("recycle requested while session was still connecting")
	}
	if elapsed := time.Since(start); elapsed > 40*time.Millisecond {
		t.Fatalf("probe window was waited unnecessarily (%s)", elapsed)
	}
}

// Гэп-детектор ядра и пинок с платформы приходят почти одновременно: второй сигнал
// про тот же сон не должен давать второй рецикл.
func TestWakeCollapsesBurst(t *testing.T) {
	t.Parallel()
	s := newWakeSession(t, 50*time.Millisecond)
	s.connected.Store(1)

	go s.watchWake(t.Context())

	s.Wake()
	s.Wake()

	select {
	case <-s.reconnectCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no recycle after wake on a silent channel")
	}

	select {
	case <-s.reconnectCh:
		t.Fatal("burst produced a second recycle")
	case <-time.After(200 * time.Millisecond):
	}
}
