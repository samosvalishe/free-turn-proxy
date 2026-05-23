package static

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samosvalishe/btp/internal/provider"
)

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty user", Config{Pass: "p", Addr: "h:1"}, "User"},
		{"empty pass", Config{User: "u", Addr: "h:1"}, "Pass"},
		{"empty addr", Config{User: "u", Pass: "p"}, "Addr"},
		{"addr without port", Config{User: "u", Pass: "p", Addr: "host"}, "host:port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestProvider_GetCredentials(t *testing.T) {
	t.Parallel()
	p, err := New(Config{User: "alice", Pass: "secret", Addr: "turn.example.com:3478"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.GetCredentials(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Credentials{User: "alice", Pass: "secret", ServerAddr: "turn.example.com:3478"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestProvider_InterfaceContract(t *testing.T) {
	t.Parallel()
	p, _ := New(Config{User: "u", Pass: "p", Addr: "h:1"})
	if p.IsAuthError(errors.New("anything")) {
		t.Error("IsAuthError should always return false")
	}
	if p.HandleAuthError(0) {
		t.Error("HandleAuthError should always return false")
	}
	p.ResetErrors(0)
	if p.BackoffUntilUnix() != 0 {
		t.Error("BackoffUntilUnix should always return 0")
	}
	if p.Name() != "static" {
		t.Errorf("Name=%q, want static", p.Name())
	}
	var _ provider.Provider = p
}
