// Package udp implements the UDP-mode proxy loop: it terminates DTLS from a
// local peer (WireGuard) and relays its packets through a per-stream TURN
// allocation back to a remote peer. It owns oneDtlsConnection /
// oneTurnConnection and their retry loops; wiring (flag parsing, listener,
// inbound dispatch) stays in main.
package udp

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/dtlsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/turnpipe"
	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
	"github.com/cbeuw/connutil"
)

// Packet is a pooled UDP datagram carried from the listener to the per-stream
// DTLS worker. N is the populated prefix of Data.
type Packet struct {
	Data []byte
	N    int
}

// Pool reuses Packet buffers across the inbound hot path. Buffer size matches
// the 2048-byte default the listener loop expects.
var Pool = sync.Pool{
	New: func() any { return &Packet{Data: make([]byte, 2048)} },
}

// GetCredsFunc resolves VK TURN credentials for a (link, streamID) pair.
// Matches vkauth.Client.GetCredentials.
type GetCredsFunc func(ctx context.Context, link string, streamID int) (string, string, string, error)

// AuthHandler is the subset of vkauth.Client this package needs. Keeping it as
// an interface lets internal/proxy/udp avoid importing internal/client/vkauth.
type AuthHandler interface {
	IsAuthError(err error) bool
	HandleAuthError(streamID int) bool
	ResetErrors(streamID int)
	LockoutUntilUnix() int64
}

// Params is the per-stream TURN/wrap configuration shared by the DTLS and TURN
// loops. Equivalent of the old client/main.go turnParams.
type Params struct {
	Host     string
	Port     string
	Link     string
	UDP      bool
	WrapKey  []byte
	GetCreds GetCredsFunc
}

// Deps groups everything the loops need from the host process. ActiveLocalPeer
// is read concurrently with the listener that writes it; ConnectedStreams is
// the live-stream counter the auth layer queries.
type Deps struct {
	DTLSDialer       *dtlsdial.Dialer
	Auth             AuthHandler
	Debug            bool
	Debugf           func(format string, v ...any)
	ActiveLocalPeer  *atomic.Value
	ConnectedStreams *atomic.Int32
	AppCancel        func()
}

func (d *Deps) debugf(format string, v ...any) {
	if d.Debugf != nil {
		d.Debugf(format, v...)
	}
}

// DTLSLoop keeps a single DTLS termination alive for streamID, restarting it
// on failure with a 10-30s backoff (skipped while a captcha lockout is active
// and the prior error was a deadline). connchan is fed a fresh AsyncPacketPipe
// half on each attempt; okchan (non-nil only for stream 1) signals the first
// successful handshake.
func DTLSLoop(ctx context.Context, deps *Deps, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := oneDTLS(ctx, deps, peer, listenConn, inboundChan, connchan, okchan, streamID)
			if err != nil {
				if time.Now().Unix() < deps.Auth.LockoutUntilUnix() && strings.Contains(err.Error(), "context deadline exceeded") {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(10+rand.Intn(20)) * time.Second):
				}
			}
		}
	}
}

// TURNLoop drives the TURN allocation half. It waits for a fresh conn2 from
// the DTLS loop, throttles via t (the global 200ms tick), runs one TURN
// session, and reacts to FATAL_CAPTCHA / CAPTCHA_WAIT_REQUIRED accordingly.
func TURNLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, connchan <-chan net.PacketConn, t <-chan time.Time, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case conn2 := <-connchan:
			select {
			case <-t:
			case <-ctx.Done():
				return
			}
			c := make(chan error)
			go oneTURN(ctx, deps, params, peer, conn2, streamID, c)

			if err := <-c; err != nil {
				if strings.Contains(err.Error(), "FATAL_CAPTCHA") {
					log.Printf("[STREAM %d] Fatal manual captcha error. Shutting down application.", streamID)
					if deps.AppCancel != nil {
						deps.AppCancel()
					}
					return
				}
				if strings.Contains(err.Error(), "CAPTCHA_WAIT_REQUIRED") {
					if !strings.Contains(err.Error(), "global lockout active") {
						log.Printf("[STREAM %d] Backing off for 60 seconds to avoid IP ban...", streamID)
						select {
						case <-ctx.Done():
							return
						case <-time.After(60 * time.Second):
						}
					} else {
						lockoutEnd := deps.Auth.LockoutUntilUnix()
						sleepDuration := time.Until(time.Unix(lockoutEnd, 0))
						if sleepDuration < 0 {
							sleepDuration = 5 * time.Second
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(sleepDuration):
						}
					}
				} else {
					log.Printf("[STREAM %d] %s", streamID, err)
					time.Sleep(2 * time.Second)
				}
			}
		}
	}
}

