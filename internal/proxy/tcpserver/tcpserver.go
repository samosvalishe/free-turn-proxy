// Package tcpserver - серверная сторона tcp-режима: KCP+smux поверх DTLS, каждый
// smux-поток форвардится в локальный TCP backend.
package tcpserver

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bond"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcpmux"
	"github.com/xtaci/smux"
)

const backendDialTimeout = 10 * time.Second

// Handle блокирует вызывающую горутину до закрытия сессии клиентом или ctx.
func Handle(ctx context.Context, logger logx.Logger, dtlsConn net.Conn, connectAddr string, profile kcpmux.Profile) {
	handle(ctx, logger, dtlsConn, profile, func(ctx context.Context, stream *smux.Stream) {
		handleStream(ctx, logger, stream, connectAddr)
	})
}

func HandleBond(ctx context.Context, logger logx.Logger, dtlsConn net.Conn, connectAddr string, profile kcpmux.Profile, server *bond.Server, clientID string) {
	handle(ctx, logger, dtlsConn, profile, func(ctx context.Context, stream *smux.Stream) {
		if err := server.Handle(ctx, stream, clientID, connectAddr); err != nil && ctx.Err() == nil {
			logger.Debugf("tcpserver bond: %v", err)
		}
	})
}

func handle(ctx context.Context, logger logx.Logger, dtlsConn net.Conn, profile kcpmux.Profile, streamHandler func(context.Context, *smux.Stream)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kcpSess, err := kcpmux.Accept(dtlsConn, profile)
	if err != nil {
		logger.Errorf("tcpserver: %s", err)
		return
	}
	defer func() {
		if closeErr := kcpSess.Close(); closeErr != nil {
			logger.Warnf("tcpserver: close KCP session: %v", closeErr)
		}
	}()

	smuxSess, err := smux.Server(kcpSess, kcpmux.SmuxConfig())
	if err != nil {
		logger.Errorf("tcpserver: smux server: %s", err)
		return
	}
	defer func() {
		if closeErr := smuxSess.Close(); closeErr != nil {
			logger.Warnf("tcpserver: close smux session: %v", closeErr)
		}
	}()
	logger.Debugf("tcpserver: smux session established")

	// ctx живёт всё время процесса - без stop() хук копился бы на каждую сессию.
	stopOnCancel := context.AfterFunc(ctx, func() { _ = smuxSess.Close() })
	defer stopOnCancel()

	var wg sync.WaitGroup
	for {
		stream, err := smuxSess.AcceptStream()
		if err != nil {
			if ctx.Err() == nil {
				logger.Debugf("tcpserver: smux accept: %s", err)
			}
			break
		}
		wg.Go(func() {
			_ = safego.Run(logger, func() { streamHandler(ctx, stream) })
		})
	}
	cancel()
	wg.Wait()
}

func handleStream(ctx context.Context, logger logx.Logger, s *smux.Stream, connectAddr string) {
	defer func() {
		if err := s.Close(); err != nil && !errors.Is(err, smux.ErrGoAway) {
			logger.Warnf("tcpserver: close smux stream: %v", err)
		}
	}()

	backend, err := (&net.Dialer{Timeout: backendDialTimeout}).DialContext(ctx, "tcp", connectAddr)
	if err != nil {
		logger.Errorf("tcpserver: backend dial %s: %s", connectAddr, err)
		return
	}
	defer func() {
		if closeErr := backend.Close(); closeErr != nil {
			logger.Warnf("tcpserver: close backend connection: %v", closeErr)
		}
	}()

	netconn.BiCopy(ctx, s, backend, logger.Debugf)
}
