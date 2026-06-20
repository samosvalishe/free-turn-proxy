package ios

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/stats"
)

// Мгновенная скорость (байт/с), пересчитывается startRateMeter раз в секунду.
// Суммарные счётчики живут в stats (stats.GlobalCounters) — там же, где учёт байт.
var (
	globalTxRate atomic.Int64
	globalRxRate atomic.Int64
)

// startRateMeter раз в секунду пересчитывает мгновенную скорость (байт/с) по
// дельте суммарных счётчиков stats. Учётом трафика управляет Start/Stop явно
// (StartGlobalCount/StopGlobalCount) — здесь только чтение. Живёт ровно одну
// сессию: завершается по отмене ctx с остановкой тикера, без вечных горутин.
func startRateMeter(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var prevTx, prevRx uint64
	for {
		select {
		case <-ctx.Done():
			globalTxRate.Store(0)
			globalRxRate.Store(0)
			return
		case <-t.C:
			tx, rx := stats.GlobalCounters()
			// Счётчики могли обнулиться (StopGlobalCount) — пропускаем такой тик,
			// чтобы не получить отрицательную дельту через переполнение uint64.
			if tx < prevTx || rx < prevRx {
				prevTx, prevRx = tx, rx
				continue
			}
			globalTxRate.Store(clampToInt64(tx - prevTx))
			globalRxRate.Store(clampToInt64(rx - prevRx))
			prevTx, prevRx = tx, rx
		}
	}
}
