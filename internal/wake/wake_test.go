package wake

import (
	"context"
	"testing"
	"time"
)

func TestWatchStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Watch(ctx, time.Millisecond, time.Hour, nil)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Watch did not return after context cancel")
	}
}

func TestWatchIgnoresGapsBelowThreshold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	fired := make(chan time.Duration, 1)
	Watch(ctx, time.Millisecond, time.Hour, func(gap time.Duration) { fired <- gap })

	select {
	case gap := <-fired:
		t.Fatalf("onGap fired without a sleep gap: %s", gap)
	default:
	}
}
