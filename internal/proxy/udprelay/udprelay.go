// Package udprelay implements the UDP-mode proxy loop: it terminates DTLS from a
// local peer (WireGuard) and relays its packets through a per-stream TURN
// allocation back to a remote peer. Run is the entrypoint; it owns the local
// listener, the inbound dispatch fan-in, and the per-stream DTLS/TURN loops.
package udprelay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/client/vkauth"
	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/common"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/transport/dtlsdial"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/srtpmimicry"
	"github.com/cbeuw/connutil"
)

// Packet is a pooled UDP datagram carried from the listener to the per-stream
// DTLS worker. N is the populated prefix of Data.
type Packet struct {
	Data []byte
	N    int
}

// packetPool reuses Packet buffers across the inbound hot path. Buffer size
// matches the 2048-byte default the listener loop expects.
var packetPool = sync.Pool{
	New: func() any { return &Packet{Data: make([]byte, 2048)} },
}

// GetCredsFunc is an alias of common.GetCredsFunc kept for cmd/main wiring
// stability.
type GetCredsFunc = common.GetCredsFunc

// AuthHandler is the subset of vkauth.Client this package needs. Keeping it as
// an interface lets internal/proxy/udprelay avoid importing internal/client/vkauth.
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

// Deps groups everything the loops need from the host process. The atomics
// are owned by Run and exposed here so DTLSLoop/TURNLoop can share them when
// called directly (Run wires them automatically).
type Deps struct {
	DTLSDialer       *dtlsdial.Dialer
	Auth             AuthHandler
	Log              logx.Logger
	ActiveLocalPeer  *atomic.Value
	ConnectedStreams *atomic.Int32
	AppCancel        func()
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
func Run(ctx context.Context, dtlsDialer *dtlsdial.Dialer, auth AuthHandler, logger logx.Logger, connectedStreams *atomic.Int32, appCancel context.CancelFunc, params *Params, peer *net.UDPAddr, listenAddr string, numStreams int) {
	listenConn, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		log.Panicf("Failed to listen: %s", err)
	}
	context.AfterFunc(ctx, func() {
		if closeErr := listenConn.Close(); closeErr != nil {
			log.Printf("Failed to close local connection: %s", closeErr)
		}
	})

	if numStreams <= 0 {
		numStreams = 1
	}

	var activeLocalPeer atomic.Value
	deps := &Deps{
		DTLSDialer:       dtlsDialer,
		Auth:             auth,
		Log:              logger,
		ActiveLocalPeer:  &activeLocalPeer,
		ConnectedStreams: connectedStreams,
		AppCancel:        appCancel,
	}

	inboundChan := make(chan *Packet, inboundQueueCap)
	go runListener(ctx, listenConn, &activeLocalPeer, inboundChan)

	wg := sync.WaitGroup{}
	t := time.Tick(200 * time.Millisecond)

	okchan := make(chan struct{})
	connchan := make(chan net.PacketConn)
	wg.Go(func() {
		DTLSLoop(ctx, deps, peer, listenConn, inboundChan, connchan, okchan, 1)
	})
	wg.Go(func() {
		TURNLoop(ctx, deps, params, peer, connchan, t, 1)
	})

	select {
	case <-okchan:
	case <-ctx.Done():
	}

	for i := 1; i < numStreams; i++ {
		cchan := make(chan net.PacketConn)
		streamID := i
		wg.Go(func() {
			DTLSLoop(ctx, deps, peer, listenConn, inboundChan, cchan, nil, streamID)
		})
		wg.Go(func() {
			TURNLoop(ctx, deps, params, peer, cchan, t, streamID)
		})
	}

	wg.Wait()
}

const inboundQueueCap = 2000

