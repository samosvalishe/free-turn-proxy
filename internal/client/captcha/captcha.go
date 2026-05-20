package captcha

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"

	"github.com/samosvalishe/btp/internal/client/browserprofile"
	"github.com/samosvalishe/btp/internal/logx"
)

// Log is the package-level logger. Defaults to no-op; main wires it via
// SetLogger so captcha output respects the global -debug flag and levels.
//
// Deprecated: pass a logx.Logger to Solve directly. SetLogger remains for
// callers that cannot be updated (e.g. auto_solver.go uses captcha.Log directly).
var Log logx.Logger = logx.Nop()

// SetLogger installs a logger for this package. Safe to call once at startup.
//
// Deprecated: prefer passing a logx.Logger to Solve.
func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const (
	captchaAPIVersion    = "5.131"
	captchaScriptVersion = "1.1.1324"
	captchaDeviceInfo    = `{"screenWidth":1920,"screenHeight":1080,"screenAvailWidth":1920,"screenAvailHeight":1080,"innerWidth":1920,"innerHeight":951,"devicePixelRatio":1,"language":"en-US","languages":["en-US","en"],"webdriver":false,"hardwareConcurrency":8,"notificationsPermission":"denied"}`
)

var (
	reCaptchaPowInput   = regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	reCaptchaDifficulty = regexp.MustCompile(`const\s+difficulty\s*=\s*(\d+)`)
	reCaptchaWindowInit = regexp.MustCompile(`(?s)window\.init\s*=\s*(\{.*?})\s*;`)
	reCaptchaScriptSrc  = regexp.MustCompile(`src="(https://[^"]+not_robot_captcha[^"]+)"`)
	reCaptchaDebugInfo  = regexp.MustCompile(`debug_info:(?:[^"]*\|\|)?"([a-fA-F0-9]{64})"`)
	reCaptchaVersion    = regexp.MustCompile(`vkid/([0-9.]*)/not_robot_captcha\.js`)

	errCaptchaRateLimit = errors.New("captcha session rate limit reached")
	errCaptchaBot       = errors.New("captcha bot challenge")

	captchaMaxAttempts = 2

	captchaDebugCache  sync.Map // scriptURL -> string
	captchaHeaderOrder = []string{
		"host",
		"content-length",
		"sec-ch-ua-platform",
		"accept-language",
		"sec-ch-ua",
		"content-type",
		"sec-ch-ua-mobile",
		"user-agent",
		"accept",
		"origin",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-dest",
		"referer",
		"accept-encoding",
		"priority",
	}
	captchaPHeaderOrder = []string{":method", ":path", ":authority", ":scheme"}
)

type captchaInit struct {
	Data captchaInitData `json:"data"`
}

type captchaInitData struct {
	ShowCaptchaType string               `json:"show_captcha_type"`
	CaptchaSettings []captchaInitSetting `json:"captcha_settings"`
}

type captchaInitSetting struct {
	Type     string `json:"type"`
	Settings string `json:"settings"`
}

type captchaPage struct {
	PowInput      string
	PowDifficulty int
	ScriptURL     string
	Init          *captchaInit
}

type captchaCheck struct {
	Status       string
	SuccessToken string
	ShowType     string
}

type captchaShowTypeError struct {
	ShowType string
}

func (e *captchaShowTypeError) Error() string {
	return "captcha show type mismatch: " + e.ShowType
}

type captchaSession struct {
	ctx          context.Context
	client       tlsclient.HttpClient
	profile      browserprofile.Profile
	savedProfile *browserprofile.Saved
	log          logx.Logger
}

func (s *captchaSession) logger() logx.Logger {
	if s.log != nil {
		return s.log
	}
	return Log
}

