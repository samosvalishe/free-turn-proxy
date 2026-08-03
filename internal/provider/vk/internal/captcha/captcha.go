package captcha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

var Log logx.Logger = logx.Nop()

func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const (
	captchaAPIVersion = "5.131"
	captchaAPIOrigin  = "https://id.vk.ru"
	captchaDomain     = "vk.ru"

	// captchaDebugInfoFallback - константа виджета из not_robot_captcha.js 1.1.1387.
	// Ротируется вместе с версией бандла; используется, только если бандл не скачался
	// (UUID тут был бы аномалией - VK всегда шлёт 64-hex).
	captchaDebugInfoFallback = "b50d237c4ab98510721070cdbafe5dd88691e8883aa2458968040d70f870d91f"
)

var (
	reCaptchaPowInput   = regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	reCaptchaDifficulty = regexp.MustCompile(`const\s+difficulty\s*=\s*(\d+)`)
	reCaptchaScriptSrc  = regexp.MustCompile(`src="(https://[^"]+not_robot_captcha[^"]+)"`)
	reCaptchaDebugInfo  = regexp.MustCompile(`debug_info:(?:[^"]*\|\|)?"([a-fA-F0-9]{64})"`)

	errCaptchaRateLimit = errors.New("captcha session rate limit reached")
	errCaptchaBot       = errors.New("captcha bot challenge")

	captchaMaxAttempts = 2

	// debugInfoCache кэширует 64-hex debug_info по URL JS-бандла (константа виджета,
	// одна на всех).
	debugInfoCache sync.Map
)

type captchaInitSetting struct {
	Type        string `json:"type"`
	Settings    string `json:"settings"`
	SettingsKey string `json:"settings_key"`
}

type captchaPage struct {
	PowInput      string
	PowDifficulty int
	ScriptURL     string
}

type captchaCheck struct {
	Status       string
	SuccessToken string
	ShowType     string
	Content      captchaContentRef
}

type captchaShowTypeError struct {
	ShowType string
	Content  captchaContentRef
}

func (e *captchaShowTypeError) Error() string {
	return "captcha show type mismatch: " + e.ShowType
}

type captchaSession struct {
	ctx     context.Context
	client  tlsclient.HttpClient
	profile browserprofile.Profile
	layout  widgetLayout
	domain  string
	log     logx.Logger

	// browserFP - слот visitorId FingerprintJS: у живого посетителя стабилен
	// между сессиями, поэтому выводится из seed, а не генерится заново.
	browserFP string
	debugInfo string
	powHash   string

	sensors sensorConfig
	// sensorsStart - момент ответа settings: с него виджет начинает тикать
	// таймером телеметрии.
	sensorsStart  time.Time
	onCompromised func()
}

func (s *captchaSession) logger() logx.Logger {
	if s.log != nil {
		return s.log
	}
	return Log
}

