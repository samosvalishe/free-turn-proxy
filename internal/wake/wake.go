package wake

import (
	"context"
	"time"
)

func Watch(ctx context.Context, tick, threshold time.Duration, onGap func(time.Duration)) {
	t := time.NewTicker(tick)
	defer t.Stop()

	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			// Round(0) убирает монотонные часы: их разница с wall clock отражает сон.
			gap := now.Round(0).Sub(last.Round(0)) - now.Sub(last)
			last = now
			if gap < threshold {
				continue
			}
			if onGap != nil {
				onGap(gap)
			}
		}
	}
}
