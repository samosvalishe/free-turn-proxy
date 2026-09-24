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
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
)

const powResultGlobal = "captchaPowResult"

var (
	rePowArgs   = regexp.MustCompile(`\}\(\s*["']([A-Za-z0-9_-]{8,})["']\s*,\s*(\d+)\s*,\s*["'][^"']*["']\s*(?:,|\)\s*\))`)
	rePowPrefix = regexp.MustCompile(powResultGlobal + `["'\]]{0,3}\s*=\s*["']([A-Za-z0-9._-]{0,8})["']\s*\+`)
)

type powParams struct {
	Input      string
	Difficulty int
	Prefix     string
}

// powResult - конверт window.captchaPowResult: виджету уходит не голый хэш.
// Порядок полей повторяет порядок ключей у JSON.stringify страницы.
type powResult struct {
	Hash       string          `json:"hash"`
	Nonce      int             `json:"nonce"`
	DurationMs int64           `json:"duration_ms"`
	Telemetry  json.RawMessage `json:"telemetry"`
	TelHash    string          `json:"tel_hash"`
}

func parsePowParams(html string) (powParams, error) {
	m := rePowArgs.FindStringSubmatch(html)
	if len(m) < 3 {
		return powParams{}, errors.New("captcha pow args not found")
	}
	difficulty, err := strconv.Atoi(m[2])
	if err != nil || difficulty <= 0 {
		return powParams{}, fmt.Errorf("invalid captcha difficulty %q", m[2])
	}
	prefix := rePowPrefix.FindStringSubmatch(html)
	if len(prefix) < 2 {
		return powParams{}, errors.New("captcha pow envelope not recognized")
	}
	return powParams{Input: m[1], Difficulty: difficulty, Prefix: prefix[1]}, nil
}

func (s *captchaSession) powEnvelope(p powParams) (string, error) {
	hash, nonce := solvePoW(s.ctx, p.Input, p.Difficulty)
	if hash == "" {
		return "", errors.New("captcha pow failed")
	}
	telemetry, err := marshalJS(s.powTelemetry())
	if err != nil {
		return "", fmt.Errorf("captcha pow telemetry: %w", err)
	}
	telHash, err := telemetryHash(telemetry)
	if err != nil {
		return "", fmt.Errorf("captcha pow tel_hash: %w", err)
	}
	envelope, err := marshalJS(powResult{
		Hash:       hash,
		Nonce:      nonce,
		DurationMs: powDurationMs(nonce),
		Telemetry:  telemetry,
		TelHash:    telHash,
	})
	if err != nil {
		return "", fmt.Errorf("captcha pow encode: %w", err)
	}
	// Печатаем до base64: диффать с живым браузером (см. DecodePowEnvelope) иначе нечем.
	s.logger().Debugf("[Captcha] pow envelope: %s", envelope)
	return p.Prefix + base64.StdEncoding.EncodeToString(envelope), nil
}

