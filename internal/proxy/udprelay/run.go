// Package udprelay реализует UDP-режим прокси: терминирует DTLS от локального
// пира (WireGuard) и ретранслирует пакеты через per-stream TURN-аллокацию
// обратно к удалённому пиру. Run — точка входа; владеет локальным listener,
// fan-in входящего dispatch и per-stream DTLS/TURN циклами.
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

// GetCredsFunc реэкспортирован из common, чтобы вызывающие не выходили за пределы импортов пакета.
type GetCredsFunc = common.GetCredsFunc

// AuthHandler — подмножество vkauth.Client, необходимое пакету. Определено как
// интерфейс, чтобы тесты могли подменять fake; prod-код всё равно импортирует
// vkauth ради sentinel-ошибок (ErrFatalCaptchaNoStreams и т.д.).
type AuthHandler interface {
	IsAuthError(err error) bool
	HandleAuthError(streamID int) bool
	ResetErrors(streamID int)
	LockoutUntilUnix() int64
}

// Params — per-stream конфигурация TURN/wrap, общая для DTLS и TURN циклов.
type Params struct {
	Host     string
	Port     string
	Link     string
	TransportUDP bool
	ObfKey  []byte
	GetCreds GetCredsFunc
}

// ErrFatal возвращается из Run, когда поток встречает условие, требующее
// завершения всего приложения (напр. ручной решатель captcha провалился без
// подключённых потоков). Вызывающий должен проверить через errors.Is и вызвать
// os.Exit сам — udprelay не вмешивается в хост-процесс.
var ErrFatal = errors.New("udprelay: fatal error")

// Deps объединяет всё, что циклы берут из хост-процесса. Атомики принадлежат
// Run и экспонированы здесь, чтобы DTLSLoop/TURNLoop могли разделять их при
// прямом вызове (Run подключает их автоматически).
type Deps struct {
	DTLSDialer       *dtlsdial.Dialer
	Auth             AuthHandler
	Log              logx.Logger
	ActiveLocalPeer  *atomic.Value
	ConnectedStreams *atomic.Int32
	// fatalCh — внутренний сигнальный канал; устанавливается Run, пишется
	// TURNLoop, читается Run для проброса фатальной ошибки наверх.
	fatalCh chan error
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Run — точка входа UDP-режима. Биндит listenAddr, распределяет входящие пакеты
// в общую очередь и запускает numStreams пар (DTLSLoop, TURNLoop).
// connectedStreams принадлежит вызывающему (vkauth читает через StreamsAlive)
// и инкрементируется/декрементируется в oneTURN.
// Возвращается после выхода всех потоков (т.е. при отмене ctx).
// При фатальном captcha-условии возвращает ErrFatal — вызывающий делает os.Exit
// без вмешательства udprelay в хост-процесс.
func Run(ctx context.Context, dtlsDialer *dtlsdial.Dialer, auth AuthHandler, logger logx.Logger, connectedStreams *atomic.Int32, params *Params, peer *net.UDPAddr, listenAddr string, numStreams int) error {
	listenConn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", listenAddr)
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

	// runCtx отменяется при обнаружении фатальной ошибки (через fatalCh),
	// распространяя отмену во все потоковые циклы без необходимости хранить
	// ссылку на cancel-функцию хост-процесса.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	inboundChan := make(chan *Packet, inboundQueueCap)
	wg := sync.WaitGroup{}
	wg.Go(func() {
		runListener(runCtx, listenConn, &activeLocalPeer, inboundChan)
	})
	t := time.Tick(200 * time.Millisecond)

	// Поток 1 получает okchan для сигнализации о первом успешном handshake в лог.
	// Все потоки стартуют одновременно — нет барьера между потоком 1 и остальными,
	// поэтому медленный DTLS handshake потока 1 не задерживает потоки 2..N.
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

	// При фатальной ошибке отменяем остальные горутины и пробрасываем наверх.
	// watcherDone синхронизирует watcher-горутину с возвратом Run, обеспечивая
	// happens-after между store и load fatalErr.
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
