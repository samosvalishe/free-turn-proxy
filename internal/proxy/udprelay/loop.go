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
	pipeBufLimit = 256 << 10
	// maxDatagramLen - бюджет локальной датаграммы (WG/AWG) по всей цепочке, как у сервера.
	maxDatagramLen = 2048
	// maxRecordLen - та же датаграмма в DTLS-записи: заголовок с CID, явный nonce, тег GCM.
	maxRecordLen = maxDatagramLen + 64
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
	for ctx.Err() == nil {
		err := oneDTLS(ctx, deps, params, peer, listenConn, inboundChan, connchan, okchan, streamID)
		var wait time.Duration
		switch {
		case err == nil, errors.Is(err, errPairRecycled):
			// Пара пересоздаётся под новую аллокацию - TURN-цикл уже держит свою паузу.
			continue
		case time.Now().Unix() < deps.Auth.BackoffUntilUnix() && errors.Is(err, context.DeadlineExceeded):
			wait = time.Duration(1+randx.Intn(2)) * time.Second
		default:
			wait = time.Duration(10+randx.Intn(20)) * time.Second
			deps.log().Warnf("[STREAM %d] DTLS: %v - повтор через %s", streamID, err, wait)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// TURNLoop управляет жизненным циклом одной TURN-аллокации.
func TURNLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, connchan <-chan streamPair, streamID int) {
	for {
		var pair streamPair
		select {
		case <-ctx.Done():
			return
		case pair = <-connchan:
		}
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
		if err == nil {
			continue
		}
		if errors.Is(err, provider.ErrFatalNoStreams) {
			deps.log().Errorf("[STREAM %d] Fatal provider error. Shutting down application.", streamID)
			deps.fatal(err)
			return
		}
		if !sleepCtx(ctx, turnRetryDelay(deps, streamID, err)) {
			return
		}
	}
}

// turnRetryDelay: пауза провайдера важнее собственной - ретрай в её середине только
// продлевает локаут. Задержки свои, не tcprelay: UDP-пара дешевле TCP-стека.
func turnRetryDelay(deps *Deps, streamID int, err error) time.Duration {
	switch {
	case errors.Is(err, turndial.ErrAllocQuota):
		wait := turndial.QuotaBackoff()
		deps.log().Warnf("[STREAM %d] квота аллокаций занята - пауза %s", streamID, wait)
		return wait
	case errors.Is(err, provider.ErrBackoffActive):
		lockoutEnd := deps.Auth.BackoffUntilUnix()
		if lockoutEnd <= 0 {
			deps.log().Warnf("[STREAM %d] Backing off for 60 seconds (provider requests wait)", streamID)
			return 60 * time.Second
		}
		if d := time.Until(time.Unix(lockoutEnd, 0)); d > 0 {
			return d
		}
		return 5 * time.Second
	default:
		deps.log().Errorf("[STREAM %d] %s", streamID, err)
		return 2 * time.Second
	}
}

// sleepCtx: false - ctx отменён раньше.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
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
	if !sleepCtx(dtlsctx, time.Duration(randx.Intn(400)+100)*time.Millisecond) {
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
	dtlsConn, err := deps.DTLSDialer.Dial(dtlsctx, conn1, peer)
	if err != nil {
		return fmt.Errorf("failed to connect DTLS: %w", err)
	}
	defer func() {
		_ = dtlsConn.Close()
		deps.log().Debugf("[STREAM %d] Closed DTLS connection", streamID)
	}()
	deps.log().Debugf("[STREAM %d] Established DTLS connection", streamID)

	if err := clientsdb.WriteClientID(dtlsctx, dtlsConn, params.ClientID, clientsdb.ModeUDP); err != nil {
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
		forwardToLocal(dtlsConn, listenConn, deps.ActiveLocalPeer)
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

func forwardToLocal(dtlsConn net.Conn, listenConn net.PacketConn, activeLocalPeer *atomic.Value) {
	var buf [maxDatagramLen]byte
	for {
		n, err := dtlsConn.Read(buf[:])
		if err != nil {
			return
		}
		addr, ok := activeLocalPeer.Load().(net.Addr)
		if !ok {
			continue
		}
		if _, err := listenConn.WriteTo(buf[:n], addr); err != nil {
			return
		}
	}
}

func oneTURN(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	var err error
	defer func() {
		c <- err
	}()

	codec, err := wire.NewClientCodec(params.Profile, params.ObfKey)
	if err != nil {
		err = fmt.Errorf("OBF init: %w", err)
		return
	}
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
	defer releaseStream(deps, stream, relayedAddr, streamID)

	turnctx, turncancel := context.WithCancel(ctx)
	defer turncancel()
	// без дедлайна relayConn.ReadFrom не проснётся на отмене turnctx - wg.Wait встанет намертво
	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			deps.log().Errorf("[STREAM %d] Failed to set relay deadline: %s", streamID, err)
		}
	})

	var pipeAddr atomic.Value
	var wg sync.WaitGroup
	wg.Go(func() { recycleOnPermDead(turnctx, turncancel, deps, stream.PermDead, conn2, streamID) })
	wg.Go(func() {
		defer turncancel()
		relayUplink(turnctx, deps, params, codec, conn2, relayConn, peer, &pipeAddr, streamID)
	})
	wg.Go(func() {
		defer turncancel()
		relayDownlink(deps, params, codec, conn2, relayConn, &pipeAddr, streamID)
	})
	wg.Wait()

	if err := relayConn.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("Failed to clear relay deadline: %s", err)
	}
	if err := conn2.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("Failed to clear pipe deadline: %s", err)
	}
}