// Solve runs the automatic captcha challenge against VK's captchaNotRobot API
// and returns a success token on success. log may be nil, in which case the
// package-level Log is used.
func Solve(
	ctx context.Context,
	captchaErr *Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
	savedProfile *browserprofile.Saved,
	log logx.Logger,
) (string, error) {
	if captchaErr == nil || captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}
	l := logx.OrNop(log)
	l.Infof("[STREAM %d] [Captcha] Solving VK Smart Captcha automatically...", streamID)

	s := &captchaSession{ctx: ctx, client: client, profile: profile, savedProfile: savedProfile, log: l}

	for attempt := 1; attempt <= captchaMaxAttempts; attempt++ {
		token, solveErr := s.solveOnce(captchaErr)
		if solveErr == nil {
			return token, nil
		}
		l.Warnf("[STREAM %d] [Captcha] solve attempt %d failed: %v", streamID, attempt, solveErr)
		if errors.Is(solveErr, errCaptchaRateLimit) {
			return "", solveErr
		}

		backoffSteps := attempt
		if backoffSteps > 10 {
			backoffSteps = 10
		}
		timer := time.NewTimer(time.Duration(backoffSteps) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("captcha attempts exhausted")
}

func (s *captchaSession) solveOnce(captchaErr *Error) (string, error) {
	html, err := s.fetchCaptchaHTML(captchaErr.RedirectURI)
	if err != nil {
		return "", err
	}

	page, err := parseCaptchaPage(html)
	if err != nil {
		return "", err
	}
	if page.PowInput == "" {
		return "", errors.New("failed to find PoW settings")
	}

	sliderSettings := ""
	if page.Init != nil {
		for _, setting := range page.Init.Data.CaptchaSettings {
			if setting.Type == "slider" {
				sliderSettings = setting.Settings
			}
		}
	}
	if page.Init != nil && page.Init.Data.ShowCaptchaType == "slider" && sliderSettings == "" {
		return "", errors.New("failed to find slider captcha settings")
	}

	s.logger().Debugf("[Captcha] solving pow difficulty=%d", page.PowDifficulty)
	hash := solveCaptchaPoW(s.ctx, page.PowInput, page.PowDifficulty)
	if hash == "" {
		return "", errors.New("captcha pow failed")
	}
	s.logger().Debugf("[Captcha] pow solved")

	base := captchaBaseValues(captchaErr.SessionToken)
	if _, settingsErr := s.captchaRequest("captchaNotRobot.settings", base); settingsErr != nil {
		return "", fmt.Errorf("captcha settings failed: %w", settingsErr)
	}

	browserFP, err := captchaBrowserFP()
	if err != nil {
		return "", err
	}
	if s.savedProfile != nil && strings.TrimSpace(s.savedProfile.BrowserFp) != "" {
		browserFP = s.savedProfile.BrowserFp
	}

	if m := reCaptchaVersion.FindStringSubmatch(page.ScriptURL); len(m) > 1 {
		if m[1] != captchaScriptVersion {
			s.logger().Warnf("[Captcha] script version drift: known=%s latest=%s", captchaScriptVersion, m[1])
		}
	}

	debugInfo, err := s.fetchDebugInfo(page.ScriptURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch debug info: %w (script_version=%s)", err, captchaScriptVersion)
	}

	showType := ""
	if page.Init != nil {
		showType = page.Init.Data.ShowCaptchaType
	}
	var token string
	for {
		s.logger().Debugf("[Captcha] solving show_type=%s", showType)
		switch showType {
		case "slider":
			token, err = s.solveSliderCaptcha(captchaErr.SessionToken, browserFP, hash, sliderSettings, debugInfo)
		case "checkbox", "":
			token, err = s.solveCheckboxCaptcha(captchaErr.SessionToken, browserFP, hash, debugInfo)
		default:
			return "", fmt.Errorf("unsupported captcha type: %s", showType)
		}
		if err == nil {
			break
		}
		if errors.Is(err, errCaptchaBot) && !strings.EqualFold(showType, "slider") && sliderSettings != "" {
			s.logger().Infof("[Captcha] checkbox returned BOT, trying slider challenge")
			showType = "slider"
			continue
		}
		var stErr *captchaShowTypeError
		if !errors.As(err, &stErr) || stErr.ShowType == "" {
			return "", err
		}
		showType = stErr.ShowType
	}

	if _, endErr := s.captchaRequest("captchaNotRobot.endSession", base); endErr != nil {
		s.logger().Warnf("[Captcha] endSession failed: %v", endErr)
	}
	return token, nil
}

func captchaBaseValues(sessionToken string) [][2]string {
	return [][2]string{
		{"session_token", sessionToken},
		{"domain", "vk.com"},
		{"adFp", ""},
		{"access_token", ""},
	}
}

func captchaBrowserFP() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("browser fp generate: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *captchaSession) fetchCaptchaHTML(redirectURI string) (string, error) {
	body, err := s.doRaw(fhttp.MethodGet, redirectURI, nil, map[string]string{
		"Accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Sec-Fetch-Dest": "document",
		"Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Site": "cross-site",
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (s *captchaSession) fetchDebugInfo(scriptURL string) (string, error) {
	if cached, ok := captchaDebugCache.Load(scriptURL); ok {
		if cachedDebugInfo, ok := cached.(string); ok {
			return cachedDebugInfo, nil
		}
		captchaDebugCache.Delete(scriptURL)
	}
	body, err := s.doRaw(fhttp.MethodGet, scriptURL, nil, map[string]string{
		"Accept":  "text/javascript,*/*",
		"Referer": "https://id.vk.com/",
	})
	if err != nil {
		return "", err
	}
	m := reCaptchaDebugInfo.FindSubmatch(body)
	if len(m) < 2 {
		return "", errors.New("debug_info match not found")
	}
	v := string(m[1])
	captchaDebugCache.Store(scriptURL, v)
	s.logger().Debugf("[Captcha] debug_info fetched url=%s", scriptURL)
	return v, nil
}

func parseCaptchaPage(html string) (*captchaPage, error) {
	page := &captchaPage{}

	match := reCaptchaWindowInit.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil, errors.New("captcha init json not found")
	}
	var init captchaInit
	if err := json.Unmarshal([]byte(match[1]), &init); err != nil {
		return nil, fmt.Errorf("captcha init json parse: %w", err)
	}
	page.Init = &init

	match = reCaptchaScriptSrc.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil, errors.New("captcha script url not found")
	}
	page.ScriptURL = match[1]

	if m := reCaptchaPowInput.FindStringSubmatch(html); len(m) >= 2 {
		page.PowInput = m[1]
	}
	if page.PowInput == "" {
		return page, nil
	}

	match = reCaptchaDifficulty.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil, errors.New("captcha difficulty const not found")
	}
	difficulty, err := strconv.Atoi(match[1])
	if err != nil || difficulty <= 0 {
		return nil, fmt.Errorf("invalid captcha difficulty %q", match[1])
	}
	page.PowDifficulty = difficulty
	return page, nil
}

func (s *captchaSession) captchaRequest(method string, form [][2]string) (map[string]any, error) {
	endpoint := "https://api.vk.ru/method/" + method + "?v=" + captchaAPIVersion
	body, err := s.doRaw(fhttp.MethodPost, endpoint, form, map[string]string{
		"Origin":   "https://id.vk.com",
		"Referer":  "https://id.vk.com/",
		"Priority": "u=1, i",
	})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("captcha api decode: %w", err)
	}
	return out, nil
}