func oneDTLS(ctx context.Context, deps *Deps, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) error {
	time.Sleep(time.Duration(rand.Intn(400)+100) * time.Millisecond)

	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()

	conn1, conn2 := connutil.AsyncPacketPipe()
	go func() {
		for {
			select {
			case <-dtlsctx.Done():
				return
			case connchan <- conn2:
			}
		}
	}()
	dtlsRaw, err1 := deps.DTLSDialer.Dial(dtlsctx, conn1, peer)
	if err1 != nil {
		return fmt.Errorf("failed to connect DTLS: %s", err1)
	}
	var dtlsConn net.Conn = dtlsRaw
	defer func() {
		if closeErr := dtlsConn.Close(); closeErr != nil {
			log.Printf("[STREAM %d] failed to close DTLS connection: %s", streamID, closeErr)
		}
		log.Printf("[STREAM %d] Closed DTLS connection\n", streamID)
	}()
	log.Printf("[STREAM %d] Established DTLS connection!\n", streamID)

	if okchan != nil {
		go func() {
			select {
			case okchan <- struct{}{}:
			case <-dtlsctx.Done():
			}
		}()
	}

	wg := sync.WaitGroup{}
	context.AfterFunc(dtlsctx, func() {
		if err := dtlsConn.SetDeadline(time.Now()); err != nil {
			log.Printf("[STREAM %d] Warning: SetDeadline failed: %v", streamID, err)
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		for {
			select {
			case <-dtlsctx.Done():
				return
			case pkt := <-inboundChan:
				_, _ = dtlsConn.Write(pkt.Data[:pkt.N])
				Pool.Put(pkt)
			}
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		buf := make([]byte, 1600)
		for {
			n, err1 := dtlsConn.Read(buf)
			if err1 != nil {
				return
			}

			if peerAddr := deps.ActiveLocalPeer.Load(); peerAddr != nil {
				if addr, ok := peerAddr.(net.Addr); ok {
					if _, err := listenConn.WriteTo(buf[:n], addr); err != nil {
						log.Printf("[STREAM %d] failed to forward packet to local peer: %v", streamID, err)
					}
				}
			}
		}
	})

	wg.Wait()
	if err := dtlsConn.SetDeadline(time.Time{}); err != nil {
		log.Printf("[STREAM %d] Failed to clear DTLS deadline: %s", streamID, err)
	}
	return nil
}

func oneTURN(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	time.Sleep(time.Duration(rand.Intn(400)+100) * time.Millisecond)
	var err error
	defer func() { c <- err }()
	user, pass, urlTarget, err1 := params.GetCreds(ctx, params.Link, streamID)
	if err1 != nil {
		err = fmt.Errorf("failed to get TURN credentials: %s", err1)
		return
	}
	stream, err1 := turnpipe.Open(ctx, turnpipe.Config{
		HostOverride: params.Host,
		PortOverride: params.Port,
		UDP:          params.UDP,
	}, peer, user, pass, urlTarget)
	if err1 != nil {
		if deps.Auth.IsAuthError(err1) {
			deps.Auth.HandleAuthError(streamID)
		}
		err = err1
		return
	}
	relayConn := stream.Relay
	deps.debugf("[STREAM %d] TURN server IP: %s", streamID, stream.ServerUDPAddr.IP)

	deps.Auth.ResetErrors(streamID)

	deps.ConnectedStreams.Add(1)
	defer func() {
		deps.ConnectedStreams.Add(-1)
		if cerr := stream.Close(); cerr != nil {
			err = fmt.Errorf("failed to close TURN stream: %s", cerr)
		}
	}()

	if deps.Debug {
		log.Printf("[STREAM %d] relayed-address=%s", streamID, relayConn.LocalAddr().String())
	}

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)
	st := stats.New(deps.Debug)
	go st.LogEvery(turnctx, deps.debugf, fmt.Sprintf("[STREAM %d] TURN", streamID), "to-turn", "from-turn")

	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			log.Printf("Failed to set relay deadline: %s", err)
		}
	})
	var internalPipeAddr atomic.Value
	var wc *wrap.Conn
	if len(params.WrapKey) == wrap.KeyLen {
		var wcErr error
		wc, wcErr = wrap.NewConn(params.WrapKey, false)
		if wcErr != nil {
			log.Printf("[STREAM %d] WRAP init failed: %v", streamID, wcErr)
			turncancel()
			return
		}
	}

	go func() {
		defer turncancel()
		buf := make([]byte, 1600)
		var wireBuf []byte
		if wc != nil {
			wireBuf = make([]byte, wrap.MaxWire(len(buf)))
		}
		for {
			if turnctx.Err() != nil {
				return
			}
			n, addr1, err1 := conn2.ReadFrom(buf)
			if err1 != nil {
				return
			}
			if turnctx.Err() != nil {
				return
			}

			internalPipeAddr.Store(addr1)

			out := buf[:n]
			if wc != nil {
				written, wrapErr := wc.WrapInto(wireBuf, out)
				if wrapErr != nil {
					log.Printf("[STREAM %d] WRAP failed: %v", streamID, wrapErr)
					return
				}
				out = wireBuf[:written]
			}

			written, err1 := relayConn.WriteTo(out, peer)
			st.AddTx(written)
			if err1 != nil {
				return
			}
		}
	}()

	wg.Go(func() {
		defer turncancel()
		readBufLen := 1600
		if wc != nil {
			readBufLen = wrap.MaxWire(1600)
		}
		buf := make([]byte, readBufLen)
		plain := make([]byte, 1600)
		for {
			n, _, err1 := relayConn.ReadFrom(buf)
			if err1 != nil {
				return
			}
			addr1 := internalPipeAddr.Load()
			if addr1 == nil {
				continue
			}

			if addr, ok := addr1.(net.Addr); ok {
				payload := buf[:n]
				if wc != nil {
					m, wrapErr := wc.Unwrap(payload, plain)
					if wrapErr != nil {
						log.Printf("[STREAM %d] UNWRAP failed: %v (n=%d)", streamID, wrapErr, n)
						continue
					}
					payload = plain[:m]
				}
				st.AddRx(len(payload))
				if _, err := conn2.WriteTo(payload, addr); err != nil {
					return
				}
			}
		}
	})

	wg.Wait()
	if err := relayConn.SetDeadline(time.Time{}); err != nil {
		log.Printf("Failed to clear relay deadline: %s", err)
	}
}
