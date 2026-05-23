// Package static — провайдер фиксированных TURN-реквизитов из CLI-флагов.
//
// Используется для подключения к любому совместимому TURN-серверу (coturn,
// metered, self-hosted) с долгоживущими credentials. Без сетевого control-plane:
// никаких captcha, throttling, lockout. Проверяет, что абстракция provider.Provider
// действительно достаточна для не-VK источников.
package static

import (
	"context"
	"fmt"
	"strings"

	"github.com/samosvalishe/btp/internal/provider"
)

// Config — параметры static-провайдера.
type Config struct {
	User string // TURN username (обязателен)
	Pass string // TURN password (обязателен)
	Addr string // TURN server host:port (обязателен)
}

// Provider реализует provider.Provider, всегда возвращает одни и те же реквизиты.
type Provider struct {
	creds provider.Credentials
}

// New создаёт static-провайдер. Валидирует обязательные поля.
func New(cfg Config) (*Provider, error) {
	if cfg.User == "" {
		return nil, fmt.Errorf("static: empty User")
	}
	if cfg.Pass == "" {
		return nil, fmt.Errorf("static: empty Pass")
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("static: empty Addr")
	}
	if !strings.Contains(cfg.Addr, ":") {
		return nil, fmt.Errorf("static: Addr must be host:port, got %q", cfg.Addr)
	}
	return &Provider{
		creds: provider.Credentials{
			User:       cfg.User,
			Pass:       cfg.Pass,
			ServerAddr: cfg.Addr,
		},
	}, nil
}

// GetCredentials возвращает фиксированные реквизиты.
func (p *Provider) GetCredentials(_ context.Context, _ int) (provider.Credentials, error) {
	return p.creds, nil
}

// IsAuthError — для static-провайдера auth-ошибки не приводят к ротации
// реквизитов (их просто негде взять). Возвращаем false всегда: pipeline сам
// сделает retry с тем же кешем.
func (*Provider) IsAuthError(error) bool { return false }

// HandleAuthError ничего не делает (нет кеша для инвалидации).
func (*Provider) HandleAuthError(int) bool { return false }

// ResetErrors ничего не делает.
func (*Provider) ResetErrors(int) {}

// BackoffUntilUnix всегда 0 — backoff не реализован.
func (*Provider) BackoffUntilUnix() int64 { return 0 }

// Name реализует provider.Provider.
func (*Provider) Name() string { return "static" }
