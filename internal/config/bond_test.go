package config

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/uri"
)

func TestBondConfigRoundTrip(t *testing.T) {
	args := []string{"-provider", "direct", "-peer", "127.0.0.1:56000", "-mode", "tcp", "-bond", "-n", "3"}
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	round, err := ParseClient(ClientArgs(c), io.Discard)
	if err != nil || !round.Proxy.Bond {
		t.Fatalf("args round trip: %v", err)
	}
	link := (&uri.Config{Provider: "direct", Peer: "127.0.0.1:56000", Mode: "tcp", Bond: true, N: 3}).String()
	parsed, err := uri.Parse(link)
	if err != nil || !parsed.Bond {
		t.Fatalf("URI round trip: %v", err)
	}
	c, err = ParseClient([]string{link}, io.Discard)
	if err != nil || !c.Proxy.Bond {
		t.Fatalf("URI to CLI: %v", err)
	}
	c, err = ParseClientJSON([]byte(`{"provider":"direct","peer":"127.0.0.1:56000","proxy":{"mode":"tcp","bond":true}}`), "")
	if err != nil || !c.Proxy.Bond {
		t.Fatalf("JSON: %v", err)
	}
	c, err = ParseClientJSON([]byte(`{}`), link)
	if err != nil || !c.Proxy.Bond {
		t.Fatalf("URI overlay: %v", err)
	}
	if strings.Contains(DefaultClientJSON(), `"bond"`) {
		t.Fatal("default JSON changed")
	}
}

func TestBondValidation(t *testing.T) {
	for _, extra := range [][]string{{}, {"-mode", "tcp", "-n", "257"}} {
		args := append([]string{"-provider", "direct", "-peer", "127.0.0.1:56000", "-bond"}, extra...)
		if _, err := ParseClient(args, io.Discard); !errors.Is(err, ErrBondConfig) {
			t.Fatalf("accepted %v: %v", args, err)
		}
	}
	if _, err := ParseClientJSON([]byte(`{"provider":"direct","peer":"127.0.0.1:1","proxy":{"mode":"tcp","bonds":true}}`), ""); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
}
