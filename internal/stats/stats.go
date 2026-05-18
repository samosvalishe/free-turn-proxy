// Package stats provides throughput counters and a counting net.Conn wrapper
// shared between the client and server binaries.
package stats

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Stats tracks tx/rx byte counters. When Enabled is false, Add* are no-ops and
// LogEvery returns immediately, matching the original isDebug-gated behavior.
type Stats struct {
	tx      atomic.Uint64
	rx      atomic.Uint64
	enabled bool
}

// New returns a Stats with the given enabled flag.
func New(enabled bool) *Stats {
	return &Stats{enabled: enabled}
}

// AddTx records n bytes sent.
func (s *Stats) AddTx(n int) {
	if !s.enabled || n <= 0 {
		return
	}
	s.tx.Add(uint64(n))
}

// AddRx records n bytes received.
func (s *Stats) AddRx(n int) {
	if !s.enabled || n <= 0 {
		return
	}
	s.rx.Add(uint64(n))
}

// LogEvery emits a throughput summary every 5s via logf until ctx is canceled.
// No-op if Stats is disabled.
func (s *Stats) LogEvery(ctx context.Context, logf func(string, ...any), label, txName, rxName string) {
	if !s.enabled {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var prevTx, prevRx uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tx := s.tx.Load()
			rx := s.rx.Load()
			deltaTx := tx - prevTx
			deltaRx := rx - prevRx
			prevTx = tx
			prevRx = rx

			if deltaTx == 0 && deltaRx == 0 {
				continue
			}

			logf(
				"%s throughput: %s=%s %s=%s total_%s=%s total_%s=%s",
				label,
				txName,
				FormatBitsPerSecond(deltaTx, 5*time.Second),
				rxName,
				FormatBitsPerSecond(deltaRx, 5*time.Second),
				txName,
				FormatByteCount(tx),
				rxName,
				FormatByteCount(rx),
			)
		}
	}
}

// FormatBitsPerSecond renders a bandwidth value from a byte count and interval.
func FormatBitsPerSecond(bytes uint64, interval time.Duration) string {
	if interval <= 0 {
		interval = time.Second
	}

	bps := float64(bytes*8) / interval.Seconds()
	if bps >= 1_000_000 {
		return fmt.Sprintf("%.2f Mbit/s", bps/1_000_000)
	}
	if bps >= 1_000 {
		return fmt.Sprintf("%.1f kbit/s", bps/1_000)
	}
	return fmt.Sprintf("%.0f bit/s", bps)
}

// FormatByteCount renders a human-readable byte count.
func FormatByteCount(bytes uint64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MiB", float64(bytes)/(1024*1024))
	}
	if bytes >= 1024 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%d B", bytes)
}

// CountingConn wraps a net.Conn and accumulates rx/tx byte counters in Stats.
type CountingConn struct {
	net.Conn
	Stats *Stats
}

// Read reads from the underlying conn and updates rx counter.
func (c *CountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.Stats.AddRx(n)
	return n, err
}

// Write writes to the underlying conn and updates tx counter.
func (c *CountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.Stats.AddTx(n)
	return n, err
}