func releaseStream(deps *Deps, stream *turndial.Stream, relayedAddr string, streamID int) {
	cerr := stream.Close()
	deps.log().Infof("[STREAM %d] TURN allocation released: relayed=%s deallocate=%v",
		streamID, relayedAddr, cerr)
	if cerr != nil {
		deps.Auth.DropCredentials(streamID)
	}
}

func recycleOnPermDead(ctx context.Context, cancel context.CancelFunc, deps *Deps, permDead <-chan struct{}, conn2 net.PacketConn, streamID int) {
	select {
	case <-ctx.Done():
	case <-permDead:
		deps.log().Warnf("[STREAM %d] TURN refresh failed - recycle allocation", streamID)
		cancel()
	}
	if err := conn2.SetDeadline(time.Now()); err != nil {
		deps.log().Errorf("[STREAM %d] Failed to set pipe deadline: %s", streamID, err)
	}
}

func relayUplink(ctx context.Context, deps *Deps, params *Params, codec wire.Codec, conn2, relayConn net.PacketConn, peer net.Addr, pipeAddr *atomic.Value, streamID int) {
	var buf, readSlot []byte
	if codec != nil {
		buf = make([]byte, codec.MaxWire(maxRecordLen))
		readSlot = buf[codec.HeaderLen() : codec.HeaderLen()+maxRecordLen]
	} else {
		buf = make([]byte, maxRecordLen)
		readSlot = buf
	}
	addrStored := false
	for ctx.Err() == nil {
		n, addr, err := conn2.ReadFrom(readSlot)
		if err != nil || ctx.Err() != nil {
			return
		}
		if !addrStored {
			pipeAddr.Store(addr)
			addrStored = true
		}

		out := readSlot[:n]
		if codec != nil {
			written, wErr := codec.WrapInPlace(buf, n)
			if wErr != nil {
				deps.log().Errorf("[STREAM %d] OBF wrap failed: %v", streamID, wErr)
				return
			}
			out = buf[:written]
		}

		written, err := relayConn.WriteTo(out, peer)
		if params.TrafficStats != nil {
			params.TrafficStats.AddTx(written)
		}
		if err != nil {
			return
		}
	}
}

func relayDownlink(deps *Deps, params *Params, codec wire.Codec, conn2, relayConn net.PacketConn, pipeAddr *atomic.Value, streamID int) {
	readBufLen := maxRecordLen
	if codec != nil {
		readBufLen = codec.MaxWire(maxRecordLen)
	}
	buf := make([]byte, readBufLen)
	for {
		n, _, err := relayConn.ReadFrom(buf)
		if err != nil {
			return
		}
		addr, ok := pipeAddr.Load().(net.Addr)
		if !ok {
			continue
		}
		payload := buf[:n]
		if codec != nil {
			p, uErr := codec.UnwrapInPlace(payload)
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
