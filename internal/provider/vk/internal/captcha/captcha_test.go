package captcha

import (
	"context"
	"strings"
	"testing"
)

func TestCaptchaInitSettingContentRefPrefersSettingsKey(t *testing.T) {
	setting := captchaInitSetting{
		Type:        "slider",
		Settings:    "legacy-settings",
		SettingsKey: "new-settings-key",
	}

	got := setting.contentRef()
	if got.Source != "settings_key" || got.Value != "new-settings-key" {
		t.Fatalf("contentRef = %+v, want settings_key/new-settings-key", got)
	}
}

func TestCaptchaInitSettingContentRefLegacySettings(t *testing.T) {
	setting := captchaInitSetting{
		Type:     "slider",
		Settings: "legacy-settings",
	}

	got := setting.contentRef()
	if got.Source != "captcha_settings" || got.Value != "legacy-settings" {
		t.Fatalf("contentRef = %+v, want captcha_settings/legacy-settings", got)
	}
}

func TestParseCaptchaPageSPA(t *testing.T) {
	html := `<html><head><script>
const powInput = "Pihj7tyAHFxdwm4t";
const difficulty = 2;
</script>
<script src="https://static.vk.ru/vkid/1.1.1384/not_robot_captcha.js"></script>
</head><body><div id="spa_root"></div></body></html>`

	page, err := parseCaptchaPage(html)
	if err != nil {
		t.Fatal(err)
	}
	if page.PowInput != "Pihj7tyAHFxdwm4t" || page.PowDifficulty != 2 {
		t.Fatalf("pow parse = %q/%d", page.PowInput, page.PowDifficulty)
	}
	if page.ScriptURL != "https://static.vk.ru/vkid/1.1.1384/not_robot_captcha.js" {
		t.Fatalf("script url = %q", page.ScriptURL)
	}
}

func TestParseCaptchaPageMissingPoW(t *testing.T) {
	if _, err := parseCaptchaPage(`<html><body><div id="spa_root"></div></body></html>`); err == nil {
		t.Fatal("expected error when powInput/difficulty absent")
	}
}

func TestSolveCaptchaPoWRawHex(t *testing.T) {
	got := solveCaptchaPoW(context.Background(), "input", 1)
	if !strings.HasPrefix(got, "v2.") {
		t.Fatalf("pow = %q, want v2. prefix", got)
	}
	// Note: output is no longer deterministic because it contains time duration.
}

func TestParseCaptchaInitSession(t *testing.T) {
	raw := map[string]any{"response": map[string]any{
		"show_captcha_type": "slider",
		"captcha_id":        "cid",
		"content_settings": []any{
			map[string]any{"type": "slider", "settings_key": "sliderkey"},
			map[string]any{"type": "sound", "settings_key": "soundkey"},
		},
	}}
	showType, content := parseCaptchaInitSession(raw)
	if showType != "slider" {
		t.Fatalf("show_type = %q, want slider", showType)
	}
	if content.Value != "sliderkey" || content.Source != "settings_key" {
		t.Fatalf("content = %+v, want sliderkey/settings_key", content)
	}
}

func TestParseCaptchaInitSessionCheckbox(t *testing.T) {
	raw := map[string]any{"response": map[string]any{
		"show_captcha_type": "checkbox",
		"content_settings": []any{
			map[string]any{"type": "slider", "settings_key": "k"},
		},
	}}
	showType, _ := parseCaptchaInitSession(raw)
	if showType != "checkbox" {
		t.Fatalf("show_type = %q, want checkbox", showType)
	}
}

func TestCaptchaDomainFromRedirectURI(t *testing.T) {
	tests := []struct {
		name        string
		redirectURI string
		want        string
	}{
		{
			name:        "vk com from query",
			redirectURI: "https://id.vk.ru/not_robot_captcha?domain=vk.com&session_token=x",
			want:        "vk.com",
		},
		{
			name:        "vk ru from query",
			redirectURI: "https://id.vk.ru/not_robot_captcha?domain=vk.ru&session_token=x",
			want:        "vk.ru",
		},
		{
			name:        "fallback without domain",
			redirectURI: "https://id.vk.ru/not_robot_captcha?session_token=x",
			want:        captchaDomain,
		},
		{
			name:        "fallback invalid url",
			redirectURI: "%",
			want:        captchaDomain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captchaDomainFromRedirectURI(tt.redirectURI); got != tt.want {
				t.Fatalf("domain = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReverseSwapPairs(t *testing.T) {
	got := reverseSwapPairs([]int{1, 2, 3, 4, 5, 6})
	want := []int{5, 6, 3, 4, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reverseSwapPairs = %v, want %v", got, want)
		}
	}
}
