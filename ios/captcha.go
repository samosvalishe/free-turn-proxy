package ios

import "sync"

// CaptchaPresenter — мост в UI приложения для ручного решения captcha. gomobile
// генерирует из этого интерфейса ObjC-протокол IosCaptchaPresenter, который
// реализует Swift. Вызывается из Go, когда авто-решатель captcha не справился и
// нужен ручной fallback.
//
//   - Show(url): открыть WebView на локальном прокси-адресе url, где пользователь
//     решает captcha. Метод блокирующим быть не обязан.
//   - Hide(): captcha решена или отменена — закрыть окно.
type CaptchaPresenter interface {
	Show(url string)
	Hide()
}

var (
	captchaMu        sync.RWMutex
	captchaPresenter CaptchaPresenter
)

// SetCaptchaPresenter регистрирует UI-презентер ручной captcha. Передайте nil,
// чтобы отключить ручной fallback — тогда при провале авто-captcha поток просто
// упадёт, как раньше. Вызывать один раз при старте приложения, до Start.
func SetCaptchaPresenter(p CaptchaPresenter) {
	captchaMu.Lock()
	captchaPresenter = p
	captchaMu.Unlock()
}

func currentCaptchaPresenter() CaptchaPresenter {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	return captchaPresenter
}
