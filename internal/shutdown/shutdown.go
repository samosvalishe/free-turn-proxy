// Package shutdown связывает сигналы ОС с отменой контекста процесса.
package shutdown

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

// Grace - время на доработку после отмены контекста.
const Grace = 5 * time.Second

// Watch отменяет ctx по SIGINT/SIGTERM. Возвращённый stop снимает обработчик при
// штатном выходе - без него процесс уходил бы в форс-выход с кодом 1 на каждой остановке.
func Watch(parent context.Context, log logx.Logger) (context.Context, func()) {
	log = logx.OrNop(log)
	ctx, cancel := context.WithCancel(parent)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})

	go func() {
		select {
		case <-sig:
		case <-done:
			return
		}
		log.Infof("Terminating...")
		cancel()
		select {
		case <-sig:
			log.Warnf("Forced exit on second signal")
		case <-done:
			return
		case <-time.After(Grace):
			log.Warnf("Forced exit after %s shutdown timeout", Grace)
		}
		// Форс-выход сжигает defer-ы (в том числе снятие host-маршрутов) - только как крайняя мера.
		os.Exit(1)
	}()

	return ctx, func() {
		signal.Stop(sig)
		close(done)
		cancel()
	}
}
