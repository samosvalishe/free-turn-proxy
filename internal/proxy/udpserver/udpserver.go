// Package udpserver implements the server-side UDP relay: a DTLS-terminated
// stream is forwarded to a UDP backend (WireGuard) and vice-versa.
package udpserver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
)

const (
	udpRelayBufSize = 1600
	udpIdleTimeout  = 30 * time.Minute
)

// Handle forwards DTLS packets between conn and a UDP backend at connectAddr
// until either side closes. Blocks until both copy goroutines exit.
func Handle(ctx context.Context, logger logx.Logger, conn net.Conn, connectAddr string) {
	serverConn, err := net.Dial("udp", connectAddr)
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
	st := stats.New(logger.DebugEnabled())
	go st.LogEvery(
		ctx2,
		logger.Debugf,
		fmt.Sprintf("[DTLS %s]", conn.RemoteAddr()),
		"dtls-to-backend",
		"backend-to-dtls",
	)

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
		copyOne(ctx2, logger, conn, serverConn, st.AddTx)
	})
	wg.Go(func() {
		defer cancel()
		copyOne(ctx2, logger, serverConn, conn, st.AddRx)
	})
	wg.Wait()
}

// copyOne reads from src and writes to dst until ctx fires or either side errors.
// Each read/write resets an idle timeout so a stuck side closes instead of hanging.
func copyOne(ctx context.Context, logger logx.Logger, src, dst net.Conn, count func(int)) {
	buf := make([]byte, udpRelayBufSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := src.SetReadDeadline(time.Now().Add(udpIdleTimeout)); err != nil {
			logger.Errorf("udpserver: set read deadline: %s", err)
			return
		}
		n, err := src.Read(buf)
		if err != nil {
			logger.Debugf("udpserver: read: %s", err)
			return
		}
		if werr := dst.SetWriteDeadline(time.Now().Add(udpIdleTimeout)); werr != nil {
			logger.Errorf("udpserver: set write deadline: %s", werr)
			return
		}
		written, werr := dst.Write(buf[:n])
		count(written)
		if werr != nil {
			logger.Debugf("udpserver: write: %s", werr)
			return
		}
	}
}