// Solve запускает авторешение captcha против VK captchaNotRobot API.
func Solve(
	ctx context.Context,
	captchaErr *Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
	log logx.Logger,
	onCompromised func(),
) (string, error) {
	if captchaErr == nil || captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}
	l := logx.OrNop(log)
	l.Infof("[STREAM %d] [Captcha] Solving VK Smart Captcha automatically...", streamID)

	s := &captchaSession{
		ctx:           ctx,
		client:        client,
		profile:       profile,
		layout:        layoutFor(profile),
		domain:        captchaDomain,
		log:           l,
		browserFP:     profile.VisitorID,
		sensors:       defaultSensorConfig(),
		onCompromised: onCompromised,
	}

	for attempt := 1; attempt <= captchaMaxAttempts; attempt++ {
		token, solveErr := s.solveOnce(captchaErr)
		if solveErr == nil {
			return token, nil
		}
		l.Warnf("[STREAM %d] [Captcha] solve attempt %d failed: %v", streamID, attempt, solveErr)
		if errors.Is(solveErr, errCaptchaRateLimit) || errors.Is(solveErr, errCaptchaBot) {
			return "", solveErr
		}

		backoffSteps := min(attempt, 10)
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
	s.domain = captchaDomainFromRedirectURI(captchaErr.RedirectURI)
	s.logger().Debugf("[Captcha] using domain=%s", s.domain)

	html, err := s.fetchCaptchaHTML(captchaErr.RedirectURI)
	if err != nil {
		return "", err
	}
	s.logger().Debugf("[Captcha] html fetched bytes=%d", len(html))

	page, err := parseCaptchaPage(html)
	if err != nil {
		s.dumpChunks("html", html)
		return "", err
	}

	s.logger().Debugf("[Captcha] solving pow difficulty=%d", page.PowDifficulty)
	s.powHash = solveCaptchaPoW(s.ctx, page.PowInput, page.PowDifficulty)
	if s.powHash == "" {
		return "", errors.New("captcha pow failed")
	}

	s.debugInfo = s.resolveDebugInfo(page.ScriptURL)

	base := s.captchaBaseValues(captchaErr.SessionToken)
	settingsResp, err := s.captchaRequest("captchaNotRobot.settings", base)
	if err != nil {
		return "", fmt.Errorf("captcha settings failed: %w", err)
	}
	s.sensors = parseSensorConfig(settingsResp)
	s.sensorsStart = time.Now()
	s.logger().Debugf("[Captcha] sensors delay=%s cursor=%t taps=%t accel=%t",
		s.sensors.delay, s.sensors.cursor, s.sensors.taps, s.sensors.accelerometer)

	initResp, err := s.captchaRequest("captchaNotRobot.initSession", [][2]string{
		{"session_token", captchaErr.SessionToken},
		{"domain", s.domain},
		{"lang", "0"},
	})
	if err != nil {
		return "", fmt.Errorf("captcha initSession failed: %w", err)
	}
	showType, sliderContent := parseCaptchaInitSession(initResp)
	s.logger().Debugf("[Captcha] initSession show_type=%q slider_len=%d", showType, len(sliderContent.Value))

	// Отрисовка виджета и первая реакция человека.
	if dwellErr := s.dwell(400, 700); dwellErr != nil {
		return "", dwellErr
	}

	var token string
	switch showType {
	case "slider":
		token, err = s.solveSliderCaptcha(captchaErr.SessionToken, sliderContent)
	case "checkbox", "":
		token, err = s.solveCheckboxCaptcha(captchaErr.SessionToken)
	default:
		return "", fmt.Errorf("unsupported captcha type: %s", showType)
	}
	if err != nil {
		token, err = s.escalate(captchaErr.SessionToken, sliderContent, err)
	}
	if err != nil {
		// Живой посетитель, уходя с нерешённой captcha, закрывает виджет.
		if _, leaveErr := s.captchaRequest("captchaNotRobot.leaveCaptcha", base); leaveErr != nil {
			s.logger().Debugf("[Captcha] leaveCaptcha failed: %v", leaveErr)
		}
		return "", err
	}

	if _, endErr := s.captchaRequest("captchaNotRobot.endSession", base); endErr != nil {
		s.logger().Warnf("[Captcha] endSession failed: %v", endErr)
	}
	return token, nil
}

// escalate добивает сессию, если VK на check-е сменил тип челленджа: виджет в
// браузере дорисовывает слайдер на месте, а не переоткрывает captcha.
func (s *captchaSession) escalate(sessionToken string, initContent captchaContentRef, cause error) (string, error) {
	var mismatch *captchaShowTypeError
	if !errors.As(cause, &mismatch) || !strings.EqualFold(mismatch.ShowType, "slider") {
		return "", cause
	}
	content := mismatch.Content
	if content.Value == "" {
		content = initContent
	}
	if content.Value == "" {
		return "", cause
	}
	if s.onCompromised != nil {
		s.onCompromised()
	}
	s.logger().Debugf("[Captcha] escalated to slider in-session (content source=%s)", content.Source)
	// Перерисовка виджета плюс пауза на осознание.
	if err := s.dwell(500, 1100); err != nil {
		return "", err
	}
	return s.solveSliderCaptcha(sessionToken, content)
}

// dwell выдерживает паузу [minMs, maxMs): телеметрия виджета тикает по таймеру,
// поэтому длина её массивов обязана биться с реальным временем сессии.
func (s *captchaSession) dwell(minMs, maxMs int) error {
	d := time.Duration(minMs+randx.Intn(max(maxMs-minMs, 1))) * time.Millisecond
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseCaptchaInitSession(raw map[string]any) (string, captchaContentRef) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return "", captchaContentRef{}
	}
	return captchaStringifyAny(resp["show_captcha_type"]), parseSliderContentRef(resp["content_settings"])
}

func parseSliderContentRef(raw any) captchaContentRef {
	content := captchaContentRef{}
	data, err := json.Marshal(raw)
	if err != nil {
		return content
	}
	var settings []captchaInitSetting
	if json.Unmarshal(data, &settings) != nil {
		return content
	}
	for _, setting := range settings {
		if setting.Type == "slider" {
			content = setting.contentRef()
		}
	}
	return content
}

type captchaContentRef struct {
	Source string
	Value  string
}

func (s captchaInitSetting) contentRef() captchaContentRef {
	if v := strings.TrimSpace(s.SettingsKey); v != "" {
		return captchaContentRef{Source: "settings_key", Value: v}
	}
	if v := strings.TrimSpace(s.Settings); v != "" {
		return captchaContentRef{Source: "captcha_settings", Value: v}
	}
	return captchaContentRef{}
}

