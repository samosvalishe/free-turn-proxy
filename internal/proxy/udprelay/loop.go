package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/shape"
)

// errPairRecycled - пару свернул TURN-цикл (аллокация мертва), а не сеть: сетевой
// backoff тут только удлиняет простой.
var errPairRecycled = errors.New("udprelay: stream pair recycled")

const (
	quotaBackoff       = 15 * time.Second
	quotaBackoffJitter = 15 * time.Second

	pipeBufLimit = 256 << 10
)

// streamPair связывает DTLS-сессию с аллокацией, поверх которой она поднята. Смерть
// аллокации обязана ронять DTLS: у новой аллокации другой relayed-адрес, а миграция
// адреса по DTLS Connection ID под obf-профилем на сервере не работает - сервер примет
// такие записи за новую сессию и будет ждать от них handshake.
type streamPair struct {
	pipe   net.PacketConn
	cancel context.CancelFunc
}

// DTLSLoop поддерживает и перезапускает DTLS-соединение для указанного streamID.
func DTLSLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- streamPair, okchan chan<- struct{}, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := oneDTLS(ctx, deps, params, peer, listenConn, inboundChan, connchan, okchan, streamID)
			// Пара пересоздаётся под новую аллокацию - TURN-цикл уже держит свою паузу.
			if errors.Is(err, errPairRecycled) {
				continue
			}
			if err != nil && time.Now().Unix() < deps.Auth.BackoffUntilUnix() && errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(1+randx.Intn(2)) * time.Second):
				}
				continue
			}
			if err != nil {
				wait := time.Duration(10+randx.Intn(20)) * time.Second
				deps.log().Warnf("[STREAM %d] DTLS: %v - повтор через %s", streamID, err, wait)
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
	}
}

