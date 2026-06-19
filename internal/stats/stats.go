// Package stats - счётчики пропускной способности и обёртка net.Conn
// с подсчётом байт. Используется и клиентом, и сервером.
package stats

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Stats хранит счётчики tx/rx байт. При enabled=false Add* - no-op,
// LogEvery возвращается сразу (поведение под isDebug).
type Stats struct {
	tx      atomic.Uint64
	rx      atomic.Uint64
	enabled bool
}

// New возвращает Stats с заданным флагом enabled.
func New(enabled bool) *Stats {
	return &Stats{enabled: enabled}
}

// Сессионные глобальные счётчики суммарного трафика. Инкремент идёт только
// между StartGlobalCount и StopGlobalCount — этим явно управляет вызывающий
// (mobile-биндинг на старте/дисконнекте сессии). Рассчитаны на одну активную
// сессию в процессе; вне сессии учёта нет.
var (
	globalTx    atomic.Uint64
	globalRx    atomic.Uint64
	trackGlobal atomic.Bool
)

// StartGlobalCount включает учёт суммарного трафика в глобальных счётчиках.
func StartGlobalCount() {
	trackGlobal.Store(true)
}

// StopGlobalCount выключает учёт и обнуляет глобальные счётчики.
func StopGlobalCount() {
	trackGlobal.Store(false)
	globalTx.Store(0)
	globalRx.Store(0)
}

// GlobalCounters возвращает накопленные суммарные tx/rx байт текущей сессии.
func GlobalCounters() (tx, rx uint64) {
	return globalTx.Load(), globalRx.Load()
}

// AddTx учитывает n переданных байт.
func (s *Stats) AddTx(n int) {
	if n <= 0 {
		return
	}
	if trackGlobal.Load() {
		globalTx.Add(uint64(n))
	}
	if !s.enabled {
		return
	}
	s.tx.Add(uint64(n))
}

// AddRx учитывает n полученных байт.
func (s *Stats) AddRx(n int) {
	if n <= 0 {
		return
	}
	if trackGlobal.Load() {
		globalRx.Add(uint64(n))
	}
	if !s.enabled {
		return
	}
	s.rx.Add(uint64(n))
}

// LogEvery каждые 5с печатает сводку пропускной способности через logf,
// пока не отменён ctx. No-op, если Stats выключен.
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

// FormatBitsPerSecond форматирует пропускную способность из числа байт и интервала.
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

// FormatByteCount форматирует число байт в человекочитаемый вид.
func FormatByteCount(bytes uint64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MiB", float64(bytes)/(1024*1024))
	}
	if bytes >= 1024 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%d B", bytes)
}

// CountingConn оборачивает net.Conn и аккумулирует rx/tx-счётчики в Stats.
type CountingConn struct {
	net.Conn
	Stats *Stats
}

func (c *CountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.Stats.AddRx(n)
	return n, err
}

func (c *CountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.Stats.AddTx(n)
	return n, err
}
