// Package udprelay implements the UDP-mode proxy loop: it terminates DTLS from a
// local peer (WireGuard) and relays its packets through a per-stream TURN
// allocation back to a remote peer. Run is the entrypoint; it owns the local
// listener, the inbound dispatch fan-in, and the per-stream DTLS/TURN loops.
package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/btp/internal/logx"
	"github.com/samosvalishe/btp/internal/proxy/common"
	"github.com/samosvalishe/btp/internal/transport/dtlsdial"
)

// GetCredsFunc is re-exported from common so callers can keep their imports
// scoped to this package.
type GetCredsFunc = common.GetCredsFunc

// AuthHandler is the subset of vkauth.Client this package needs. Defined as
// an interface so tests can inject fakes; the production wiring still imports
// vkauth for its sentinel errors (ErrFatalCaptchaNoStreams, etc.).
type AuthHandler interface {
	IsAuthError(err error) bool
	HandleAuthError(streamID int) bool
	ResetErrors(streamID int)
	LockoutUntilUnix() int64
}

// Params is the per-stream TURN/wrap configuration shared by the DTLS and TURN loops.
type Params struct {
	Host     string
	Port     string
	Link     string
	UDP      bool
	WrapKey  []byte
	GetCreds GetCredsFunc
}

// ErrFatal is returned by Run when a stream encounters a condition that
// requires the entire application to exit (e.g. manual captcha solver failed
// with no connected streams). Callers should check with errors.Is and call
// os.Exit themselves — udprelay does not reach into the host process.
var ErrFatal = errors.New("udprelay: fatal error")

// Deps groups everything the loops need from the host process. The atomics
// are owned by Run and exposed here so DTLSLoop/TURNLoop can share them when
// called directly (Run wires them automatically).
type Deps struct {
	DTLSDialer       *dtlsdial.Dialer
	Auth             AuthHandler
	Log              logx.Logger
	ActiveLocalPeer  *atomic.Value
	ConnectedStreams *atomic.Int32
	// fatalCh is an internal signalling channel; set by Run, written by
	// TURNLoop, and drained by Run to propagate the fatal error up.
	fatalCh chan error
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Run is the UDP-mode entrypoint. It binds listenAddr, fans inbound packets
// into a shared queue, and spawns numStreams pairs of (DTLSLoop, TURNLoop).
// connectedStreams is owned by the caller (vkauth reads it via StreamsAlive)
// and incremented/decremented by oneTURN.
// Returns after all stream loops exit (i.e. when ctx is cancelled).
// If a fatal captcha condition is encountered, Run returns ErrFatal so the
// caller can perform os.Exit without udprelay reaching into the host process.
func Run(ctx context.Context, dtlsDialer *dtlsdial.Dialer, auth AuthHandler, logger logx.Logger, connectedStreams *atomic.Int32, params *Params, peer *net.UDPAddr, listenAddr string, numStreams int) error {
	listenConn, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("udprelay listen %s: %w", listenAddr, err)
	}
	context.AfterFunc(ctx, func() {
		if closeErr := listenConn.Close(); closeErr != nil {
			logger.Errorf("udprelay: close local connection: %s", closeErr)
		}
	})

	if numStreams <= 0 {
		numStreams = 1
	}

	fatalCh := make(chan error, 1)
	var activeLocalPeer atomic.Value
	deps := &Deps{
		DTLSDialer:       dtlsDialer,
		Auth:             auth,
		Log:              logger,
		ActiveLocalPeer:  &activeLocalPeer,
		ConnectedStreams: connectedStreams,
		fatalCh:          fatalCh,
	}

	// runCtx is cancelled when a fatal error is detected (via fatalCh), which
	// propagates cancellation into all stream loops without requiring them to
	// hold a reference to the host-process cancel function.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	inboundChan := make(chan *Packet, inboundQueueCap)
	wg := sync.WaitGroup{}
	wg.Go(func() {
		runListener(runCtx, listenConn, &activeLocalPeer, inboundChan)
	})
	t := time.Tick(200 * time.Millisecond)

	// Stream 1 gets okchan so it can signal the first successful handshake to
	// the log. All streams start concurrently — no gate between stream 1 and
	// the rest, so a slow DTLS handshake on stream 1 never delays streams 2..N.
	okchan := make(chan struct{}, 1)
	for i := 0; i < numStreams; i++ {
		cchan := make(chan net.PacketConn)
		var ok chan<- struct{}
		if i == 0 {
			ok = okchan
		}
		streamID := i + 1
		wg.Go(func() {
			DTLSLoop(runCtx, deps, peer, listenConn, inboundChan, cchan, ok, streamID)
		})
		wg.Go(func() {
			TURNLoop(runCtx, deps, params, peer, cchan, t, streamID)
		})
	}

	// If a fatal error was sent, cancel remaining goroutines and propagate up.
	// watcherDone synchronises the watcher goroutine with Run's return so the
	// fatalErr load happens-after the store.
	var fatalErr atomic.Pointer[error]
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case err := <-fatalCh:
			fatalErr.Store(&err)
			runCancel()
		case <-runCtx.Done():
		}
	}()

	wg.Wait()
	runCancel()
	<-watcherDone
	if p := fatalErr.Load(); p != nil {
		return *p
	}
	return nil
}
