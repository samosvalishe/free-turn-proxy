package captcha

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

var logHolder logx.Holder

func SetLogger(l logx.Logger) { logHolder.Set(l) }

func Log() logx.Logger { return logHolder.Get() }

const (
	captchaAPIVersion = "5.131"
	captchaAPIOrigin  = "https://id.vk.ru"
	captchaAPIHost    = "api.vk.ru"
	captchaDomain     = "vk.ru"
)

var (
	reCaptchaInitGlobal = regexp.MustCompile(`window\.init\s*=\s*\{`)
	reCaptchaVKGlobal   = regexp.MustCompile(`window\.vk\s*=\s*\{`)
	reCaptchaDebugInfo  = regexp.MustCompile(`[A-Za-z_$][\w$]*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"`)

	errCaptchaRateLimit = errors.New("captcha session rate limit reached")
	errCaptchaBot       = errors.New("captcha bot challenge")

	ErrUnavailable = errors.New("captcha unavailable")

	captchaMaxAttempts = 2
)

type captchaInitSetting struct {
	Type        string `json:"type"`
	Settings    string `json:"settings"`
	SettingsKey string `json:"settings_key"`
}

type captchaPage struct {
	Pow       powParams
	DebugInfo string
	Init      captchaInitData
}

// captchaInitData - состояние из window.init: пока оно есть, виджет не ходит в
// initSession, и лишний запрос выдавал бы нас.
type captchaInitData struct {
	Found    bool
	APIHost  string
	ShowType string
	Content  captchaContentRef
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
	domain  string
	apiHost string
	log     logx.Logger

	// VK раздаёт страницу то с id.vk.ru, то с api.vk.ru, а от её совпадения с
	// apiHost зависят Origin, Referer и Sec-Fetch-Site.
	pageURL    string
	pageOrigin string

	// checked - после check session_token потрачен, повтор сессии сам по себе аномалия.
	checked bool

	// browserFP - visitorId FingerprintJS: у живого посетителя стабилен между сессиями.
	browserFP string
	debugInfo string
	powHash   string

	// downlink - у живого клиента одно значение на всю captcha, не отдельное на тик.
	downlink float64

	sensors sensorConfig
	// sensorsStart - ответ settings: с него виджет начинает тикать таймером телеметрии.
	sensorsStart time.Time
	// started - начало решения; все http-строки лога отмечены смещением от него.
	started time.Time
}

func (s *captchaSession) logger() logx.Logger {
	if s.log != nil {
		return s.log
	}
	return Log()
}

