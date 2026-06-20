// Package ios экспортирует минимальный API для gomobile bind (iOS).
// Все экспортированные функции используют только примитивные типы — ограничение gomobile.
package ios

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
)

// State — состояние подключения.
// gomobile генерирует константы IosStateXxx.
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateError      = "error"
)

// Snapshot — единый консистентный снимок состояния сессии для UI: и стадия
// подключения, и статистика трафика. gomobile bind транслирует в ObjC-класс
// IosSnapshot. Один GetState() на тик заменяет прежние GetStatus/GetStats/
// IsRunning — UI получает согласованный срез без гонок порядка чтения.
type Snapshot struct {
	State   string // idle | connecting | connected | error
	Streams int    // подключённых TURN-потоков прямо сейчас
	Total   int    // целевое число потоков
	ErrMsg  string // непустой при State == error
	TxTotal int64  // всего отправлено байт
	RxTotal int64  // всего получено байт
	TxRate  int64  // текущая скорость отправки, байт/с
	RxRate  int64  // текущая скорость получения, байт/с
}

// statusInfo — внутренний снимок стадии подключения (без статистики).
type statusInfo struct {
	state   string
	streams int
	total   int
	errMsg  string
}

// connectTimeout — сколько ждём первый успешный стрим. Если за это время ни
// один поток так и не поднялся (connectedStreams не стал > 0), считаем, что
// все стримы упали, и переходим в error вместо вечного connecting. Ошибка
// одного стрима не страшна — пока хоть один живой, таймаут не срабатывает.
const connectTimeout = 15 * time.Second

var (
	mu         sync.Mutex
	cancelFn   context.CancelFunc
	statusVal  atomic.Value // *statusInfo
	running    atomic.Bool
	sessionGen atomic.Int64 // номер текущей сессии; растёт на каждый Start/Stop
)

func setStatus(s *statusInfo) { statusVal.Store(s) }

// clampToInt64 насыщает uint64 до int64: gomobile экспортирует только int64,
// а счётчики байт — uint64. Реальный трафик до math.MaxInt64 не доходит, но
// насыщение делает конверсию безопасной от переполнения.
func clampToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// GetState возвращает текущий снимок состояния сессии: стадию подключения
// плюс статистику трафика, собранные на чтении из внутренних атомиков.
func GetState() *Snapshot {
	st, _ := statusVal.Load().(*statusInfo)
	if st == nil {
		st = &statusInfo{state: StateIdle}
	}
	tx, rx := stats.GlobalCounters()
	return &Snapshot{
		State:   st.state,
		Streams: st.streams,
		Total:   st.total,
		ErrMsg:  st.errMsg,
		TxTotal: clampToInt64(tx),
		RxTotal: clampToInt64(rx),
		TxRate:  globalTxRate.Load(),
		RxRate:  globalRxRate.Load(),
	}
}