// DecodePowEnvelope разворачивает значение window.captchaPowResult обратно в JSON.
func DecodePowEnvelope(value string) string {
	payload := value
	if _, rest, ok := strings.Cut(value, "."); ok {
		payload = rest
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func solvePoW(ctx context.Context, input string, difficulty int) (string, int) {
	if input == "" || difficulty <= 0 {
		return "", 0
	}
	target := strings.Repeat("0", difficulty)
	buf := make([]byte, 0, len(input)+20)
	buf = append(buf, input...)
	for nonce := 0; nonce <= 10_000_000; nonce++ {
		if nonce&1023 == 0 {
			select {
			case <-ctx.Done():
				return "", 0
			default:
			}
		}
		buf = strconv.AppendInt(buf[:len(input)], int64(nonce), 10)
		sum := sha256.Sum256(buf)
		hashHex := hex.EncodeToString(sum[:])
		if strings.HasPrefix(hashHex, target) {
			return hashHex, nonce
		}
	}
	return "", 0
}

// Цикл в браузере синхронный: nonce 53 -> 1 мс, 267 -> 4 мс.
func powDurationMs(nonce int) int64 {
	return max(1, int64(math.Round(float64(nonce+1)*0.015)))
}

// telemetryHash повторяет канонизатор страницы: ключи по алфавиту (map в Go
// сортирует их сам), undefined на нашей стороне не бывает.
func telemetryHash(telemetry []byte) (string, error) {
	var v any
	if err := json.Unmarshal(telemetry, &v); err != nil {
		return "", err
	}
	canonical, err := marshalJS(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// marshalJS - JSON без HTML-экранирования: JSON.stringify не трогает < > &.
func marshalJS(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// powTelemetryData - пробы PoW-скрипта; порядок полей = порядок проб, так их
// пишет JSON.stringify.
type powTelemetryData struct {
	Globals          powProbe `json:"globals"`
	UA               powProbe `json:"ua"`
	Frame            powProbe `json:"frame"`
	MatchMedia       powProbe `json:"match_media"`
	Plugins          powProbe `json:"plugins"`
	NavTamper        powProbe `json:"nav_tamper"`
	Referrer         powProbe `json:"referrer"`
	DevTools         powProbe `json:"devtools"`
	CSS              powProbe `json:"css"`
	NativeIntegrity  powProbe `json:"native_integrity"`
	CookieTest       powProbe `json:"cookie_test"`
	AncestorOrigins  powProbe `json:"ancestor_origins"`
	SandboxBehavior  powProbe `json:"sandbox_behavior"`
	MaxTouchPoints   powProbe `json:"max_touch_points"`
	TimezoneLocale   powProbe `json:"timezone_locale"`
	DevicePixelRatio powProbe `json:"device_pixel_ratio"`
}

type powProbe struct {
	OK     bool `json:"ok"`
	Result any  `json:"result"`
}

func probe(result any) powProbe { return powProbe{OK: true, Result: result} }

type powGlobals struct {
	Doc          bool `json:"doc"`
	Win          bool `json:"win"`
	Nav          bool `json:"nav"`
	Webdriver    bool `json:"webdriver"`
	Subtle       bool `json:"subtle"`
	Secure       bool `json:"secure"`
	GCS          bool `json:"gcs"`
	RAF          bool `json:"raf"`
	Wasm         bool `json:"wasm"`
	PluginsLen   int  `json:"plugins_len"`
	LanguagesLen int  `json:"languages_len"`
	HW           int  `json:"hw"`
	Mem          *int `json:"mem"`
}

type powUA struct {
	UserAgent     string     `json:"userAgent"`
	UserAgentData *powUAData `json:"userAgentData"`
}

type powUAData struct {
	Brands       []browserprofile.Brand `json:"brands"`
	Platform     string                 `json:"platform"`
	Mobile       bool                   `json:"mobile"`
	Architecture *string                `json:"architecture"`
}

type powFrame struct {
	FrameElement       *string `json:"frameElement"`
	AncestorOriginsLen int     `json:"ancestorOriginsLen"`
	ParentAccessible   bool    `json:"parentAccessible"`
}

type powMatchMedia struct {
	PrefersDark   bool `json:"prefersDark"`
	PrefersLight  bool `json:"prefersLight"`
	ReducedMotion bool `json:"reducedMotion"`
	PointerFine   bool `json:"pointerFine"`
}

type powPlugins struct {
	Length       int        `json:"length"`
	Names        []string   `json:"names"`
	Descriptions []string   `json:"descriptions"`
	MimeTypes    [][]string `json:"mimeTypes"`
	IsChrome     bool       `json:"isChrome"`
}

type powNavTamper struct {
	Tampered       bool   `json:"tampered"`
	ElCtor         string `json:"el_ctor"`
	StyleCtor      string `json:"style_ctor"`
	NavCtor        string `json:"nav_ctor"`
	AlertNative    bool   `json:"alert_native"`
	ToStringNative bool   `json:"to_string_native"`
}

type powReferrer struct {
	Referrer string `json:"referrer"`
	InIframe bool   `json:"inIframe"`
	Domain   string `json:"domain"`
}

type powDevTools struct {
	Open    bool `json:"open"`
	DelayMs int  `json:"delay_ms"`
}

type powCSS struct {
	ExpectedMissing int `json:"expectedMissing"`
}

type powNativeIntegrity struct {
	ProtoMatch             bool `json:"protoMatch"`
	XHRNative              bool `json:"xhrNative"`
	XHRSendNative          bool `json:"xhrSendNative"`
	AddEventListenerNative bool `json:"addEventListenerNative"`
	AlertNative            bool `json:"alertNative"`
	ToStringNative         bool `json:"toStringNative"`
}

type powCookieTest struct {
	Write bool `json:"write"`
}

type powAncestorOrigins struct {
	AncestorOrigin *string `json:"ancestorOrigin"`
}

type powSandboxBehavior struct {
	OriginIsNull   bool `json:"originIsNull"`
	LocalStorage   bool `json:"localStorage"`
	SessionStorage bool `json:"sessionStorage"`
}

type powMaxTouchPoints struct {
	MaxTouchPoints int `json:"maxTouchPoints"`
}

type powTimezoneLocale struct {
	Timezone  string   `json:"timezone"`
	Languages []string `json:"languages"`
}

type powDevicePixelRatio struct {
	DPR              float64 `json:"dpr"`
	Orientation      string  `json:"orientation"`
	OrientationAngle int     `json:"orientationAngle"`
}

var chromePDFPlugins = []string{
	"PDF Viewer",
	"Chrome PDF Viewer",
	"Chromium PDF Viewer",
	"Microsoft Edge PDF Viewer",
	"WebKit built-in PDF",
}

const chromePDFDescription = "Portable Document Format"

var chromePDFMimeTypes = []string{"application/pdf", "text/pdf"}

func chromePlugins() powPlugins {
	out := powPlugins{
		Length:       len(chromePDFPlugins),
		Names:        chromePDFPlugins,
		Descriptions: make([]string, len(chromePDFPlugins)),
		MimeTypes:    make([][]string, len(chromePDFPlugins)),
		IsChrome:     true,
	}
	for i := range chromePDFPlugins {
		out.Descriptions[i] = chromePDFDescription
		out.MimeTypes[i] = chromePDFMimeTypes
	}
	return out
}

// powTelemetry описывает страницу такой, какой её видел бы браузер персоны:
// страницу мы тянем верхнеуровневой навигацией (Sec-Fetch-Dest: document),
// поэтому frame и referrer описывают top-level документ - врозь их менять нельзя.
func (s *captchaSession) powTelemetry() powTelemetryData {
	p := s.profile
	// На Android нет встроенного PDF-вьювера, значит и списка плагинов.
	plugins := powPlugins{Names: []string{}, Descriptions: []string{}, MimeTypes: [][]string{}}
	if !p.IsMobile() {
		plugins = chromePlugins()
	}
	dark := prefersDark(p)
	return powTelemetryData{
		Globals: probe(powGlobals{
			Doc: true, Win: true, Nav: true,
			Webdriver:    p.Webdriver(),
			Subtle:       true,
			Secure:       true,
			GCS:          true,
			RAF:          true,
			Wasm:         true,
			PluginsLen:   plugins.Length,
			LanguagesLen: len(p.Languages()),
			HW:           p.HardwareConcurrency(),
			Mem:          p.DeviceMemory(),
		}),
		UA: probe(powUA{
			UserAgent: p.UserAgent,
			UserAgentData: &powUAData{
				Brands:   p.Brands(),
				Platform: p.PlatformName(),
				Mobile:   p.IsMobile(),
			},
		}),
		Frame:      probe(powFrame{ParentAccessible: true}),
		MatchMedia: probe(powMatchMedia{PrefersDark: dark, PrefersLight: !dark, PointerFine: !p.IsMobile()}),
		Plugins:    probe(plugins),
		NavTamper: probe(powNavTamper{
			ElCtor: "HTMLDivElement", StyleCtor: "CSSStyleDeclaration", NavCtor: "Navigator",
			AlertNative: true, ToStringNative: true,
		}),
		Referrer: probe(powReferrer{
			Referrer: "https://" + s.domain + "/",
			Domain:   strings.TrimPrefix(s.pageOrigin, "https://"),
		}),
		DevTools: probe(powDevTools{}),
		CSS:      probe(powCSS{}),
		NativeIntegrity: probe(powNativeIntegrity{
			ProtoMatch: true, XHRNative: true, XHRSendNative: true,
			AddEventListenerNative: true, AlertNative: true, ToStringNative: true,
		}),
		CookieTest:      probe(powCookieTest{Write: true}),
		AncestorOrigins: probe(powAncestorOrigins{}),
		SandboxBehavior: probe(powSandboxBehavior{LocalStorage: true, SessionStorage: true}),
		MaxTouchPoints:  probe(powMaxTouchPoints{MaxTouchPoints: p.MaxTouchPoints()}),
		TimezoneLocale:  probe(powTimezoneLocale{Timezone: p.Timezone(), Languages: p.Languages()}),
		DevicePixelRatio: probe(powDevicePixelRatio{
			DPR:         p.DevicePixelRatio(),
			Orientation: p.Orientation(),
		}),
	}
}

// prefers-color-scheme у живого посетителя не скачет между сессиями - берём бит персоны.
func prefersDark(p browserprofile.Profile) bool {
	return len(p.VisitorID) > 0 && p.VisitorID[0]%2 == 1
}
