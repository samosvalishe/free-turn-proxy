// Package tcpfwdserver implements the server-side VLESS lane: KCP+smux over a
// DTLS connection, with each smux stream forwarded as a TCP connection to the
// backend (Xray/VLESS). Bond streams are auto-detected by their magic prefix
// and dispatched to a bondserver.Registry.
package tcpfwdserver

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/bondserver"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/transport/kcptun"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/bondframe"
	"github.com/xtaci/smux"
)

// Handle wraps dtlsConn in KCP+smux and forwards each accepted stream as a TCP
// connection to connectAddr. Streams whose first 4 bytes match the bond magic
// are handed off to registry.
func Handle(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, dtlsConn net.Conn, connectAddr string) {
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	st := stats.New(logger.DebugEnabled())
	go st.LogEvery(
		statsCtx,
		logger.Debugf,
		"[VLESS "+dtlsConn.RemoteAddr().String()+"]",
		"to-client",
		"from-client",
	)

	kcpSess, err := kcptun.NewKCPOverDTLS(&stats.CountingConn{Conn: dtlsConn, Stats: st}, true)
	if err != nil {
		log.Printf("KCP session error: %s", err)
		return
	}
	defer func() {
		if closeErr := kcpSess.Close(); closeErr != nil {
			log.Printf("failed to close KCP session: %v", closeErr)
		}
	}()
	logger.Debugf("KCP session established (server)")

	smuxSess, err := smux.Server(kcpSess, kcptun.DefaultSmuxConfig())
	if err != nil {
		log.Printf("smux server error: %s", err)
		return
	}
	defer func() {
		if err := smuxSess.Close(); err != nil {
			log.Printf("failed to close smux session: %v", err)
		}
	}()
	logger.Debugf("smux session established (server)")

	var wg sync.WaitGroup
	for {
		stream, err := smuxSess.AcceptStream()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				log.Printf("smux accept error: %s", err)
			}
			break
		}

		s := stream
		wg.Go(func() {
			handleStream(ctx, logger, registry, s, connectAddr)
		})
	}
	wg.Wait()
}

func handleStream(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, s *smux.Stream, connectAddr string) {
	var prefix [4]byte
	if _, err := io.ReadFull(s, prefix[:]); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Printf("smux stream prefix read error: %v", err)
		}
		_ = s.Close()
		return
	}
	if string(prefix[:]) == bondframe.Magic {
		logger.Debugf("auto-detected bond smux stream")
		registry.HandleStreamAfterMagic(ctx, s, connectAddr, prefix)
		return
	}

	defer func() {
		if err := s.Close(); err != nil && err != smux.ErrGoAway {
			log.Printf("failed to close smux stream: %v", err)
		}
	}()

	backendConn, err := net.DialTimeout("tcp", connectAddr, 10*time.Second)
	if err != nil {
		log.Printf("backend dial error: %s", err)
		return
	}
	defer func() {
		if err := backendConn.Close(); err != nil {
			log.Printf("failed to close backend connection: %v", err)
		}
	}()

	pipeConn(ctx, logger, &prefixedConn{Conn: s, prefix: prefix[:]}, backendConn)
}

// prefixedConn re-injects the magic-peek prefix on the first reads so the
// backend sees the full original byte stream.
type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// pipeConn copies data bidirectionally between two connections.
func pipeConn(ctx context.Context, logger logx.Logger, c1, c2 net.Conn) {
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	context.AfterFunc(ctx2, func() {
		if err := c1.SetDeadline(time.Now()); err != nil {
			logger.Debugf("pipeConn: failed to set deadline c1: %v", err)
		}
		if err := c2.SetDeadline(time.Now()); err != nil {
			logger.Debugf("pipeConn: failed to set deadline c2: %v", err)
		}
	})

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := io.Copy(c1, c2); err != nil {
			logger.Debugf("pipeConn: c1<-c2 copy error: %v", err)
		}
	})
	wg.Go(func() {
		if _, err := io.Copy(c2, c1); err != nil {
			logger.Debugf("pipeConn: c2<-c1 copy error: %v", err)
		}
	})
	wg.Wait()

	if err := c1.SetDeadline(time.Time{}); err != nil {
		logger.Debugf("pipeConn: failed to reset deadline c1: %v", err)
	}
	if err := c2.SetDeadline(time.Time{}); err != nil {
		logger.Debugf("pipeConn: failed to reset deadline c2: %v", err)
	}
}