func (s *captchaSession) captchaBaseValues(sessionToken string) [][2]string {
	return [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"access_token", ""},
	}
}

func captchaDomainFromRedirectURI(redirectURI string) string {
	u, err := neturl.Parse(redirectURI)
	if err != nil {
		return captchaDomain
	}
	domain := strings.TrimSpace(u.Query().Get("domain"))
	if domain == "" {
		return captchaDomain
	}
	return domain
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

func (s *captchaSession) resolveDebugInfo(scriptURL string) string {
	if scriptURL != "" {
		v, err := s.fetchDebugInfoJS(scriptURL)
		if err == nil {
			s.logger().Debugf("[Captcha] debug_info from JS len=%d", len(v))
			return v
		}
		s.logger().Warnf("[Captcha] debug_info JS fetch failed: %v; using pinned fallback", err)
	} else {
		s.logger().Warnf("[Captcha] captcha script URL not in HTML; using pinned fallback")
	}
	return captchaDebugInfoFallback
}

func (s *captchaSession) fetchDebugInfoJS(scriptURL string) (string, error) {
	if cached, ok := debugInfoCache.Load(scriptURL); ok {
		if v, ok := cached.(string); ok {
			return v, nil
		}
	}
	body, err := s.doRaw(fhttp.MethodGet, scriptURL, nil, map[string]string{
		"Accept":  "text/javascript,*/*",
		"Referer": captchaAPIOrigin + "/",
	})
	if err != nil {
		return "", err
	}
	m := reCaptchaDebugInfo.FindSubmatch(body)
	if len(m) < 2 {
		return "", errors.New("debug_info constant not found in JS")
	}
	v := string(m[1])
	if v != captchaDebugInfoFallback {
		// Константа ротируется вместе с версией бандла: сигнал, что пора
		// пересматривать пин и сверять протокол виджета.
		s.logger().Warnf("[Captcha] debug_info drifted from pinned value; bundle=%s", captchaSafeURL(scriptURL))
	}
	debugInfoCache.Store(scriptURL, v)
	return v, nil
}

func (s *captchaSession) dumpChunks(label, body string) {
	const chunk = 1800
	total := (len(body) + chunk - 1) / chunk
	l := s.logger()
	l.Debugf("[Captcha][DUMP] %s bytes=%d chunks=%d", label, len(body), total)
	for i := range total {
		start := i * chunk
		end := min(start+chunk, len(body))
		l.Debugf("[Captcha][DUMP %s %d/%d] %s", label, i+1, total, body[start:end])
	}
}

func parseCaptchaPage(html string) (*captchaPage, error) {
	page := &captchaPage{}

	if m := reCaptchaScriptSrc.FindStringSubmatch(html); len(m) >= 2 {
		page.ScriptURL = m[1]
	}

	m := reCaptchaPowInput.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil, errors.New("captcha powInput not found")
	}
	page.PowInput = m[1]

	dm := reCaptchaDifficulty.FindStringSubmatch(html)
	if len(dm) < 2 {
		return nil, errors.New("captcha difficulty const not found")
	}
	difficulty, err := strconv.Atoi(dm[1])
	if err != nil || difficulty <= 0 {
		return nil, fmt.Errorf("invalid captcha difficulty %q", dm[1])
	}
	page.PowDifficulty = difficulty
	return page, nil
}

func (s *captchaSession) captchaRequest(method string, form [][2]string) (map[string]any, error) {
	endpoint := "https://api.vk.ru/method/" + method + "?v=" + captchaAPIVersion
	body, err := s.doRaw(fhttp.MethodPost, endpoint, form, map[string]string{
		"Origin":  captchaAPIOrigin,
		"Referer": captchaAPIOrigin + "/",
	})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("captcha api decode: %w", err)
	}
	s.logger().Debugf("[Captcha] api %s response=%s", method, captchaAPIResponseSummary(out))
	return out, nil
}

func (s *captchaSession) performCaptchaCheck(
	sessionToken string,
	answerJSON string,
	g gesture,
) (*captchaCheck, error) {
	data := buildAnalytics(s.profile, s.sensors, g, time.Since(s.sensorsStart))
	values := make([][2]string, 0, 15)
	values = append(values,
		[2]string{"session_token", sessionToken},
		[2]string{"domain", s.domain},
		[2]string{"adFp", ""},
	)
	values = append(values, data.fields()...)
	values = append(values,
		[2]string{"browser_fp", s.browserFP},
		[2]string{"hash", s.powHash},
		[2]string{"answer", base64.StdEncoding.EncodeToString([]byte(answerJSON))},
		[2]string{"debug_info", s.debugInfo},
		[2]string{"access_token", ""},
	)
	resp, err := s.captchaRequest("captchaNotRobot.check", values)
	if err != nil {
		return nil, fmt.Errorf("captcha check failed: %w", err)
	}
	s.logger().Debugf("[Captcha] check payload answer_bytes=%d pointer_samples=%d conn_samples=%d",
		len(answerJSON), len(data.cursor)+len(data.taps), len(data.connRtt))
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
		Content:      parseSliderContentRef(resp["content_settings"]),
	}
	if out.Status == "" {
		return nil, fmt.Errorf("captcha check status missing: %v", raw)
	}
	return out, nil
}

