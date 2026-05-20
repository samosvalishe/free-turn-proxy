package captcha

import (
	"fmt"
	neturl "net/url"
)

// Error describes a VK API "captcha required" error (error_code 14) and the
// fields the solver needs to drive the challenge.
type Error struct {
	ErrorCode               int
	ErrorMsg                string
	CaptchaSid              string
	CaptchaImg              string
	RedirectURI             string
	IsSoundCaptchaAvailable bool
	SessionToken            string
	CaptchaTs               string
	CaptchaAttempt          string
}

// ParseError extracts a captcha challenge from a VK API error payload. Returns
// nil if required fields are missing.
func ParseError(errData map[string]any) *Error {
	codeFloat, ok := errData["error_code"].(float64)
	if !ok {
		Log.Warnf("[Captcha] missing error_code in error data")
		return nil
	}
	code := int(codeFloat)

	redirectURI, ok := errData["redirect_uri"].(string)
	if !ok {
		Log.Warnf("[Captcha] missing redirect_uri in error data")
		return nil
	}

	captchaSid, ok := errData["captcha_sid"].(string)
	if !ok {
		if sidNum, ok2 := errData["captcha_sid"].(float64); ok2 {
			captchaSid = fmt.Sprintf("%.0f", sidNum)
		} else {
			Log.Warnf("[Captcha] missing captcha_sid in error data")
			return nil
		}
	}

	captchaImg, ok := errData["captcha_img"].(string)
	if !ok {
		Log.Warnf("[Captcha] missing captcha_img in error data")
		return nil
	}

	errorMsg, ok := errData["error_msg"].(string)
	if !ok {
		Log.Warnf("[Captcha] missing error_msg in error data")
		return nil
	}

	var sessionToken string
	if redirectURI != "" {
		if parsed, err := neturl.Parse(redirectURI); err == nil {
			sessionToken = parsed.Query().Get("session_token")
		} else {
			Log.Warnf("[Captcha] failed to parse redirect_uri: %v", err)
			return nil
		}
	}
	if sessionToken == "" {
		if st, stOk := errData["session_token"].(string); stOk {
			sessionToken = st
		}
	}

	isSound, ok := errData["is_sound_captcha_available"].(bool)
	if !ok {
		isSound = false
	}

	var captchaTs string
	if tsFloat, ok := errData["captcha_ts"].(float64); ok {
		captchaTs = fmt.Sprintf("%.0f", tsFloat)
	} else if tsStr, ok := errData["captcha_ts"].(string); ok {
		captchaTs = tsStr
	}

	var captchaAttempt string
	if attFloat, ok := errData["captcha_attempt"].(float64); ok {
		captchaAttempt = fmt.Sprintf("%.0f", attFloat)
	} else if attStr, ok := errData["captcha_attempt"].(string); ok {
		captchaAttempt = attStr
	}

	return &Error{
		ErrorCode:               code,
		ErrorMsg:                errorMsg,
		CaptchaSid:              captchaSid,
		CaptchaImg:              captchaImg,
		RedirectURI:             redirectURI,
		IsSoundCaptchaAvailable: isSound,
		SessionToken:            sessionToken,
		CaptchaTs:               captchaTs,
		CaptchaAttempt:          captchaAttempt,
	}
}

// IsCaptcha reports whether this error represents an actionable captcha
// challenge (error_code 14 with both redirect_uri and session_token present).
func (e *Error) IsCaptcha() bool {
	return e.ErrorCode == 14 && e.RedirectURI != "" && e.SessionToken != ""
}
