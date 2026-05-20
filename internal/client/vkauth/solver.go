package vkauth

import (
	"context"
	"net"

	"github.com/samosvalishe/btp/internal/client/browserprofile"
	"github.com/samosvalishe/btp/internal/client/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// CaptchaSolveMode selects between auto-solving and manual-browser fallback.
type CaptchaSolveMode int

const (
	CaptchaSolveModeAuto CaptchaSolveMode = iota
	CaptchaSolveModeManual
)

// CaptchaSolveModeForAttempt picks the solver to use on a given retry attempt.
// Returns (mode, true) when a mode is available, (_, false) when exhausted.
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

// AutoSolveFunc returns a success_token for the captcha when solvable via the
// in-page widget flow. Implementations must respect ctx cancellation.
type AutoSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID int,
	http tlsclient.HttpClient,
	profile browserprofile.Profile,
) (token string, err error)

// ManualSolveFunc opens the local browser-based fallback. Returns either a
// success_token (token != "") or a captcha_key, depending on which path the
// VK error exposed.
type ManualSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	dialer net.Dialer,
) (token, key string, err error)
