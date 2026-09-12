package turndial

import (
	"sync/atomic"
	"testing"

	"github.com/pion/logging"
)

func newTestWatch(threshold int) (*permWatchFactory, *atomic.Int32) {
	var fired atomic.Int32
	f := &permWatchFactory{
		inner:     logging.NewDefaultLoggerFactory(),
		threshold: threshold,
		onDead:    func() { fired.Add(1) },
	}
	return f, &fired
}

func TestPermWatchFiresAfterThreshold(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 0 {
		t.Fatalf("fired too early after 1 fail: %d", fired.Load())
	}
	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 1 {
		t.Fatalf("expected 1 fire after threshold, got %d", fired.Load())
	}
	log.Warnf(permFailMarker+": %s", "boom")
	if fired.Load() != 1 {
		t.Fatalf("fired more than once: %d", fired.Load())
	}
}

func TestPermWatchResetOnSuccess(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(permFailMarker + ": x")
	log.Debug(permOKMarker)
	log.Warnf(permFailMarker + ": x")
	if fired.Load() != 0 {
		t.Fatalf("reset failed: fired=%d (fail/ok/fail не должно фаерить)", fired.Load())
	}
}

func TestPermWatchIgnoresOtherScopes(t *testing.T) {
	f, fired := newTestWatch(1)
	log := f.NewLogger("other")
	if _, ok := log.(*permWatchLogger); ok {
		t.Fatal("non-turnc scope must not be wrapped")
	}
	log.Warnf(permFailMarker)
	if fired.Load() != 0 {
		t.Fatalf("fired on non-turnc scope: %d", fired.Load())
	}
}

func TestPermWatchIgnoresUnrelatedMessages(t *testing.T) {
	f, fired := newTestWatch(1)
	log := f.NewLogger(turncScope)
	log.Debug("Started refresh permission timer")
	log.Debug("No permission to refresh")
	log.Debug("Refresh permissions successful")
	if fired.Load() != 0 {
		t.Fatalf("fired on unrelated message: %d", fired.Load())
	}
}

// Провал Refresh мимо порога ChannelBind: аллокация уже мертва.
func TestPermWatchFiresOnAllocRefreshFail(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(allocFailMarker+": %s", "all retransmissions failed")
	if fired.Load() != 1 {
		t.Fatalf("expected immediate fire, got %d", fired.Load())
	}
	log.Warnf(allocFailMarker + ": again")
	if fired.Load() != 1 {
		t.Fatalf("fired more than once: %d", fired.Load())
	}
}

// Успешный ChannelBind не отменяет мёртвую аллокацию.
func TestPermWatchAllocFailNotResetByBindOK(t *testing.T) {
	f, fired := newTestWatch(2)
	log := f.NewLogger(turncScope)

	log.Warnf(allocFailMarker + ": x")
	log.Debug(permOKMarker)
	log.Warnf(allocFailMarker + ": x")
	if fired.Load() != 1 {
		t.Fatalf("expected exactly 1 fire, got %d", fired.Load())
	}
}
