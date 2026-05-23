package vkauth

import (
	"context"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// CaptchaSolveMode — выбор между автоматическим решением и ручным браузерным fallback.
type CaptchaSolveMode int

const (
	CaptchaSolveModeAuto CaptchaSolveMode = iota
	CaptchaSolveModeManual
)

// CaptchaSolveModeForAttempt выбирает решалку для конкретной попытки retry.
// Возвращает (mode, true) если режим доступен, (_, false) если исчерпан.
func CaptchaSolveModeForAttempt(attempt int, manualOnly bool) (CaptchaSolveMode, bool) {
	if manualOnly {
		return CaptchaSolveModeManual, attempt == 0
	}
	switch attempt {
	case 0:
		return CaptchaSolveModeAuto, true
	case 1:
		return CaptchaSolveModeManual, true
	}
	return 0, false
}

func CaptchaSolveModeLabel(mode CaptchaSolveMode) string {
	switch mode {
	case CaptchaSolveModeAuto:
		return "auto captcha"
	case CaptchaSolveModeManual:
		return "manual captcha"
	default:
		return "captcha"
	}
}

// AutoSolveFunc возвращает success_token для captcha через in-page widget flow.
// Реализации обязаны соблюдать отмену ctx.
type AutoSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID int,
	http tlsclient.HttpClient,
	profile browserprofile.Profile,
) (token string, err error)

// ManualSolveFunc открывает локальный браузерный fallback. Возвращает либо
// success_token (token != ""), либо captcha_key — в зависимости от пути
// ошибки VK.
type ManualSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	dialer net.Dialer,
) (token, key string, err error)