func (s *captchaSession) performCaptchaCheck(
	sessionToken string,
	browserFP string,
	hash string,
	answerJSON string,
	cursor string,
	debugInfo string,
) (*captchaCheck, error) {
	values := [][2]string{
		{"session_token", sessionToken},
		{"domain", "vk.com"},
		{"adFp", ""},
		{"accelerometer", "[]"},
		{"gyroscope", "[]"},
		{"motion", "[]"},
		{"cursor", cursor},
		{"taps", "[]"},
		{"connectionRtt", "[]"},
		{"connectionDownlink", "[]"},
		{"browser_fp", browserFP},
		{"hash", hash},
		{"answer", base64.StdEncoding.EncodeToString([]byte(answerJSON))},
		{"debug_info", debugInfo},
		{"access_token", ""},
	}
	resp, err := s.captchaRequest("captchaNotRobot.check", values)
	if err != nil {
		return nil, fmt.Errorf("captcha check failed: %w", err)
	}
	check, err := parseCaptchaCheck(resp)
	if err != nil {
		return nil, err
	}
	if check.ShowType != "" {
		s.logger().Debugf("[Captcha] check status=%s show_type=%s", check.Status, check.ShowType)
	} else {
		s.logger().Debugf("[Captcha] check status=%s", check.Status)
	}
	return check, nil
}

func parseCaptchaCheck(raw map[string]any) (*captchaCheck, error) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid captcha check response: %v", raw)
	}
	out := &captchaCheck{
		Status:       captchaStringifyAny(resp["status"]),
		SuccessToken: captchaStringifyAny(resp["success_token"]),
		ShowType:     captchaStringifyAny(resp["show_captcha_type"]),
	}
	if out.Status == "" {
		return nil, fmt.Errorf("captcha check status missing: %v", raw)
	}
	return out, nil
}