// runListener reads packets from listenConn, refreshes the active-peer cache,
// and posts each packet to inboundChan. Packets are dropped when the channel
// is full to keep the read loop wait-free.
func runListener(ctx context.Context, listenConn net.PacketConn, activeLocalPeer *atomic.Value, inboundChan chan<- *Packet) {
	// Pointer-cache for the last seen local peer addr. Avoids the
	// per-packet addr.String() allocation pair on the hot WG ingest path:
	// most packets come from the same UDPAddr instance, so a pointer
	// equality check covers the fast path. The slow path (new instance
	// from ReadFrom for the same ip:port) does one String compare and
	// then refreshes the cache.
	var lastAddr net.Addr
	var lastAddrStr string
	for {
		if ctx.Err() != nil {
			return
		}
		pktIface := packetPool.Get()
		pkt := pktIface.(*Packet) //nolint:errcheck // pool New always returns *Packet
		nRead, addr, err := listenConn.ReadFrom(pkt.Data)
		if err != nil {
			return
		}

		if addr != lastAddr {
			s := addr.String()
			if s != lastAddrStr {
				activeLocalPeer.Store(addr)
				lastAddrStr = s
			}
			lastAddr = addr
		}

		pkt.N = nRead

		select {
		case inboundChan <- pkt:
		default:
			packetPool.Put(pkt)
		}
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
			// During captcha lockout the handshake deadline fires before
			// auth retries can succeed; back off briefly to avoid a tight
			// retry spin until the lockout clears.
			if err != nil && time.Now().Unix() < deps.Auth.LockoutUntilUnix() && errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(1+rand.Intn(2)) * time.Second):
				}
				continue
			}
			if err != nil {
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
				if errors.Is(err, vkauth.ErrFatalCaptchaNoStreams) {
					log.Printf("[STREAM %d] Fatal manual captcha error. Shutting down application.", streamID)
					if deps.AppCancel != nil {
						deps.AppCancel()
					}
					return
				}
				if errors.Is(err, vkauth.ErrCaptchaWaitRequired) {
					if !errors.Is(err, vkauth.ErrLockoutActive) {
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
	// TURNLoop may restart oneTURN several times within a single DTLS lifetime,
	// re-reading conn2 on each restart; keep publishing until the DTLS attempt
	// itself ends.
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
		return fmt.Errorf("failed to connect DTLS: %w", err1)
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
				packetPool.Put(pkt)
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
	stream, err1 := common.DialTURN(ctx, params.Host, params.Port, params.UDP, peer, params.Link, streamID, params.GetCreds)
	if err1 != nil {
		if deps.Auth.IsAuthError(err1) {
			deps.Auth.HandleAuthError(streamID)
		}
		err = err1
		return
	}
	relayConn := stream.Relay
	deps.log().Debugf("[STREAM %d] TURN server IP: %s", streamID, stream.ServerUDPAddr.IP)

	deps.Auth.ResetErrors(streamID)

	deps.ConnectedStreams.Add(1)
	defer func() {
		deps.ConnectedStreams.Add(-1)
		if cerr := stream.Close(); cerr != nil {
			err = fmt.Errorf("failed to close TURN stream: %s", cerr)
		}
	}()

	if deps.log().DebugEnabled() {
		log.Printf("[STREAM %d] relayed-address=%s", streamID, relayConn.LocalAddr().String())
	}

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)
	st := stats.New(deps.log().DebugEnabled())
	go st.LogEvery(turnctx, deps.log().Debugf, fmt.Sprintf("[STREAM %d] TURN", streamID), "to-turn", "from-turn")

	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			log.Printf("Failed to set relay deadline: %s", err)
		}
	})
	var internalPipeAddr atomic.Value
	wc, wcErr := common.NewClientWrap(params.WrapKey)
	if wcErr != nil {
		log.Printf("[STREAM %d] WRAP init failed: %v", streamID, wcErr)
		turncancel()
		return
	}

	go func() {
		defer turncancel()
		buf := make([]byte, 1600)
		var wireBuf []byte
		if wc != nil {
			wireBuf = make([]byte, srtpmimicry.MaxWire(len(buf)))
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
			readBufLen = srtpmimicry.MaxWire(1600)
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
