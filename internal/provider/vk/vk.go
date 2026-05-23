// Package vk — провайдер TURN-реквизитов через VK Calls API.
//
// Фасад над internal/client/vkauth: добавляет фиксированный link (адрес
// VK callroom) и адаптирует сигнатуру GetCredentials к provider.Provider.
//
// vk.Provider удовлетворяет provider.Provider и используется через generic
// pipeline (proxy/udprelay, proxy/tcpfwd) без VK-specific импортов.
package vk

import (
	"context"
	"fmt"
	"net"

	"github.com/samosvalishe/btp/internal/client/captcha"
	manualcaptcha "github.com/samosvalishe/btp/internal/client/captcha/manual"
	"github.com/samosvalishe/btp/internal/client/vkauth"
	"github.com/samosvalishe/btp/internal/logx"
	"github.com/samosvalishe/btp/internal/provider"
)

// Config — параметры VK-провайдера.
type Config struct {
	// Link — VK callroom join-код (нормализованный, без префикса URL).
	// Обязателен.
	Link string

	// Dialer для HTTP-транспорта VK API.
	Dialer net.Dialer

	// ManualOnly форсирует ручной путь captcha с первой попытки.
	ManualOnly bool

	// StreamsPerCache — делитель streamID → cacheID. <=0 → дефолт (10).
	StreamsPerCache int

	// StreamsAlive возвращает число подключённых потоков; vkauth использует
	// для решения, является ли исчерпанная captcha фатальной или throttle.
	StreamsAlive func() int32

	// Credentials — VK app_id/secret пары; nil → vkauth.DefaultCredentials.
	Credentials []vkauth.VKCredentials

	// Log — уровневый логгер. nil → no-op.
	Log logx.Logger
}

// ManualSolverFunc — кастомный решатель captcha. Если nil, vkauth не пытается
// решать ручную captcha (поток падает на ErrFatalNoStreams при auto-fail).
type ManualSolverFunc = vkauth.ManualSolveFunc

// Provider реализует provider.Provider через vkauth.Client + сохранённый link.
type Provider struct {
	link string
	auth *vkauth.Client
}

// New создаёт VK-провайдер. solver — функция ручного решения captcha
// (опциональная); если nil — manual captcha путь отключён.
func New(cfg Config, solver ManualSolverFunc) (*Provider, error) {
	if cfg.Link == "" {
		return nil, fmt.Errorf("vk: empty Link")
	}
	auth := vkauth.New(vkauth.Config{
		Credentials:     cfg.Credentials,
		Dialer:          cfg.Dialer,
		ManualOnly:      cfg.ManualOnly,
		StreamsPerCache: cfg.StreamsPerCache,
		StreamsAlive:    cfg.StreamsAlive,
		ManualSolver:    solver,
		Log:             cfg.Log,
	})
	return &Provider{link: cfg.Link, auth: auth}, nil
}

// GetCredentials реализует provider.Provider.
func (p *Provider) GetCredentials(ctx context.Context, streamID int) (provider.Credentials, error) {
	user, pass, addr, err := p.auth.GetCredentials(ctx, p.link, streamID)
	if err != nil {
		return provider.Credentials{}, err
	}
	return provider.Credentials{User: user, Pass: pass, ServerAddr: addr}, nil
}

// IsAuthError реализует provider.Provider.
func (p *Provider) IsAuthError(err error) bool { return p.auth.IsAuthError(err) }

// HandleAuthError реализует provider.Provider.
func (p *Provider) HandleAuthError(streamID int) bool { return p.auth.HandleAuthError(streamID) }

// ResetErrors реализует provider.Provider.
func (p *Provider) ResetErrors(streamID int) { p.auth.ResetErrors(streamID) }

// BackoffUntilUnix реализует provider.Provider.
func (p *Provider) BackoffUntilUnix() int64 { return p.auth.BackoffUntilUnix() }

// Name реализует provider.Provider.
func (*Provider) Name() string { return "vk" }

// SetCaptchaLoggers подключает logger в общие captcha-пакеты. Удобный шорткат
// для cmd/client/main.go, чтобы не импортировать captcha/manualcaptcha напрямую
// после полного перехода на provider/vk.
func SetCaptchaLoggers(log logx.Logger, debug bool) {
	captcha.SetLogger(log)
	manualcaptcha.SetLogger(log)
	manualcaptcha.Debug = debug
}

// DefaultManualSolver — стандартный manual-captcha solver, использует
// internal/client/captcha/manual (HTTP-сервер 127.0.0.1:8765 + браузер).
func DefaultManualSolver(ctx context.Context, e *captcha.Error, d net.Dialer) (string, string, error) {
	if e.RedirectURI != "" {
		t, err := manualcaptcha.SolveViaProxy(ctx, e.RedirectURI, d)
		return t, "", err
	}
	if e.CaptchaImg != "" {
		k, err := manualcaptcha.SolveViaHTTP(ctx, e.CaptchaImg)
		return "", k, err
	}
	return "", "", fmt.Errorf("no redirect_uri or captcha_img")
}
