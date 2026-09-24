package captcha

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
)

// Живая страница captcha, снятая целиком: PoW-скрипт обфусцирован, поэтому
// контракт проверяется на настоящей разметке, а не на её пересказе. Обновлять
// фикстуру - task captcha:page.
func TestParseCaptchaPageFixture(t *testing.T) {
	html, err := os.ReadFile("testdata/not_robot_captcha.html")
	if err != nil {
		t.Fatal(err)
	}

	page, err := parseCaptchaPage(string(html))
	if err != nil {
		t.Fatal(err)
	}
	if page.Pow.Input != "ST9BEClStK0wSdo4" || page.Pow.Difficulty != 2 || page.Pow.Prefix != "v2." {
		t.Fatalf("pow = %+v", page.Pow)
	}
	if page.DebugInfo != "273cc83f-426f-4d98-9ce5-92490107e3a6" {
		t.Fatalf("debug_info = %q", page.DebugInfo)
	}
	if page.Init.Found {
		t.Fatalf("init = %+v", page.Init)
	}
}

// Незнакомая разметка обязана ронять авторешение, а не уезжать в check заведомо
// неверным hash.
func TestParsePowParamsRejectsUnknownMarkup(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{name: "no pow script", html: `<html><body><div id="spa_root"></div></body></html>`},
		{
			name: "args without envelope",
			html: `<script>(function(a,b,c){})("fnZQN7lKKXvo37tH",2,"pow_timeout"));</script>`,
		},
		{
			name: "envelope without args",
			html: `<script>window['captchaPowResult']='v2.'+btoa(x);}(powInput,difficulty,"pow_timeout"));</script>`,
		},
		{
			name: "zero difficulty",
			html: `<script>window['captchaPowResult']='v2.'+btoa(x);}("fnZQN7lKKXvo37tH",0,"pow_timeout"));</script>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parsePowParams(tt.html); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParsePowParamsAcceptsAdditionalArgs(t *testing.T) {
	html := `<script>window['captchaPowResult']='v2.'+btoa(x);}('fnZQN7lKKXvo37tH',2,'pow_timeout',["globals"]));</script>`

	got, err := parsePowParams(html)
	if err != nil {
		t.Fatal(err)
	}
	if got.Input != "fnZQN7lKKXvo37tH" || got.Difficulty != 2 || got.Prefix != "v2." {
		t.Fatalf("pow = %+v", got)
	}
}

func TestSolvePoW(t *testing.T) {
	got, nonce := solvePoW(context.Background(), "input", 1)
	if len(got) != 64 || !strings.HasPrefix(got, "0") {
		t.Fatalf("pow = %q, want 64-hex with leading zero", got)
	}
	sum := sha256.Sum256([]byte("input" + strconv.Itoa(nonce)))
	if hex.EncodeToString(sum[:]) != got {
		t.Fatalf("pow hash does not match sha256(input+nonce)")
	}
}

// Эталон снят с персоны seed=fixture; tel_hash сверен с канонизатором самой
// страницы. Любая правка значений телеметрии обязана осознанно двигать эталон.
const (
	goldenTelemetry = `{"globals":{"ok":true,"result":{"doc":true,"win":true,"nav":true,"webdriver":false,"subtle":true,"secure":true,"gcs":true,"raf":true,"wasm":true,"plugins_len":5,"languages_len":2,"hw":12,"mem":8}},` +
		`"ua":{"ok":true,"result":{"userAgent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36","userAgentData":{"brands":[{"brand":"Google Chrome","version":"146"},{"brand":"Chromium","version":"146"},{"brand":"Not)A;Brand","version":"24"}],"platform":"Windows","mobile":false,"architecture":null}}},` +
		`"frame":{"ok":true,"result":{"frameElement":null,"ancestorOriginsLen":0,"parentAccessible":true}},` +
		`"match_media":{"ok":true,"result":{"prefersDark":true,"prefersLight":false,"reducedMotion":false,"pointerFine":true}},` +
		`"plugins":{"ok":true,"result":{"length":5,"names":["PDF Viewer","Chrome PDF Viewer","Chromium PDF Viewer","Microsoft Edge PDF Viewer","WebKit built-in PDF"],"descriptions":["Portable Document Format","Portable Document Format","Portable Document Format","Portable Document Format","Portable Document Format"],"mimeTypes":[["application/pdf","text/pdf"],["application/pdf","text/pdf"],["application/pdf","text/pdf"],["application/pdf","text/pdf"],["application/pdf","text/pdf"]],"isChrome":true}},` +
		`"nav_tamper":{"ok":true,"result":{"tampered":false,"el_ctor":"HTMLDivElement","style_ctor":"CSSStyleDeclaration","nav_ctor":"Navigator","alert_native":true,"to_string_native":true}},` +
		`"referrer":{"ok":true,"result":{"referrer":"https://vk.com/","inIframe":false,"domain":"id.vk.ru"}},` +
		`"devtools":{"ok":true,"result":{"open":false,"delay_ms":0}},` +
		`"css":{"ok":true,"result":{"expectedMissing":0}},` +
		`"native_integrity":{"ok":true,"result":{"protoMatch":true,"xhrNative":true,"xhrSendNative":true,"addEventListenerNative":true,"alertNative":true,"toStringNative":true}},` +
		`"cookie_test":{"ok":true,"result":{"write":true}},` +
		`"ancestor_origins":{"ok":true,"result":{"ancestorOrigin":null}},` +
		`"sandbox_behavior":{"ok":true,"result":{"originIsNull":false,"localStorage":true,"sessionStorage":true}},` +
		`"max_touch_points":{"ok":true,"result":{"maxTouchPoints":0}},` +
		`"timezone_locale":{"ok":true,"result":{"timezone":"Europe/Moscow","languages":["ru-RU","en-US"]}},` +
		`"device_pixel_ratio":{"ok":true,"result":{"dpr":1,"orientation":"landscape-primary","orientationAngle":0}}}`

	goldenTelHash = "838102524be656361f2f88dba3dd16d3baaa20b5b33251b472b365ff646b99e9"
)

// Конверт целиком: порядок ключей, телеметрия персоны и её sha256 - всё, что VK
// проверяет на check.
func TestPowEnvelope(t *testing.T) {
	s := &captchaSession{
		ctx:        context.Background(),
		profile:    browserprofile.For(browserprofile.Desktop, browserprofile.Identity{Seed: "fixture"}),
		domain:     "vk.com",
		pageOrigin: "https://id.vk.ru",
	}

	got, err := s.powEnvelope(powParams{Input: "input", Difficulty: 1, Prefix: "v2."})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := strings.CutPrefix(got, "v2.")
	if !ok {
		t.Fatalf("envelope = %q, want v2. prefix", got)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}

	hash, nonce := solvePoW(context.Background(), "input", 1)
	want := `{"hash":"` + hash + `","nonce":` + strconv.Itoa(nonce) +
		`,"duration_ms":` + strconv.FormatInt(powDurationMs(nonce), 10) +
		`,"telemetry":` + goldenTelemetry + `,"tel_hash":"` + goldenTelHash + `"}`
	if string(raw) != want {
		t.Fatalf("envelope =\n%s\nwant\n%s", raw, want)
	}
}

func TestTelemetryHashCanonicalizesKeys(t *testing.T) {
	// Тот же объект с переставленными ключами обязан дать тот же tel_hash.
	shuffled := `{"b":{"y":1,"x":[2,{"n":null,"m":"s"}]},"a":true}`
	ordered := `{"a":true,"b":{"x":[2,{"m":"s","n":null}],"y":1}}`

	first, err := telemetryHash([]byte(shuffled))
	if err != nil {
		t.Fatal(err)
	}
	second, err := telemetryHash([]byte(ordered))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("tel_hash = %s vs %s", first, second)
	}
	sum := sha256.Sum256([]byte(ordered))
	if first != hex.EncodeToString(sum[:]) {
		t.Fatalf("tel_hash = %s, want sha256 of canonical form", first)
	}
}
