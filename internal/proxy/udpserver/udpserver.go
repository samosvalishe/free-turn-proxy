// Package udpserver пересылает датаграммы между входящим DTLS-соединением и локальным UDP backend.
package udpserver

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

const (
	udpRelayBufSize    = 2048
	udpIdleTimeout     = 30 * time.Minute
	udpDeadlineRefresh = time.Minute
)

// Handle выполняет двунаправленный релей UDP-пакетов между conn и connectAddr.
func Handle(ctx context.Context, logger logx.Logger, conn net.Conn, connectAddr string) {
	serverConn, err := (&net.Dialer{}).DialContext(ctx, "udp", connectAddr)
	if err != nil {
		logger.Errorf("udpserver: dial backend: %v", err)
		return
	}
	defer func() {
		if err = serverConn.Close(); err != nil {
			logger.Errorf("udpserver: close outgoing connection: %s", err)
		}
	}()

	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	context.AfterFunc(ctx2, func() {
		if err := conn.SetDeadline(time.Now()); err != nil {
			logger.Errorf("udpserver: set incoming deadline: %s", err)
		}
		if err := serverConn.SetDeadline(time.Now()); err != nil {
			logger.Errorf("udpserver: set outgoing deadline: %s", err)
		}
	})

	var wg sync.WaitGroup
	wg.Go(func() {
		defer cancel()
		copyOne(ctx2, logger, conn, serverConn)
	})
	wg.Go(func() {
		defer cancel()
		copyOne(ctx2, logger, serverConn, conn)
	})
	wg.Wait()
}

func copyOne(ctx context.Context, logger logx.Logger, src, dst net.Conn) {
	buf := make([]byte, udpRelayBufSize)
	var refreshed time.Time
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if now := time.Now(); now.Sub(refreshed) >= udpDeadlineRefresh {
			refreshed = now
			if err := src.SetReadDeadline(now.Add(udpIdleTimeout)); err != nil {
				logger.Errorf("udpserver: set read deadline: %s", err)
				return
			}
			if err := dst.SetWriteDeadline(now.Add(udpIdleTimeout)); err != nil {
				logger.Errorf("udpserver: set write deadline: %s", err)
				return
			}
		}
		n, err := src.Read(buf)
		if err != nil {
			logger.Debugf("udpserver: read: %s", err)
			return
		}
		if _, werr := dst.Write(buf[:n]); werr != nil {
			logger.Debugf("udpserver: write: %s", werr)
			return
		}
	}
}
