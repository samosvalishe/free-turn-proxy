package vk

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"
	manualcaptcha "github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha/manual"
)

// iosManualURL — адрес локального прокси manual-captcha, который поднимает
// SolveViaProxy. Должен совпадать с портом из пакета manual (captchaListenPort).
// Приложение наводит на него WebView, пока пользователь решает captcha.
const iosManualURL = "http://127.0.0.1:8765"

// iosManualMu сериализует ручное решение captcha: пользователю показываем
// одновременно только одно окно, даже если в captcha упёрлись сразу несколько
// стримов/кредов.
var iosManualMu sync.Mutex

// IOSManualSolver строит ManualSolverFunc для мобильного приложения. Извлечение
// success_token переиспользует manualcaptcha.SolveViaProxy — ту же логику, что и
// десктопный CLI (локальный прокси сам ловит токен из ответов VK). show
// вызывается с URL прокси перед показом captcha (приложение должно открыть
// WebView на этот адрес), hide — после завершения, чтобы закрыть окно.
//
// Функция предназначена только для ios-биндинга и нигде в библиотеке/CLI не
// вызывается; поведение vk-провайдера для прочих вызывающих не меняется.
func IOSManualSolver(show func(url string), hide func()) ManualSolverFunc {
	return func(ctx context.Context, e *captcha.Error, dialer net.Dialer) (string, string, error) {
		if e.RedirectURI == "" {
			return "", "", fmt.Errorf("manual captcha: no redirect_uri")
		}

		// Один WebView за раз: SolveViaProxy слушает фиксированный порт, да и
		// показывать пользователю несколько captcha разом смысла нет.
		iosManualMu.Lock()
		defer iosManualMu.Unlock()

		if show != nil {
			show(iosManualURL)
		}
		if hide != nil {
			defer hide()
		}

		token, err := manualcaptcha.SolveViaProxy(ctx, e.RedirectURI, dialer)
		return token, "", err
	}
}
