// Package mobile предоставляет фасад ядра для gomobile bind (Android/iOS).
package mobile

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk"
	"github.com/samosvalishe/free-turn-proxy/internal/safego"
	"github.com/samosvalishe/free-turn-proxy/internal/session"
	"github.com/samosvalishe/free-turn-proxy/internal/statedir"
	"github.com/samosvalishe/free-turn-proxy/internal/sub"
	"github.com/samosvalishe/free-turn-proxy/internal/tunnel"
	"github.com/samosvalishe/free-turn-proxy/internal/tunnel/awg"
)

// Состояния подключения (session.Phase*).
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateError      = "error"
	// Пользователь решает капчу вручную (watchdog не считает это зависанием).
	StateCaptcha = "captcha"
)

const (
	stopTimeout        = 5 * time.Second
	tunnelCloseTimeout = 3 * time.Second
)

var ErrTunnelRequiresStartTunnel = errors.New("tunnel mode requires StartTunnel with a platform tun fd")

// ErrTCPModeRequiresStart - в tcp-режиме ядро слушает локальный порт и tun не читает:
// принятый fd остался бы установленным вхолостую, а трафик устройства - в чёрной дыре.
var ErrTCPModeRequiresStart = errors.New("proxy mode tcp requires Start without a tun fd")

var version = "dev"

func Version() string { return version }

type live struct {
	sess   *session.Session
	cancel context.CancelFunc
	done   chan struct{}
	total  int
	tunnel *tunnelParts

	rateMu sync.Mutex
	prevTx int64
	prevRx int64
	prevAt time.Time
}

func (l *live) direct() bool { return l.sess == nil }

type final struct {
	state  string
	errMsg string
	total  int
}

var (
	mu        sync.Mutex // сериализует Start/Stop
	current   atomic.Pointer[live]
	finishing atomic.Pointer[live] // сессия, завершающаяся самостоятельно, до освобождения туннеля
	lastStop  atomic.Pointer[final]
)

type Snapshot struct {
	State   string
	Streams int
	Total   int
	ErrMsg  string
	TxTotal int64
	RxTotal int64
	TxRate  int64
	RxRate  int64
}

func GetState() *Snapshot {
	if l := current.Load(); l != nil {
		if l.direct() {
			return directSnapshot(l)
		}
		s := l.sess.Snapshot()
		return &Snapshot{
			State:   string(s.Phase),
			Streams: s.Streams,
			Total:   s.Total,
			ErrMsg:  s.Err,
			TxTotal: clampToInt64(s.TxTotal),
			RxTotal: clampToInt64(s.RxTotal),
			TxRate:  s.TxRate,
			RxRate:  s.RxRate,
		}
	}
	if f := lastStop.Load(); f != nil {
		return &Snapshot{State: f.state, ErrMsg: f.errMsg, Total: f.total}
	}
	return &Snapshot{State: StateIdle}
}

// clampToInt64 насыщает uint64 до int64 для совместимости с типами gomobile.
func clampToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// SetStateDir задаёт каталог состояния. На Android обязателен: app-uid не может писать в стандартные пути.
func SetStateDir(path string) { statedir.SetDir(path) }

func DefaultConfigJSON() string { return config.DefaultClientJSON() }

func ValidateConfig(configJSON string) string {
	if _, err := config.ParseClientJSON([]byte(configJSON), ""); err != nil {
		return err.Error()
	}
	return ""
}

func ConfigToArgs(configJSON string) (string, error) {
	c, err := config.ParseClientJSON([]byte(configJSON), "")
	if err != nil {
		return "", err
	}
	return strings.Join(config.ClientArgs(c), " "), nil
}

func Start(configJSON string) error {
	mu.Lock()
	defer mu.Unlock()
	if current.Load() != nil {
		return errors.New("already running")
	}
	return startLocked(configJSON, 0, false)
}

// Restart перезапускает сессию. tunFD - дубликат дескриптора (0 для режима прокси).
func Restart(configJSON string, tunFD int) error {
	if tunFD < 0 {
		return fmt.Errorf("bad tun fd %d", tunFD)
	}
	mu.Lock()
	defer mu.Unlock()
	stopLocked()
	return startLocked(configJSON, tunFD, tunFD > 0)
}

func Stop() {
	mu.Lock()
	defer mu.Unlock()
	stopLocked()
}

// Wake форсирует пересоздание TURN-аллокаций при пробуждении устройства.
func Wake() {
	if l := current.Load(); l != nil && !l.direct() {
		l.sess.Wake()
	}
}

// Reconnect пересоздаёт TURN-аллокации, не трогая туннель и tun-дескриптор.
// В прямом режиме вместо них пересоздаются сокеты bind.
func Reconnect() {
	l := current.Load()
	if l == nil {
		return
	}
	if !l.direct() {
		l.sess.Reconnect()
		return
	}
	mu.Lock()
	defer mu.Unlock()
	l = current.Load()
	if l == nil || !l.direct() {
		return
	}
	if r, ok := l.tunnel.backend.(tunnel.Rebinder); ok {
		if err := r.Rebind(); err != nil {
			coreLog().Warnf("%v", err)
		}
	}
}