func (s *captchaSession) solveCheckboxCaptcha(
	sessionToken string,
	browserFP string,
	hash string,
	debugInfo string,
) (string, error) {
	deviceJSON := captchaDeviceInfo
	if s.savedProfile != nil && strings.TrimSpace(s.savedProfile.DeviceJSON) != "" {
		deviceJSON = s.savedProfile.DeviceJSON
	}
	if _, err := s.captchaRequest("captchaNotRobot.componentDone", [][2]string{
		{"session_token", sessionToken},
		{"domain", "vk.com"},
		{"adFp", ""},
		{"browser_fp", browserFP},
		{"device", deviceJSON},
		{"access_token", ""},
	}); err != nil {
		return "", fmt.Errorf("captcha componentDone failed: %w", err)
	}

	select {
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	case <-time.After(time.Duration(400+mathrand.Intn(250)) * time.Millisecond):
	}

	check, err := s.performCaptchaCheck(sessionToken, browserFP, hash, "{}", "[]", debugInfo)
	if err != nil {
		return "", err
	}
	if check.ShowType != "" && !strings.EqualFold(check.ShowType, "checkbox") {
		return "", &captchaShowTypeError{ShowType: check.ShowType}
	}
	if strings.EqualFold(check.Status, "error_limit") {
		return "", errCaptchaRateLimit
	}
	if strings.EqualFold(check.Status, "bot") {
		return "", fmt.Errorf("%w: checkbox captcha rejected: status=%s", errCaptchaBot, check.Status)
	}
	if !strings.EqualFold(check.Status, "ok") {
		return "", fmt.Errorf("checkbox captcha rejected: status=%s", check.Status)
	}
	if check.SuccessToken == "" {
		return "", errors.New("captcha success token not found")
	}
	return check.SuccessToken, nil
}

func solveCaptchaPoW(ctx context.Context, input string, difficulty int) string {
	if input == "" || difficulty <= 0 {
		return ""
	}
	target := strings.Repeat("0", difficulty)
	// ctx-check every 1024 iterations keeps cancel latency under a few ms
	// even on weak ARM (was every 4096).
	buf := make([]byte, 0, len(input)+20)
	buf = append(buf, input...)
	for nonce := 1; nonce <= 10_000_000; nonce++ {
		if nonce&1023 == 0 {
			select {
			case <-ctx.Done():
				return ""
			default:
			}
		}
		buf = strconv.AppendInt(buf[:len(input)], int64(nonce), 10)
		sum := sha256.Sum256(buf)
		hashHex := hex.EncodeToString(sum[:])
		if strings.HasPrefix(hashHex, target) {
			return hashHex
		}
	}
	return ""
}

func (s *captchaSession) doRaw(
	method string,
	endpoint string,
	form [][2]string,
	extraHeaders map[string]string,
) ([]byte, error) {
	var body []byte
	if form != nil {
		body = []byte(captchaEncodeForm(form))
	}
	req, err := fhttp.NewRequestWithContext(s.ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	browserprofile.ApplyFhttp(req, s.profile)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Origin", "https://vk.com")
	req.Header.Set("Referer", "https://vk.com/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	req.Header[fhttp.HeaderOrderKey] = captchaHeaderOrder
	req.Header[fhttp.PHeaderOrderKey] = captchaPHeaderOrder

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger().Warnf("[Captcha] close body: %s", closeErr)
		}
	}()
	return io.ReadAll(resp.Body)
}

func captchaEncodeForm(values [][2]string) string {
	if len(values) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, kv := range values {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(captchaQueryEscape(kv[0]))
		sb.WriteByte('=')
		sb.WriteString(captchaQueryEscape(kv[1]))
	}
	return sb.String()
}

func captchaQueryEscape(s string) string {
	const upper = "0123456789ABCDEF"
	hexDigits := func(b byte) [3]byte {
		return [3]byte{'%', upper[b>>4], upper[b&0xF]}
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			out = append(out, '+')
		case ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		default:
			h := hexDigits(c)
			out = append(out, h[:]...)
		}
	}
	return string(out)
}

func captchaStringifyAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}