// Solve запускает авторешение captcha против VK captchaNotRobot API.
func Solve(
	ctx context.Context,
	captchaErr *Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
	log logx.Logger,
) (string, error) {
	if captchaErr == nil || captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}
	l := logx.OrNop(log)
	l.Infof("[STREAM %d] [Captcha] Solving VK Smart Captcha automatically...", streamID)

	s := &captchaSession{
		ctx:       ctx,
		client:    client,
		profile:   profile,
		domain:    captchaDomain,
		apiHost:   captchaAPIHost,
		log:       l,
		browserFP: profile.VisitorID,
		downlink:  sessionDownlink(),
		sensors:   defaultSensorConfig(),
		started:   time.Now(),
	}

	var solveErr error
	for attempt := 1; attempt <= captchaMaxAttempts; attempt++ {
		var token string
		token, solveErr = s.solveOnce(captchaErr)
		if solveErr == nil {
			return token, nil
		}
		l.Debugf("[STREAM %d] [Captcha] solve attempt %d failed: %v", streamID, attempt, solveErr)
		// Повторяем только то, что не дошло до check.
		if s.checked || errors.Is(solveErr, errCaptchaRateLimit) || errors.Is(solveErr, errCaptchaBot) {
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
	return "", fmt.Errorf("captcha attempts exhausted: %w", solveErr)
}

func (s *captchaSession) solveOnce(captchaErr *Error) (string, error) {
	s.domain = captchaDomainFromRedirectURI(captchaErr.RedirectURI)
	s.setPageURL(captchaErr.RedirectURI)
	s.logger().Debugf("[Captcha] using domain=%s page=%s", s.domain, SafeURL(s.pageURL))

	html, err := s.fetchCaptchaHTML(captchaErr.RedirectURI)
	if err != nil {
		return "", err
	}
	s.logger().Debugf("[Captcha] html fetched bytes=%d", len(html))

	page, err := parseCaptchaPage(html)
	if err != nil {
		s.logger().Debugf("[Captcha] page parse failed: %v (bytes=%d); pow script near: %s", err, len(html), powSnippet(html))
		return "", err
	}

	// Браузер тянет подресурсы параллельно с исполнением скрипта, не после него.
	assets := parsePageAssets(html)
	assetsDone := make(chan struct{})
	go func() {
		defer close(assetsDone)
		s.loadAssets(assets)
	}()
	// Ранний выход не оставляет догрузку в фоне следующей попытке.
	defer func() { <-assetsDone }()

	s.logger().Debugf("[Captcha] solving pow difficulty=%d assets=%d", page.Pow.Difficulty, len(assets))
	s.powHash, err = s.powEnvelope(page.Pow)
	if err != nil {
		return "", err
	}

	s.debugInfo = page.DebugInfo
	s.logger().Debugf("[Captcha] debug_info=%s", s.debugInfo)

	<-assetsDone

	if page.Init.APIHost != "" {
		s.apiHost = page.Init.APIHost
	}

	var showType string
	var sliderContent captchaContentRef
	if page.Init.Found {
		showType, sliderContent = page.Init.ShowType, page.Init.Content
		s.logger().Debugf("[Captcha] challenge from window.init show_type=%q slider_len=%d", showType, len(sliderContent.Value))
	} else {
		initResp, initErr := s.captchaRequest("captchaNotRobot.initSession", [][2]string{
			{"session_token", captchaErr.SessionToken},
			{"domain", s.domain},
			{"lang", "0"},
		})
		if initErr != nil {
			return "", fmt.Errorf("captcha initSession failed: %w", initErr)
		}
		showType, sliderContent = parseCaptchaInitSession(initResp)
		s.logger().Debugf("[Captcha] initSession show_type=%q slider_len=%d", showType, len(sliderContent.Value))
	}

	base := s.captchaBaseValues(captchaErr.SessionToken)
	settingsResp, err := s.captchaRequest("captchaNotRobot.settings", base)
	if err != nil {
		return "", fmt.Errorf("captcha settings failed: %w", err)
	}
	s.sensors = parseSensorConfig(settingsResp)
	s.sensorsStart = time.Now()
	s.logger().Debugf("[Captcha] sensors delay=%s", s.sensors.delay)

	// Отрисовка виджета и первая реакция человека.
	if dwellErr := s.dwell(250, 400); dwellErr != nil {
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
	s.logger().Debugf("[Captcha] escalated to slider in-session (content source=%s)", content.Source)
	if err := s.dwell(500, 1100); err != nil {
		return "", err
	}
	return s.solveSliderCaptcha(sessionToken, content)
}

// clickReaction - пауза перед check: столько посетитель смотрит на виджет до
// нажатия. Медиана держится у живого эталона (3-4 тика телеметрии при
// sensors_delay=200мс), редкий хвост - на замешкавшегося; фиксированное окно
// само по себе отпечаток.
func (s *captchaSession) clickReaction() error {
	ms := 450 + randx.Intn(500)
	if randx.Intn(4) == 0 {
		ms += 700 + randx.Intn(1800)
	}
	return s.sleepFor(time.Duration(ms) * time.Millisecond)
}

// dwell - пауза [minMs, maxMs): массивы телеметрии обязаны биться с реальным
// временем сессии, поэтому её нельзя "нарисовать".
func (s *captchaSession) dwell(minMs, maxMs int) error {
	return s.sleepFor(time.Duration(minMs+randx.Intn(max(maxMs-minMs, 1))) * time.Millisecond)
}

func (s *captchaSession) sleepFor(d time.Duration) error {
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
	body, status, err := s.doRawStatus(fhttp.MethodGet, redirectURI, nil, map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		// Переход по редиректу считается пользовательской активацией.
		"Sec-Fetch-User": "?1",
		"Sec-Fetch-Site": "cross-site",
		// Кросс-сайтовый Referer режется до origin страницы звонка, откуда ушли.
		"Referer": "https://" + s.domain + "/",
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("%w: captcha page http %d (bytes=%d)", ErrUnavailable, status, len(body))
	}
	return string(body), nil
}

func parseCaptchaPage(html string) (*captchaPage, error) {
	// Заглушка VK (429/5xx) - временно; поломка парсера на настоящей странице - нет,
	// её надо эскалировать на следующий режим решения, а не ретраить вечно. Отличаем
	// по серверным глобалам: PoW-скрипт обфусцирован, переименование его конверта -
	// это поломка парсера, а не отсутствие страницы.
	if !reCaptchaVKGlobal.MatchString(html) && !reCaptchaInitGlobal.MatchString(html) {
		return nil, fmt.Errorf("%w: not a captcha page (bytes=%d)", ErrUnavailable, len(html))
	}
	pow, err := parsePowParams(html)
	if err != nil {
		return nil, err
	}
	debugInfo := parseCaptchaDebugInfo(html)
	if debugInfo == "" {
		return nil, errors.New("captcha debug_info not found on page")
	}
	return &captchaPage{Pow: pow, DebugInfo: debugInfo, Init: parseCaptchaInitGlobal(html)}, nil
}

func parseCaptchaDebugInfo(html string) string {
	m := reCaptchaVKGlobal.FindStringIndex(html)
	if m == nil {
		return ""
	}
	block := balancedJSONObject(html[m[1]-1:])
	if block == "" {
		return ""
	}
	found := reCaptchaDebugInfo.FindAllStringSubmatch(block, -1)
	if len(found) == 0 {
		return ""
	}
	if len(found) > 1 {
		Log().Warnf("[Captcha] window.vk holds %d uuid values, debug_info may be the wrong one", len(found))
	}
	return found[0][1]
}

// powSnippet - окно вокруг конверта PoW: при следующей смене разметки этого
// хватает, чтобы увидеть новый формат, не выгружая страницу целиком.
func powSnippet(html string) string {
	const window = 400
	i := strings.Index(html, "captchaPowResult")
	if i < 0 {
		return "<no captchaPowResult on page>"
	}
	return html[max(i-window, 0):min(i+window, len(html))]
}

// parseCaptchaInitGlobal вынимает window.init - серверный дамп, из которого
// виджет берёт хосты и challenge.
func parseCaptchaInitGlobal(html string) captchaInitData {
	m := reCaptchaInitGlobal.FindStringIndex(html)
	if m == nil {
		return captchaInitData{}
	}
	raw := balancedJSONObject(html[m[1]-1:])
	if raw == "" {
		return captchaInitData{}
	}
	var parsed struct {
		Hosts struct {
			API string `json:"api"`
		} `json:"hosts"`
		Data struct {
			ShowCaptchaType string               `json:"show_captcha_type"`
			CaptchaSettings []captchaInitSetting `json:"captcha_settings"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return captchaInitData{}
	}
	out := captchaInitData{
		Found:    parsed.Data.ShowCaptchaType != "",
		APIHost:  parsed.Hosts.API,
		ShowType: parsed.Data.ShowCaptchaType,
	}
	for _, setting := range parsed.Data.CaptchaSettings {
		if setting.Type == "slider" {
			out.Content = setting.contentRef()
		}
	}
	return out
}

// balancedJSONObject возвращает первый сбалансированный {...} с учётом строк.
func balancedJSONObject(s string) string {
	depth, inStr, esc := 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr && esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case inStr && c == '"':
			inStr = false
		case inStr:
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}

// setPageURL: битый redirect_uri откатывает на дефолтный origin виджета.
func (s *captchaSession) setPageURL(raw string) {
	s.pageURL = captchaAPIOrigin + "/"
	s.pageOrigin = captchaAPIOrigin
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}
	s.pageURL = raw
	s.pageOrigin = u.Scheme + "://" + u.Host
}

// pageIsAPIOrigin решает, что уйдёт в Referer: полный URL страницы (same-origin)
// или один origin (strict-origin-when-cross-origin).
func (s *captchaSession) pageIsAPIOrigin() bool {
	return s.pageOrigin == "https://"+s.apiHost
}

func (s *captchaSession) apiRequestHeaders() map[string]string {
	if s.pageIsAPIOrigin() {
		return map[string]string{
			"Origin":         s.pageOrigin,
			"Referer":        s.pageURL,
			"Sec-Fetch-Site": "same-origin",
		}
	}
	return map[string]string{
		"Origin":         s.pageOrigin,
		"Referer":        s.pageOrigin + "/",
		"Sec-Fetch-Site": "same-site",
	}
}

func (s *captchaSession) captchaRequest(method string, form [][2]string) (map[string]any, error) {
	endpoint := "https://" + s.apiHost + "/method/" + method + "?v=" + captchaAPIVersion
	body, err := s.doRaw(fhttp.MethodPost, endpoint, form, s.apiRequestHeaders())
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
) (*captchaCheck, error) {
	s.checked = true
	sinceSettings := time.Since(s.sensorsStart)
	data := buildAnalytics(s.sensors, s.downlink, sinceSettings)
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
	s.logger().Debugf("[Captcha] check payload answer_bytes=%d downlink_samples=%d since_settings=%s",
		len(answerJSON), len(data.connDownlink), sinceSettings.Truncate(time.Millisecond))
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
		{"browser_fp", s.browserFP},
		{"device", s.profile.DeviceJSON},
		{"access_token", ""},
	}); err != nil {
		return fmt.Errorf("captcha componentDone failed: %w", err)
	}
	return nil
}

func (s *captchaSession) solveCheckboxCaptcha(sessionToken string) (string, error) {
	if err := s.sendComponentDone(sessionToken); err != nil {
		return "", err
	}

	if err := s.clickReaction(); err != nil {
		return "", err
	}

	check, err := s.performCaptchaCheck(sessionToken, "{}")
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

func (s *captchaSession) doRaw(
	method string,
	endpoint string,
	form [][2]string,
	extraHeaders map[string]string,
) ([]byte, error) {
	data, _, err := s.doRawStatus(method, endpoint, form, extraHeaders)
	return data, err
}

func (s *captchaSession) doRawStatus(
	method string,
	endpoint string,
	form [][2]string,
	extraHeaders map[string]string,
) ([]byte, int, error) {
	var body []byte
	if form != nil {
		body = []byte(captchaEncodeForm(form))
	}
	req, err := fhttp.NewRequestWithContext(s.ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Referer", s.pageOrigin+"/")
	if form != nil {
		req.Header.Set("Origin", s.pageOrigin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	browserprofile.ApplyFhttp(req, s.profile)
	s.dumpExchange(req, body)

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		s.logger().Debugf("[Captcha] http %s %s failed t=%s after=%s %s form=%s err=%v", method, SafeURL(endpoint), s.elapsed(), time.Since(start).Truncate(time.Millisecond), navSummary(req), captchaFormSummary(form), err)
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger().Warnf("[Captcha] close body: %s", closeErr)
		}
	}()
	data, readErr := io.ReadAll(resp.Body)
	s.logger().Debugf("[Captcha] http %s %s status=%d bytes=%d t=%s after=%s %s form=%s", method, SafeURL(endpoint), resp.StatusCode, len(data), s.elapsed(), time.Since(start).Truncate(time.Millisecond), navSummary(req), captchaFormSummary(form))
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("%w: %w", ErrUnavailable, readErr)
	}
	return data, resp.StatusCode, nil
}

// Навигационный контекст: с какой страницы виджет якобы ходит в API. Формат
// общий с manual-прокси - эти две строки диффают между собой.
func NavSummary(dest, site, ref string) string {
	return fmt.Sprintf("dest=%s site=%s ref=%s", orDash(dest), orDash(site), SafeURL(ref))
}

func navSummary(req *fhttp.Request) string {
	return NavSummary(
		req.Header.Get("Sec-Fetch-Dest"),
		req.Header.Get("Sec-Fetch-Site"),
		req.Header.Get("Referer"))
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// Смещение от старта решения - по абсолютному времени не видно, укладывается ли captcha в полсекунды.
func (s *captchaSession) elapsed() string {
	return "+" + time.Since(s.started).Truncate(time.Millisecond).String()
}

func (s *captchaSession) dumpExchange(req *fhttp.Request, body []byte) {
	if !strings.Contains(req.URL.Path, "captchaNotRobot.check") &&
		!strings.Contains(req.URL.Path, "captchaNotRobot.componentDone") {
		return
	}
	s.logger().Debugf("[Captcha] we sent %s data: %s", req.URL.Path, string(body))
	for _, name := range req.Header[fhttp.HeaderOrderKey] {
		if v := req.Header.Get(name); v != "" {
			s.logger().Debugf("[Captcha] header (%s): %s = %s", req.URL.Path, name, v)
		}
	}
	if cookies := s.client.GetCookies(req.URL); len(cookies) > 0 {
		pairs := make([]string, 0, len(cookies))
		for _, c := range cookies {
			pairs = append(pairs, c.Name+"="+c.Value)
		}
		s.logger().Debugf("[Captcha] header (%s): cookie = %s", req.URL.Path, strings.Join(pairs, "; "))
	} else {
		s.logger().Debugf("[Captcha] header (%s): cookie = <none>", req.URL.Path)
	}
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

// SafeURL - host+path и имена query-параметров без значений: в значениях едет
// session_token, а различие страниц (variant, blank, expired_at) видно и по ключам.
func SafeURL(raw string) string {
	if raw == "" {
		return "-"
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := u.Host + path
	if q := u.Query(); len(q) > 0 {
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out += "?" + strings.Join(keys, ",")
	}
	return out
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