// SetDNSServers подменяет UDP-резолверы на лету, без перезапуска сессии.
func SetDNSServers(servers string) {
	dnsdial.SetUDPDNSServers(strings.Split(servers, ","))
}

func stopLocked() {
	l := current.Swap(nil)
	if l == nil {
		// Ожидание освобождения дескриптора сессией перед возможным Restart.
		if prev := finishing.Swap(nil); prev != nil {
			waitDone(prev.done, stopTimeout)
		}
		lastStop.Store(&final{state: StateIdle})
		return
	}
	finishing.CompareAndSwap(l, nil)
	l.cancel()
	waitDone(l.done, stopTimeout)
	closeTunnel(l.tunnel)
	lastStop.Store(&final{state: StateIdle})
}

// Таймаут для защиты от зависания backend.Down() в upstream amneziawg-go.
func closeTunnel(t *tunnelParts) {
	if t == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = safego.Run(coreLog(), t.close)
	}()
	waitDone(done, tunnelCloseTimeout)
}

func waitDone(done <-chan struct{}, limit time.Duration) {
	select {
	case <-done:
	case <-time.After(limit):
	}
}

func startLocked(configJSON string, tunFD int, withTunnel bool) error {
	// До backend.Up дескриптор закрывается при ошибке инициализации.
	ownFD := withTunnel
	defer func() {
		if ownFD {
			awg.CloseTUNFD(tunFD)
		}
	}()

	raw := []byte(configJSON)

	// Резолв подписки до парсинга даёт обязательный peer.
	overlayURI := ""
	if subURL := config.PeekSubURLJSON(raw); subURL != "" {
		s, err := sub.Fetch(context.Background(), subURL)
		if err != nil {
			return fmt.Errorf("failed to fetch subscription: %w", err)
		}
		if len(s.Nodes) == 0 || s.Nodes[0].URI == nil {
			return errors.New("no nodes found in subscription")
		}
		overlayURI = s.Nodes[0].URI.String()
	}

	cfg, err := config.ParseClientJSON(raw, overlayURI)
	if err != nil {
		return err
	}
	if cfg.ClientID == "" {
		return errors.New("clientId is required")
	}
	if cfg.VK.ManualCaptcha && currentSink() == nil {
		return errors.New("manual captcha requires event sink")
	}
	if cfg.Tunnel.Enabled() && !withTunnel {
		return fmt.Errorf("%w (mode=%s)", ErrTunnelRequiresStartTunnel, cfg.Tunnel.Mode)
	}
	if withTunnel && cfg.Proxy.Mode == config.ProxyModeTCP {
		return ErrTCPModeRequiresStart
	}

	logger := &sinkLogger{debug: cfg.Log.Debug, buf: sharedLogBuf}

	var parts *tunnelParts
	var tunCfg *tunnel.Config
	if cfg.Tunnel.Enabled() {
		parts, tunCfg, err = buildTunnel(cfg, logger)
		if err != nil {
			return err
		}
	}

	var captchaActive atomic.Bool
	var solver vk.ManualSolverFunc
	if currentSink() != nil {
		solver = vk.ProxyManualSolver(
			func(url string) { captchaActive.Store(true); emitCaptcha(url) },
			func() { captchaActive.Store(false); emitCaptcha("") },
		)
	}

	deps := session.Deps{
		Logger:        logger,
		Observer:      observer{},
		Solver:        solver,
		CaptchaActive: captchaActive.Load,
		Options: session.Options{
			// Сеть и квота TURN восстанавливаются дольше 15 с; остановку решает пользователь.
			Traffic: true,
		},
	}
	if parts != nil {
		deps.LocalPipe = parts.relaySide
	}

	sess, err := session.New(cfg, deps)
	if err != nil {
		parts.close()
		return err
	}

	if parts != nil {
		ownFD = false // Up забирает владение дескриптором
		if err := parts.backend.Up(tunCfg, tunFD); err != nil {
			parts.close()
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	l := &live{
		sess:   sess,
		cancel: cancel,
		done:   make(chan struct{}),
		total:  sess.Snapshot().Total,
		tunnel: parts,
	}
	current.Store(l)
	finishing.Store(nil)
	lastStop.Store(nil)

	go func() {
		defer close(l.done)
		runErr := safego.Call(logger, func() error { return sess.Run(ctx) })
		cancel()

		// Публикация до CAS для синхронизации с параллельным Stop.
		finishing.Store(l)
		if !current.CompareAndSwap(l, nil) {
			finishing.CompareAndSwap(l, nil)
			return
		}
		closeTunnel(l.tunnel)
		if runErr != nil {
			lastStop.Store(&final{state: StateError, errMsg: runErr.Error(), total: l.total})
			return
		}
		lastStop.Store(&final{state: StateIdle})
	}()

	return nil
}
