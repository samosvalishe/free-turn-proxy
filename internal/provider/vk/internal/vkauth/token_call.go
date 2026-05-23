package vkauth

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchCallToken — шаг 2 цепочки: вызывает calls.getAnonymousToken и ведёт
// цикл retry captcha до получения call-токена или исчерпания всех режимов решения.
func (c *Client) fetchCallToken(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	streamID int,
	link, escapedName, token1 string,
	creds VKCredentials,
) (string, error) {
	urlAddr := fmt.Sprintf("https://api.vk.ru/method/calls.getAnonymousToken?v=5.275&client_id=%s", creds.ClientID)
	data := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s",
		link, escapedName, token1)

	for attempt := 0; ; attempt++ {
		resp, err := c.doRequest(ctx, httpClient, profile, data, urlAddr)
		if err != nil {
			return "", err
		}

		if errObj, hasErr := resp["error"].(map[string]any); hasErr {
			captchaErr := captcha.ParseError(errObj)
			if captchaErr != nil && captchaErr.IsCaptcha() {
				retryData, err := c.solveCaptcha(ctx, httpClient, profile, streamID, attempt, link, escapedName, token1, captchaErr)
				if err != nil {
					return "", err
				}
				data = retryData
				continue
			}
			return "", fmt.Errorf("VK API error: %v", errObj)
		}

		respMap, ok := resp["response"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("unexpected getAnonymousToken response: %v", resp)
		}
		token2, ok := respMap["token"].(string)
		if !ok {
			return "", fmt.Errorf("missing token in response: %v", resp)
		}
		return token2, nil
	}
}

// solveCaptcha выполняет одну попытку решения captcha и возвращает тело POST
// для следующего retry или ошибку при исчерпании всех режимов.
func (c *Client) solveCaptcha(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	streamID, attempt int,
	link, escapedName, token1 string,
	captchaErr *captcha.Error,
) (retryData string, err error) {
	solveMode, hasSolveMode := CaptchaSolveModeForAttempt(attempt, c.manualOnly)
	if !hasSolveMode {
		c.log.Warnf("[STREAM %d] [Captcha] No more solve modes available (attempt %d)", streamID, attempt+1)
		c.engageLockout(60 * time.Second)
		if c.streamsFn() == 0 {
			c.log.Errorf("[STREAM %d] [Captcha] FATAL: 0 connected streams and solve modes exhausted", streamID)
			return "", ErrFatalCaptchaNoStreams
		}
		return "", ErrCaptchaWaitRequired
	}

	var successToken string
	var captchaKey string
	var solveErr error

	switch solveMode {
	case CaptchaSolveModeAuto:
		solveFn := c.autoSolver
		if solveFn == nil {
			solveFn = DefaultAutoSolve
		}
		if captchaErr.SessionToken != "" && captchaErr.RedirectURI != "" {
			successToken, solveErr = solveFn(ctx, captchaErr, streamID, httpClient, profile)
			if solveErr != nil {
				c.log.Warnf("[STREAM %d] [Captcha] Auto captcha failed: %v", streamID, solveErr)
			}
		} else {
			solveErr = fmt.Errorf("missing fields for auto solve")
		}

	case CaptchaSolveModeManual:
		if c.manualSolve == nil {
			solveErr = fmt.Errorf("manual captcha solver not configured")
			break
		}
		c.log.Infof("[STREAM %d] [Captcha] Triggering manual captcha fallback", streamID)
		// Ручной решалке выделяется свой 3-минутный бюджет — жёсткий parent-deadline
		// не обрезает время пользователя. Отмена parent (завершение приложения)
		// всё равно propagate, горутина не переживает процесс.
		manualCtx, manualCancel := context.WithTimeout(ctx, 3*time.Minute)

		type manualRes struct {
			token string
			key   string
			err   error
		}
		resCh := make(chan manualRes, 1)
		go func() {
			t, k, e := c.manualSolve(manualCtx, captchaErr, c.dialer)
			resCh <- manualRes{t, k, e}
		}()

		select {
		case res := <-resCh:
			successToken = res.token
			captchaKey = res.key
			solveErr = res.err
			if successToken != "" || captchaKey != "" {
				if solveErr != nil {
					c.log.Debugf("[STREAM %d] [Captcha] Token received (ignoring cleanup error: %v)", streamID, solveErr)
					solveErr = nil
				}
				c.log.Infof("[STREAM %d] [Captcha] Got token from browser", streamID)
			} else if solveErr != nil {
				c.log.Warnf("[STREAM %d] [Captcha] Manual solver error: %v", streamID, solveErr)
			}
		case <-manualCtx.Done():
			if errors.Is(manualCtx.Err(), context.DeadlineExceeded) {
				solveErr = fmt.Errorf("manual captcha timed out after 3m")
			} else {
				solveErr = fmt.Errorf("manual captcha interrupted: %w", manualCtx.Err())
			}
		}
		manualCancel()
	}

	if solveErr != nil {
		c.log.Warnf("[STREAM %d] [Captcha] %s failed (attempt %d): %v",
			streamID, CaptchaSolveModeLabel(solveMode), attempt+1, solveErr)
		nextSolveMode, hasNextSolveMode := CaptchaSolveModeForAttempt(attempt+1, c.manualOnly)
		if hasNextSolveMode {
			c.log.Infof("[STREAM %d] [Captcha] Falling back to %s",
				streamID, CaptchaSolveModeLabel(nextSolveMode))
			return buildCaptchaRetryData(link, escapedName, token1, captchaErr, "", captchaKey), nil
		}
		c.engageLockout(60 * time.Second)
		if c.streamsFn() == 0 {
			c.log.Errorf("[STREAM %d] [Captcha] FATAL: 0 connected streams and manual captcha failed/timed out", streamID)
			return "", ErrFatalCaptchaNoStreams
		}
		return "", ErrCaptchaWaitRequired
	}

	if captchaErr.CaptchaAttempt == "0" || captchaErr.CaptchaAttempt == "" {
		captchaErr.CaptchaAttempt = "1"
	}
	return buildCaptchaRetryData(link, escapedName, token1, captchaErr, successToken, captchaKey), nil
}

// buildCaptchaRetryData формирует тело POST для следующей попытки captcha.
func buildCaptchaRetryData(link, escapedName, token1 string, captchaErr *captcha.Error, successToken, captchaKey string) string {
	if captchaKey != "" {
		return fmt.Sprintf(
			"vk_join_link=https://vk.com/call/join/%s&name=%s&captcha_key=%s&captcha_sid=%s&access_token=%s",
			link, escapedName, neturl.QueryEscape(captchaKey), captchaErr.CaptchaSid, token1,
		)
	}
	return fmt.Sprintf(
		"vk_join_link=https://vk.com/call/join/%s&name=%s&captcha_key=&captcha_sid=%s&is_sound_captcha=0&success_token=%s&captcha_ts=%s&captcha_attempt=%s&access_token=%s",
		link, escapedName, captchaErr.CaptchaSid, neturl.QueryEscape(successToken),
		captchaErr.CaptchaTs, captchaErr.CaptchaAttempt, token1,
	)
}
