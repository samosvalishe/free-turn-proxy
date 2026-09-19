package uri

import (
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "full",
			cfg: &Config{
				Version:        currentVersion,
				Provider:       "vk",
				Peer:           "127.0.0.1:56000",
				Transport:      "udp",
				Mode:           "tcp",
				ObfProfile:     "rtpopus",
				ObfKey:         "d823fa",
				ObfTimingMs:    20,
				N:              16,
				StreamsPerCred: 8,
				ClientID:       "abc123",
				Listen:         "127.0.0.1:9000",
				DNSMode:        "doh",
				DNSServers:     "1.1.1.1,8.8.8.8",
				ManualCaptcha:  true,
				Comment:        "MyServer",
				VKLink:         "https://vk.ru/call/join/abc",
			},
		},
		{
			name: "minimal",
			cfg: &Config{
				Version:  currentVersion,
				Provider: "vk",
				Peer:     "1.2.3.4:56000",
			},
		},
		{
			name: "unicode comment",
			cfg: &Config{
				Version:  currentVersion,
				Provider: "vk",
				Peer:     "1.2.3.4:56000",
				Comment:  "Сервер РФ",
			},
		},
		{
			name: "with wg conf",
			cfg: &Config{
				Version:  currentVersion,
				Provider: "vk",
				Peer:     "1.2.3.4:56000",
				Comment:  "VPN Server",
				WGConf:   "[Interface]\nPrivateKey = test\n[Peer]\nPublicKey = test\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.cfg.String())
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.cfg) {
				t.Errorf("round-trip mismatch\n got = %+v\nwant = %+v", got, tt.cfg)
			}
		})
	}
}

func TestObfNoneOmitted(t *testing.T) {
	cfg := &Config{
		Version:    currentVersion,
		Provider:   "vk",
		Peer:       "1.2.3.4:56000",
		ObfProfile: "none",
		ObfKey:     "ignored",
	}
	got, err := Parse(cfg.String())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.ObfProfile != "" || got.ObfKey != "" {
		t.Errorf("obf none must be omitted, got profile=%q key=%q", got.ObfProfile, got.ObfKey)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bad scheme", "http://vk"},
		{"empty payload", "freeturn://"},
		{"bad base64", "freeturn://!!!notbase64!!!"},
		{"bad json", "freeturn://Zm9vYmFy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.input); err == nil {
				t.Errorf("Parse(%q) expected error, got nil", tt.input)
			}
		})
	}
}

// Ссылка без mode - от старого клиента; поле optional, режим остаётся дефолтным.
func TestParseLegacyLinkWithoutMode(t *testing.T) {
	legacy := (&Config{Version: currentVersion, Provider: "vk", Peer: "1.2.3.4:56000"}).String()
	got, err := Parse(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "" {
		t.Errorf("Mode = %q, want empty", got.Mode)
	}
}

// Владелец мог затюнить ARQ на сервере - гостю нужен тот же профиль, иначе разъедутся MTU.
func TestKCPRoundTrip(t *testing.T) {
	t.Parallel()

	in := &Config{
		Version:  currentVersion,
		Provider: "vk",
		Peer:     "1.2.3.4:56000",
		Mode:     "tcp",
		KCP:      &KCP{NoDelay: 1, Interval: 40, Resend: 2, NC: 1, SndWnd: 256, RcvWnd: 256, MTU: 1100},
	}

	out, err := Parse(in.String())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if out.KCP == nil {
		t.Fatal("KCP dropped by round-trip")
	}
	if *out.KCP != *in.KCP {
		t.Errorf("KCP = %+v, want %+v", *out.KCP, *in.KCP)
	}
}

// Ссылка без kcp - от старого клиента: профиль остаётся дефолтным, поле опционально.
func TestKCPAbsentStaysNil(t *testing.T) {
	t.Parallel()

	link := (&Config{Version: currentVersion, Provider: "vk", Peer: "1.2.3.4:56000"}).String()
	if strings.Contains(link, "kcp") {
		t.Error("empty KCP must not be serialized")
	}
	out, err := Parse(link)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if out.KCP != nil {
		t.Errorf("KCP = %+v, want nil", out.KCP)
	}
}
