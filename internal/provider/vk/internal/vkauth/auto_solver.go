package vkauth

import (
	"context"
	"fmt"

	"github.com/samosvalishe/btp/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/btp/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// DefaultAutoSolve запускает in-page widget flow. Загружает сохранённый профиль
// браузера, если доступен, — капча видит стабильные fingerprints на этапе
// загрузки страницы и POST-запроса.
func DefaultAutoSolve(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
) (string, error) {
	log := captcha.Log
	log.Infof("[STREAM %d] [Captcha] Solving captcha...", streamID)

	if captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri for auto-solve")
	}
	if captchaErr.RedirectURI == "" {
		return "", fmt.Errorf("no redirect_uri for auto-solve")
	}

	var savedProfile *browserprofile.Saved
	if sp, err := browserprofile.Load(); err == nil {
		log.Infof("[STREAM %d] [Captcha] Using saved real browser profile", streamID)
		savedProfile = sp
		profile = sp.Profile
	}

	successToken, err := captcha.Solve(ctx, captchaErr, streamID, client, profile, savedProfile, log)
	if err != nil {
		return "", err
	}
	log.Infof("[STREAM %d] [Captcha] solver succeeded", streamID)
	return successToken, nil
}
