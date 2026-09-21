package shutdown

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

const Grace = 5 * time.Second

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
		os.Exit(1)
	}()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			signal.Stop(sig)
			close(done)
		})
		cancel()
	}
}
