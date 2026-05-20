package vkauth

import (
	"context"
	"fmt"

	"github.com/samosvalishe/btp/internal/client/browserprofile"
	"github.com/samosvalishe/btp/internal/client/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// DefaultAutoSolve drives the in-page widget flow. It loads a saved real
// browser profile when available so the captcha origin sees consistent
// fingerprints across the page load and the solve POST.
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
