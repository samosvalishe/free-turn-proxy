package browserprofile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	chromeSecChUa = `"Google Chrome";v="146", "Chromium";v="146", "Not)A;Brand";v="24"`

	uaWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	uaMac     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	uaAndroid = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Mobile Safari/537.36"
)

type Platform string

const (
	Desktop Platform = "desktop"
	Mobile  Platform = "mobile"
)

// PlatformFromString мапит строку флага -platform в Platform. Пустое/неизвестное -
// Desktop.
func PlatformFromString(s string) Platform {
	if s == string(Mobile) {
		return Mobile
	}
	return Desktop
}

// Profile - самосогласованная браузерная личность: единственный источник UA,
// client hints, device-fingerprint, порядка заголовков и набора JS-возможностей
// для VK control-plane и captcha. Строится один раз из Platform и Identity; не
// мутируется в рантайме. Семейство всегда Chrome: только Chromium даёт NetworkInformation и
// Generic Sensors, без которых телеметрия captcha неполна.
type Profile struct {
	Platform        Platform
	UserAgent       string
	SecChUa         string
	SecChUaMobile   string
	SecChUaPlatform string
	AcceptLanguage  string
	// Viewport - innerWidth/innerHeight персоны; из него же считаются координаты
	// указателя в телеметрии captcha.
	Viewport Size
	// DeviceJSON - navigator/screen fingerprint для captcha componentDone,
	// сериализованный из того же device, что дал Viewport.
	DeviceJSON string
	// VisitorID - идентификатор FingerprintJS этой личности.
	VisitorID string
}

type Size struct {
	W int
	H int
}

func (p Profile) IsMobile() bool { return p.Platform == Mobile }

// Touch - ввод пальцем вместо мыши: определяет, какой массив телеметрии captcha
// (taps против cursor) вообще может быть непустым.
func (p Profile) Touch() bool { return p.IsMobile() }

// Accelerometer - даст ли window.Accelerometer (Generic Sensor API) реальные
// показания: на десктопе сенсора нет.
func (p Profile) Accelerometer() bool { return p.IsMobile() }

// device - то, что виджет captcha собирает из navigator/screen. Порядок полей
// повторяет порядок ключей в объекте виджета, отсутствующие в браузере поля
// выпадают из JSON так же, как undefined выпадает у JSON.stringify.
type device struct {
	ScreenWidth             int      `json:"screenWidth"`
	ScreenHeight            int      `json:"screenHeight"`
	ScreenAvailWidth        int      `json:"screenAvailWidth"`
	ScreenAvailHeight       int      `json:"screenAvailHeight"`
	InnerWidth              int      `json:"innerWidth"`
	InnerHeight             int      `json:"innerHeight"`
	DevicePixelRatio        float64  `json:"devicePixelRatio"`
	Language                string   `json:"language"`
	Languages               []string `json:"languages"`
	Webdriver               bool     `json:"webdriver"`
	HardwareConcurrency     int      `json:"hardwareConcurrency"`
	DeviceMemory            *int     `json:"deviceMemory,omitempty"`
	ConnectionEffectiveType string   `json:"connectionEffectiveType,omitempty"`
	// NotificationsPermission - состояние permissions.query({name:"notifications"}).
	// Дефолт живого браузера - prompt; denied означало бы явный отказ пользователя.
	NotificationsPermission string `json:"notificationsPermission"`
}

// Порядок заголовков h2 для fetch/XHR. Отсутствующие в запросе имена fhttp
// пропускает, поэтому один список покрывает GET и POST.
var chromeHeaderOrder = []string{
	"content-length",
	"sec-ch-ua-platform",
	"user-agent",
	"sec-ch-ua",
	"content-type",
	"sec-ch-ua-mobile",
	"accept",
	"origin",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"priority",
}

// spec - варьируемая часть персоны: всё, чем реально различаются машины одной
// платформы. Остальное (Chrome-версия, язык, порядок заголовков) общее.
type spec struct {
	userAgent  string
	chPlatform string
	dev        device
}

var desktopSpecs = []spec{
	{userAgent: uaWindows, chPlatform: `"Windows"`, dev: newDevice(1920, 1080, 1032, 1147, 945, 1, 16, 8)},
	{userAgent: uaWindows, chPlatform: `"Windows"`, dev: newDevice(1536, 864, 816, 1229, 738, 1.25, 8, 8)},
	{userAgent: uaWindows, chPlatform: `"Windows"`, dev: newDevice(2560, 1440, 1392, 1512, 1237, 1, 12, 8)},
	{userAgent: uaMac, chPlatform: `"macOS"`, dev: newDevice(1512, 982, 944, 1147, 870, 2, 10, 8)},
}