func (s *captchaSession) sendComponentDone(sessionToken string) error {
	s.logger().Debugf("[Captcha] componentDone device_bytes=%d", len(s.profile.DeviceJSON))
	if _, err := s.captchaRequest("captchaNotRobot.componentDone", [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"browser_fp", ""},
		{"device", s.profile.DeviceJSON},
		{"access_token", ""},
	}); err != nil {
		return fmt.Errorf("captcha componentDone failed: %w", err)
	}
	return nil
}

// performGesture проживает жест в реальном времени: массивы телеметрии считаются
// от фактически прошедшего времени, поэтому жест нельзя "нарисовать" мгновенно.
func (s *captchaSession) performGesture(g gesture) error {
	total := g.move + g.settle
	timer := time.NewTimer(total)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *captchaSession) solveCheckboxCaptcha(sessionToken string) (string, error) {
	if err := s.sendComponentDone(sessionToken); err != nil {
		return "", err
	}

	target := s.layout.checkbox
	g := gesture{
		from:   approachFrom(s.profile, s.layout, target),
		to:     target,
		move:   time.Duration(500+randx.Intn(500)) * time.Millisecond,
		settle: time.Duration(250+randx.Intn(350)) * time.Millisecond,
	}
	if err := s.performGesture(g); err != nil {
		return "", err
	}

	check, err := s.performCaptchaCheck(sessionToken, "{}", g)
	if err != nil {
		return "", err
	}
	if check.ShowType != "" && !strings.EqualFold(check.ShowType, "checkbox") {
		return "", &captchaShowTypeError{ShowType: check.ShowType, Content: check.Content}
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
	buf := make([]byte, 0, len(input)+20)
	buf = append(buf, input...)
	start := time.Now()
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
			durationMs := time.Since(start).Milliseconds()
			payload := fmt.Sprintf(`{"hash":"%s","nonce":%d,"duration_ms":%d}`, hashHex, nonce, durationMs)
			return "v2." + base64.RawStdEncoding.EncodeToString([]byte(payload))
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
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Origin", captchaAPIOrigin)
	req.Header.Set("Referer", captchaAPIOrigin+"/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	// Последним: персона снимает заголовки, которых у неё быть не может, и
	// задаёт порядок отправки.
	browserprofile.ApplyFhttp(req, s.profile)

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		s.logger().Debugf("[Captcha] http %s %s failed after=%s form=%s err=%v", method, captchaSafeURL(endpoint), time.Since(start).Truncate(time.Millisecond), captchaFormSummary(form), err)
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger().Warnf("[Captcha] close body: %s", closeErr)
		}
	}()
	data, readErr := io.ReadAll(resp.Body)
	s.logger().Debugf("[Captcha] http %s %s status=%d bytes=%d after=%s form=%s", method, captchaSafeURL(endpoint), resp.StatusCode, len(data), time.Since(start).Truncate(time.Millisecond), captchaFormSummary(form))
	return data, readErr
}

func captchaAPIResponseSummary(raw map[string]any) string {
	if errData, ok := raw["error"].(map[string]any); ok {
		return fmt.Sprintf("error code=%s msg=%q keys=%s", captchaStringifyAny(errData["error_code"]), captchaStringifyAny(errData["error_msg"]), captchaMapKeys(errData))
	}
	if resp, ok := raw["response"].(map[string]any); ok {
		status := captchaStringifyAny(resp["status"])
		showType := captchaStringifyAny(resp["show_captcha_type"])
		tokenLen := len(captchaStringifyAny(resp["success_token"]))
		return fmt.Sprintf("ok status=%q show_type=%q success_token_len=%d keys=%s", status, showType, tokenLen, captchaMapKeys(resp))
	}
	return "unknown keys=" + captchaMapKeys(raw)
}

func captchaMapKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func captchaSafeURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	if u.Host == "" {
		return u.Path
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Host + path
}

func captchaFormSummary(values [][2]string) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(values))
	for _, kv := range values {
		switch kv[0] {
		case "session_token", "browser_fp", "hash", "answer", "debug_info", "device", "settings_key", "captcha_settings":
			parts = append(parts, fmt.Sprintf("%s:%d", kv[0], len(kv[1])))
		default:
			parts = append(parts, kv[0])
		}
	}
	return strings.Join(parts, ",")
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