// TURNLoop управляет жизненным циклом одной TURN-аллокации.
func TURNLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, connchan <-chan streamPair, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case pair := <-connchan:
			c := make(chan error, 1)
			go deps.guard(func() { oneTURN(ctx, deps, params, peer, pair.pipe, streamID, c) })()

			var err error
			select {
			case err = <-c:
			case <-ctx.Done():
				return
			}
			// Аллокация кончилась - DTLS поверх неё сервер больше не адресует (см. streamPair).
			pair.cancel()
			if err != nil {
				if errors.Is(err, provider.ErrFatalNoStreams) {
					deps.log().Errorf("[STREAM %d] Fatal provider error. Shutting down application.", streamID)
					deps.fatal(err)
					return
				}
				// Квота может оставаться занятой после deallocate.
				if errors.Is(err, turndial.ErrAllocQuota) {
					wait := quotaBackoff + time.Duration(randx.Intn(int(quotaBackoffJitter/time.Second)))*time.Second
					deps.log().Warnf("[STREAM %d] квота аллокаций занята - пауза %s", streamID, wait)
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
					continue
				}
				if errors.Is(err, provider.ErrBackoffActive) {
					lockoutEnd := deps.Auth.BackoffUntilUnix()
					var sleepDuration time.Duration
					if lockoutEnd > 0 {
						sleepDuration = time.Until(time.Unix(lockoutEnd, 0))
						if sleepDuration < 0 {
							sleepDuration = 5 * time.Second
						}
					} else {
						sleepDuration = 60 * time.Second
						deps.log().Warnf("[STREAM %d] Backing off for 60 seconds (provider requests wait)", streamID)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(sleepDuration):
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

func oneDTLS(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- streamPair, okchan chan<- struct{}, streamID int) error {
	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()

	err := dtlsSession(dtlsctx, dtlscancel, deps, params, peer, listenConn, inboundChan, connchan, okchan, streamID)
	// Отменена именно пара, а не вся сессия - значит её свернул TURN-цикл.
	if err != nil && ctx.Err() == nil && dtlsctx.Err() != nil {
		return errPairRecycled
	}
	return err
}

func dtlsSession(dtlsctx context.Context, dtlscancel context.CancelFunc, deps *Deps, params *Params, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- streamPair, okchan chan<- struct{}, streamID int) error {
	select {
	case <-time.After(time.Duration(randx.Intn(400)+100) * time.Millisecond):
	case <-dtlsctx.Done():
		return dtlsctx.Err()
	}

	conn1, conn2 := connutil.LimitedAsyncPacketPipe(pipeBufLimit)
	defer func() { _ = conn1.Close() }()
	defer func() { _ = conn2.Close() }()
	// Ровно один раз: пара строго 1:1, иначе следующая аллокация села бы на DTLS, который
	// уже сворачивают. Отдаём до handshake - его пакеты идут через эту же аллокацию.
	select {
	case connchan <- streamPair{pipe: conn2, cancel: dtlscancel}:
	case <-dtlsctx.Done():
		return dtlsctx.Err()
	}
	dtlsRaw, err1 := deps.DTLSDialer.Dial(dtlsctx, conn1, peer)
	if err1 != nil {
		return fmt.Errorf("failed to connect DTLS: %w", err1)
	}
	var dtlsConn net.Conn = dtlsRaw
	defer func() {
		_ = dtlsConn.Close()
		deps.log().Debugf("[STREAM %d] Closed DTLS connection", streamID)
	}()
	deps.log().Debugf("[STREAM %d] Established DTLS connection", streamID)

	if err := clientsdb.WriteClientID(dtlsConn, params.ClientID, clientsdb.ModeUDP); err != nil {
		return fmt.Errorf("failed to write client ID: %w", err)
	}
	// Аллокация без DTLS ещё не может передавать пользовательский трафик.
	deps.ConnectedStreams.Add(1)
	defer deps.ConnectedStreams.Add(-1)
	if okchan != nil {
		select {
		case okchan <- struct{}{}:
		default:
		}
	}

	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		var buf [2048]byte
		for {
			n, err := dtlsConn.Read(buf[:])
			if err != nil {
				return
			}
			addr := deps.ActiveLocalPeer.Load()
			if addr == nil {
				continue
			}
			netAddr, ok := addr.(net.Addr)
			if !ok {
				continue
			}
			_, writeErr := listenConn.WriteTo(buf[:n], netAddr)
			if writeErr != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-dtlsctx.Done():
			return dtlsctx.Err()
		case <-forwardDone:
			return errors.New("DTLS connection closed by remote peer")
		case pkt := <-inboundChan:
			_, err := dtlsConn.Write(pkt.Data[:pkt.N])
			packetPool.Put(pkt)
			if err != nil {
				return fmt.Errorf("failed to forward packet to DTLS: %w", err)
			}
		}
	}
}

func oneTURN(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	var err error
	defer func() {
		c <- err
	}()

	stream, derr := params.Dial(ctx, streamID)
	if derr != nil {
		if deps.Auth.IsAuthError(derr) {
			deps.Auth.HandleAuthError(streamID)
		}
		err = fmt.Errorf("connect to TURN server: %w", derr)
		return
	}
	relayConn := stream.Relay
	if deps.OnTURNServer != nil {
		deps.OnTURNServer(stream.ServerUDPAddr.IP)
	}

	if params.ObfTiming > 0 {
		relayConn = shape.WrapPacketConn(relayConn, params.ObfTiming)
		deps.log().Debugf("[STREAM %d] obf-timing=%s", streamID, params.ObfTiming)
	}

	deps.Auth.ResetErrors(streamID)

	relayedAddr := relayConn.LocalAddr().String()
	deps.log().Infof("[STREAM %d] TURN allocation up: relayed=%s server=%s",
		streamID, relayedAddr, stream.ServerUDPAddr.IP)

	defer func() {
		// Освобождение аллокации логируем всегда: недошедший deallocate держит квоту VK
		// до конца её lifetime, и следующий Allocate ловит 486.
		cerr := stream.Close()
		deps.log().Infof("[STREAM %d] TURN allocation released: relayed=%s deallocate=%v",
			streamID, relayedAddr, cerr)
		if cerr != nil {
			deps.Auth.DropCredentials(streamID)
		}
	}()

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)
	defer turncancel()

	// без дедлайна relayConn.ReadFrom не проснётся на отмене turnctx - wg.Wait встанет намертво
	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			deps.log().Errorf("[STREAM %d] Failed to set relay deadline: %s", streamID, err)
		}
	})

	var internalPipeAddr atomic.Value
	obfConn, obfErr := wire.NewClientCodec(params.Profile, params.ObfKey)
	if obfErr != nil {
		deps.log().Errorf("[STREAM %d] OBF init failed: %v", streamID, obfErr)
		return
	}

	const maxPayload = 1600

	wg.Go(func() {
		select {
		case <-turnctx.Done():
		case <-stream.PermDead:
			deps.log().Warnf("[STREAM %d] TURN refresh failed - recycle allocation", streamID)
			turncancel()
		}
		// conn2 молчит, пока приложение не шлёт: без дедлайна его читатель досидел бы
		// до первого пакета, а рецикл на простое висел бы вечно (тоннель без трафика)
		if err := conn2.SetDeadline(time.Now()); err != nil {
			deps.log().Errorf("[STREAM %d] Failed to set pipe deadline: %s", streamID, err)
		}
	})

	wg.Go(func() {
		defer turncancel()
		var buf, readSlot []byte
		if obfConn != nil {
			buf = make([]byte, obfConn.MaxWire(maxPayload))
			readSlot = buf[obfConn.HeaderLen() : obfConn.HeaderLen()+maxPayload]
		} else {
			buf = make([]byte, maxPayload)
			readSlot = buf
		}
		addrStored := false
		for {
			if turnctx.Err() != nil {
				return
			}
			n, addr1, err1 := conn2.ReadFrom(readSlot)
			if err1 != nil {
				return
			}
			if turnctx.Err() != nil {
				return
			}

			if !addrStored {
				internalPipeAddr.Store(addr1)
				addrStored = true
			}

			out := readSlot[:n]
			if obfConn != nil {
				written, wErr := obfConn.WrapInPlace(buf, n)
				if wErr != nil {
					deps.log().Errorf("[STREAM %d] OBF wrap failed: %v", streamID, wErr)
					return
				}
				out = buf[:written]
			}

			written, err1 := relayConn.WriteTo(out, peer)
			if params.TrafficStats != nil {
				params.TrafficStats.AddTx(written)
			}
			if err1 != nil {
				return
			}
		}
	})

	wg.Go(func() {
		defer turncancel()
		readBufLen := maxPayload
		if obfConn != nil {
			readBufLen = obfConn.MaxWire(maxPayload)
		}
		buf := make([]byte, readBufLen)
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
				if obfConn != nil {
					p, uErr := obfConn.UnwrapInPlace(buf[:n])
					if uErr != nil {
						deps.log().Errorf("[STREAM %d] OBF unwrap failed: %v (n=%d)", streamID, uErr, n)
						continue
					}
					payload = p
				}
				if params.TrafficStats != nil {
					params.TrafficStats.AddRx(len(payload))
				}
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
	// Дедлайн снимаем до закрытия пары: пока DTLS сворачивается, его записи в pipe не
	// должны сыпать таймаутами вместо реальной причины выхода.
	if err := conn2.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("Failed to clear pipe deadline: %s", err)
	}
}