// Chrome морозит мобильный UA до "Android 10; K" на всех устройствах, поэтому
// персоны различаются только железом.
var mobileSpecs = []spec{
	{userAgent: uaAndroid, chPlatform: `"Android"`, dev: newDevice(393, 852, 852, 393, 659, 3, 8, 8)},
	{userAgent: uaAndroid, chPlatform: `"Android"`, dev: newDevice(412, 915, 915, 412, 724, 2.625, 8, 4)},
	{userAgent: uaAndroid, chPlatform: `"Android"`, dev: newDevice(360, 800, 800, 360, 612, 3, 8, 4)},
	{userAgent: uaAndroid, chPlatform: `"Android"`, dev: newDevice(384, 832, 832, 384, 644, 2.75, 8, 8)},
}

// Identity - семя личности: одна и та же Identity всегда даёт один и тот же
// Profile, включая VisitorID.
type Identity struct {
	// Seed - стабильная строка установки: личность переживает перезапуск.
	Seed string
	// Gen растёт при сжигании персоны: отвергнутый captcha отпечаток не
	// переиспользуется, но и не меняется в середине сессии.
	Gen int
}

func (id Identity) String() string { return id.Seed + "|" + strconv.Itoa(id.Gen) }

// For строит персону для platform и identity. Неизвестная platform -> Desktop.
func For(p Platform, id Identity) Profile {
	specs := desktopSpecs
	if p == Mobile {
		specs = mobileSpecs
	} else {
		p = Desktop
	}

	sum := sha256.Sum256([]byte(id.String()))
	s := specs[binary.BigEndian.Uint64(sum[:8])%uint64(len(specs))]

	profile := Profile{
		Platform:        p,
		SecChUa:         chromeSecChUa,
		SecChUaMobile:   "?0",
		SecChUaPlatform: s.chPlatform,
		AcceptLanguage:  "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
		UserAgent:       s.userAgent,
	}
	if p == Mobile {
		profile.SecChUaMobile = "?1"
	}

	profile = withDevice(profile, s.dev)
	profile.VisitorID = visitorID(id, profile)
	return profile
}

// newDevice дополняет варьируемые поля общими для всех персон.
func newDevice(screenW, screenH, availH, innerW, innerH int, dpr float64, cores, memGB int) device {
	memory := memGB
	return device{
		ScreenWidth: screenW, ScreenHeight: screenH,
		ScreenAvailWidth: screenW, ScreenAvailHeight: availH,
		InnerWidth: innerW, InnerHeight: innerH,
		DevicePixelRatio:    dpr,
		Language:            "ru-RU",
		Languages:           []string{"ru-RU", "ru", "en-US", "en"},
		Webdriver:           false,
		HardwareConcurrency: cores,
		DeviceMemory:        &memory,
		ConnectionEffectiveType: "4g",
		NotificationsPermission: "denied",
	}
}

// visitorID - слот FingerprintJS: у живого посетителя стабилен между визитами,
// поэтому выводится из личности целиком, а не генерится на попытку.
func visitorID(id Identity, p Profile) string {
	sum := sha256.Sum256([]byte(id.String() + "|" + p.UserAgent + "|" + p.DeviceJSON))
	return hex.EncodeToString(sum[:16])
}

func withDevice(p Profile, d device) Profile {
	data, err := json.Marshal(d)
	if err != nil {
		return p
	}
	p.Viewport = Size{W: d.InnerWidth, H: d.InnerHeight}
	p.DeviceJSON = string(data)
	return p
}

// ClientProfile - TLS/HTTP2-отпечаток персоны (JA3 + ALPN + h2 settings + порядок
// псевдозаголовков). Chrome использует один TLS-стек на всех платформах, поэтому
// platform на него не влияет.
func (Profile) ClientProfile() profiles.ClientProfile {
	return profiles.Chrome_146
}

func ApplyFhttp(req *fhttp.Request, profile Profile) {
	req.Header.Set("User-Agent", profile.UserAgent)
	req.Header.Set("sec-ch-ua", profile.SecChUa)
	req.Header.Set("sec-ch-ua-mobile", profile.SecChUaMobile)
	req.Header.Set("sec-ch-ua-platform", profile.SecChUaPlatform)
	req.Header.Set("Accept-Language", profile.AcceptLanguage)
	req.Header.Set("Priority", "u=1, i")
	req.Header[fhttp.HeaderOrderKey] = chromeHeaderOrder
}

func ApplyProxyFhttp(req *fhttp.Request) {
	req.Header[fhttp.HeaderOrderKey] = chromeHeaderOrder
}
