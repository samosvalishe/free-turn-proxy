package udprelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/samosvalishe/btp/internal/client/vkauth"
	"github.com/samosvalishe/btp/internal/proxy/common"
	"github.com/samosvalishe/btp/internal/stats"
	"github.com/samosvalishe/btp/internal/wire/srtpmimicry"
)

// DTLSLoop поддерживает единственное DTLS-подключение для streamID, перезапуская
// его при сбое с backoff 10-30s (пропускается при активной captcha-блокировке,
// если предыдущая ошибка — дедлайн). connchan получает свежую половину
// AsyncPacketPipe на каждой попытке; okchan (non-nil только для потока 1)
// сигнализирует о первом успешном handshake.
func DTLSLoop(ctx context.Context, deps *Deps, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := oneDTLS(ctx, deps, peer, listenConn, inboundChan, connchan, okchan, streamID)
			// При captcha-блокировке дедлайн handshake срабатывает раньше,
			// чем auth-retry успевает отработать; делаем краткий backoff,
			// чтобы не крутиться в tight spin до снятия блокировки.
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

// TURNLoop ведёт половину TURN-аллокации. Ждёт свежий conn2 от DTLS-цикла,
// тормозит через t (глобальный тик 200ms), выполняет одну TURN-сессию
// и реагирует на FATAL_CAPTCHA / CAPTCHA_WAIT_REQUIRED соответственно.
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
			c := make(chan error, 1)
			go oneTURN(ctx, deps, params, peer, conn2, streamID, c)

			var err error
			select {
			case err = <-c:
			case <-ctx.Done():
				return
			}
			if err != nil {
				if errors.Is(err, vkauth.ErrFatalCaptchaNoStreams) {
					deps.log().Errorf("[STREAM %d] Fatal manual captcha error. Shutting down application.", streamID)
					select {
					case deps.fatalCh <- fmt.Errorf("%w: %w", ErrFatal, err):
					default:
					}
					return
				}
				if errors.Is(err, vkauth.ErrCaptchaWaitRequired) {
					if !errors.Is(err, vkauth.ErrLockoutActive) {
						deps.log().Warnf("[STREAM %d] Backing off for 60 seconds to avoid IP ban", streamID)
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
					deps.log().Errorf("[STREAM %d] %s", streamID, err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
			}
		}
	}
}

func oneDTLS(ctx context.Context, deps *Deps, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) error {
	select {
	case <-time.After(time.Duration(rand.Intn(400)+100) * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}

	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()

	conn1, conn2 := connutil.AsyncPacketPipe()
	// TURNLoop может перезапускать oneTURN несколько раз в рамках одного DTLS
	// соединения, каждый раз перечитывая conn2; публикуем до завершения DTLS.
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
			deps.log().Errorf("[STREAM %d] failed to close DTLS connection: %s", streamID, closeErr)
		}
		deps.log().Infof("[STREAM %d] Closed DTLS connection", streamID)
	}()
	deps.log().Infof("[STREAM %d] Established DTLS connection", streamID)

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
			deps.log().Warnf("[STREAM %d] SetDeadline failed: %v", streamID, err)
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		for {
			select {
			case <-dtlsctx.Done():
				return
			case pkt := <-inboundChan:
				_, werr := dtlsConn.Write(pkt.Data[:pkt.N])
				packetPool.Put(pkt)
				if werr != nil {
					return
				}
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
						deps.log().Errorf("[STREAM %d] failed to forward packet to local peer: %v", streamID, err)
					}
				}
			}
		}
	})

	wg.Wait()
	if err := dtlsConn.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("[STREAM %d] Failed to clear DTLS deadline: %s", streamID, err)
	}
	return nil
}

func oneTURN(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	var err error
	defer func() { c <- err }()
	select {
	case <-time.After(time.Duration(rand.Intn(400)+100) * time.Millisecond):
	case <-ctx.Done():
		err = ctx.Err()
		return
	}
	stream, err1 := common.DialTURN(ctx, params.Host, params.Port, params.TransportUDP, peer, params.Link, streamID, params.GetCreds)
	if err1 != nil {
		if deps.Auth.IsAuthError(err1) {
			deps.Auth.HandleAuthError(streamID)
		}
		err = err1
		return
	}
	relayConn := stream.Relay
	deps.log().Debugf("[STREAM %d] TURN server IP: %s", streamID, stream.ServerUDPAddr.IP)

	// Инкремент до ResetErrors — конкурентные наблюдатели HandleAuthError видят
	// поток подключённым до сброса счётчика ошибок.
	deps.ConnectedStreams.Add(1)
	deps.Auth.ResetErrors(streamID)

	defer func() {
		deps.ConnectedStreams.Add(-1)
		if cerr := stream.Close(); cerr != nil {
			err = fmt.Errorf("failed to close TURN stream: %s", cerr)
		}
	}()

	deps.log().Debugf("[STREAM %d] relayed-address=%s", streamID, relayConn.LocalAddr().String())

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)
	st := stats.New(deps.log().DebugEnabled())
	go st.LogEvery(turnctx, deps.log().Debugf, fmt.Sprintf("[STREAM %d] TURN", streamID), "to-turn", "from-turn")

	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			deps.log().Errorf("Failed to set relay deadline: %s", err)
		}
	})
	var internalPipeAddr atomic.Value
	wc, wcErr := common.NewClientWrap(params.ObfKey)
	if wcErr != nil {
		deps.log().Errorf("[STREAM %d] WRAP init failed: %v", streamID, wcErr)
		turncancel()
		return
	}

	wg.Go(func() {
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
					deps.log().Errorf("[STREAM %d] WRAP failed: %v", streamID, wrapErr)
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
	})

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
						deps.log().Errorf("[STREAM %d] UNWRAP failed: %v (n=%d)", streamID, wrapErr, n)
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
		deps.log().Errorf("Failed to clear relay deadline: %s", err)
	}
}
