package uri

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Config
		wantErr bool
	}{
		{
			name:  "Full URI",
			input: "freeturn://vk?tcp<mode=tcpfwd&obf-profile=rtpopus&bond=true>@127.0.0.1:56000#d823fa$MyServer",
			want: &Config{
				Provider:   "vk",
				Transport:  "tcp",
				Mode:       "tcpfwd",
				Bond:       true,
				ObfProfile: "rtpopus",
				Peer:       "127.0.0.1:56000",
				ObfKey:     "d823fa",
				Comment:    "MyServer",
			},
			wantErr: false,
		},
		{
			name:  "Minimal URI",
			input: "freeturn://vk@1.2.3.4",
			want: &Config{
				Provider: "vk",
				Peer:     "1.2.3.4",
			},
			wantErr: false,
		},
		{
			name:    "Invalid scheme",
			input:   "http://vk",
			wantErr: true,
		},
		{
			name:    "Empty provider",
			input:   "freeturn://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	cfg := &Config{
		Provider:   "vk",
		Transport:  "udp",
		Mode:       "udp",
		ObfProfile: "rtpopus",
		Peer:       "1.1.1.1:56000",
		ObfKey:     "abc",
		Comment:    "comment",
	}

	expected := "freeturn://vk?udp<mode=udp&obf-profile=rtpopus>@1.1.1.1:56000#abc$comment"
	if got := cfg.String(); got != expected {
		t.Errorf("String() = %v, want %v", got, expected)
	}
}