// Start запускает прокси-клиент.
//
//   - link:      ссылка VK https://vk.com/call/join/... или freeturn:// URI
//   - peer:      адрес freeturn-сервера на VPS, например "1.2.3.4:56000"
//   - dns:       DNS-серверы через запятую, например "8.8.8.8"; "" — авто
//   - listen:    локальный bind, например "127.0.0.1:9000"; "" — дефолт
//   - transport: "tcp" или "udp"; "" — дефолт tcp
//   - obfKey:    ключ обфускации rtpopus (64 hex символа); "" — без обфускации.
//     Должен совпадать с -obf-key сервера.
func Start(link, peer, dns, listen, transport, obfKey string) error {
	mu.Lock()
	defer mu.Unlock()

	if running.Load() {
		return fmt.Errorf("already running")
	}
	if listen == "" {
		listen = "127.0.0.1:9000"
	}

	if transport == "" {
		transport = "tcp"
	}
	args := []string{"-link", link, "-peer", peer, "-listen", listen, "-transport", transport}
	if dns != "" {
		args = append(args, "-dns-servers", dns)
	}
	if obfKey != "" {
		args = append(args, "-obf-profile", "rtpopus", "-obf-key", obfKey)
	}

	cfg, err := config.ParseClient(args, &bytes.Buffer{})
	if err != nil {
		return err
	}

	ClearLogs()
	stats.StartGlobalCount()

	ctx, cancel := context.WithCancel(context.Background())
	cancelFn = cancel
	running.Store(true)
	gen := sessionGen.Add(1)
	setStatus(&statusInfo{state: StateConnecting, total: cfg.TURN.N})
	go startRateMeter(ctx)

	go func() {
		// Терминальный статус выставляем ровно один раз — здесь. Сначала
		// отменяем контекст и ждём горутину-счётчик, чтобы она не затёрла
		// финальный статус своим connecting/connected. Статус ставим ДО сброса
		// running: тогда UI, увидев IsRunning()==false, гарантированно прочитает
		// уже актуальный error/idle.
		// finalErr — ошибка из udprelay.Run; watchdogErr — «не поднялся ни один
		// стрим за connectTimeout». Читаются в defer после counters.Wait(), что
		// даёт happens-after для записи watchdogErr из горутины-счётчика.
		var finalErr error
		var watchdogErr error
		var counters sync.WaitGroup

		defer func() {
			cancel()
			counters.Wait()
			mu.Lock()
			defer mu.Unlock()
			// Сессию мог уже сменить Stop() или новый Start() — тогда общий стейт
			// принадлежит им, и трогать его нельзя, иначе затрём актуальный статус.
			if sessionGen.Load() != gen {
				return
			}
			stats.StopGlobalCount()
			err := finalErr
			if err == nil {
				err = watchdogErr
			}
			if err != nil {
				setStatus(&statusInfo{state: StateError, total: cfg.TURN.N, errMsg: err.Error()})
			} else {
				setStatus(&statusInfo{state: StateIdle})
			}
			running.Store(false)
			cancelFn = nil
		}()

		logger := &bufLogger{debug: false}
		dnsdial.SetLogger(logger)

		if cfg.DNS.Servers != nil {
			dnsdial.SetUDPDNSServers(cfg.DNS.Servers)
		}
		appDialer := dnsdial.AppDialer(cfg.DNS.Mode)
		dnsdial.InstallGlobalResolver(cfg.DNS.Mode)

		var connectedStreams atomic.Int32

		// nil solver — manual captcha через браузер на мобильном недоступна.
		// При captcha-блоке поток упадёт с ErrFatalNoStreams; пользователь
		// должен переподключиться позже.
		prov, err := vk.New(vk.Config{
			// ParseClient гарантирует непустой Links (иначе вернул бы ошибку выше).
			// Мобайл передаёт одну ссылку, провайдер берёт один join-код.
			Link:            cfg.VK.Links[0],
			Dialer:          appDialer,
			StreamsPerCache: cfg.VK.StreamsPerCred,
			StreamsAlive:    connectedStreams.Load,
			Log:             logger,
		}, nil)
		if err != nil {
			finalErr = fmt.Errorf("provider: %v", err)
			return
		}

		peerAddr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Peer)
		if err != nil {
			finalErr = fmt.Errorf("bad peer addr: %v", err)
			return
		}

		dtlsDialer := &dtlsdial.Dialer{
			HandshakeTimeout: 20 * time.Second,
			HandshakeSem:     make(chan struct{}, 3),
		}

		udpParams := &udprelay.Params{
			Host:         cfg.TURN.Host,
			Port:         cfg.TURN.Port,
			TransportUDP: cfg.TURN.TransportUDP,
			Profile:      string(cfg.Obf.Profile),
			ObfKey:       cfg.Obf.Key,
			ObfTiming:    cfg.Obf.Timing,
			GetCreds: udprelay.GetCredsFunc(func(ctx context.Context, streamID int) (string, string, []string, error) {
				c, err := prov.GetCredentials(ctx, streamID)
				if err != nil {
					return "", "", nil, err
				}
				return c.User, c.Pass, c.ServerAddrs, nil
			}),
			ClientID: "ios-mobile",
		}

		// Обновляем счётчик потоков по мере подключения и сторожим стартовый
		// таймаут: если за connectTimeout ни один стрим не поднялся — считаем
		// подключение неуспешным, пишем watchdogErr и гасим всё через cancel.
		counters.Add(1)
		go func() {
			defer counters.Done()
			deadline := time.Now().Add(connectTimeout)
			everConnected := false
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					n := connectedStreams.Load()
					if n > 0 {
						everConnected = true
					}
					state := StateConnecting
					if n > 0 {
						state = StateConnected
					}
					setStatus(&statusInfo{state: state, streams: int(n), total: cfg.TURN.N})

					if !everConnected && time.Now().After(deadline) {
						watchdogErr = fmt.Errorf("не удалось подключиться: ни один поток не поднялся за %s — проверьте ссылку на звонок и адрес сервера (подробности в логах)", connectTimeout)
						cancel()
						return
					}
				}
			}
		}()

		if err := udprelay.Run(ctx, dtlsDialer, prov, logger, &connectedStreams, udpParams, peerAddr, cfg.Proxy.Listen, cfg.TURN.N); err != nil {
			if !errors.Is(ctx.Err(), context.Canceled) {
				finalErr = err
			}
		}
	}()

	return nil
}

// Stop останавливает прокси-клиент.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if cancelFn != nil {
		cancelFn()
		cancelFn = nil
		// Освобождаем сессию сразу, не дожидаясь догасания фоновой горутины.
		// Бамп поколения гарантирует, что её defer не затрёт стейт после нас и
		// не помешает немедленному переподключению.
		running.Store(false)
		sessionGen.Add(1)
		stats.StopGlobalCount()
	}
	setStatus(&statusInfo{state: StateIdle})
}
