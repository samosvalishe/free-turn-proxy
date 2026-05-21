package config

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

func validClientArgs() []string {
	return []string{
		"-peer", "1.2.3.4:5000",
		"-link", "https://vk.com/call/join/abcdef",
	}
}

func TestParseClient_Defaults(t *testing.T) {
	c, err := ParseClient(validClientArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.Proxy.Listen != "127.0.0.1:9000" {
		t.Errorf("Proxy.Listen default: %q", c.Proxy.Listen)
	}
	if c.TURN.N != 10 {
		t.Errorf("TURN.N default: %d", c.TURN.N)
	}
	if c.DNS.Mode != "auto" {
		t.Errorf("DNS.Mode default: %q", c.DNS.Mode)
	}
	if c.VK.StreamsPerCred != defaultStreamsPerCache {
		t.Errorf("VK.StreamsPerCred default: %d", c.VK.StreamsPerCred)
	}
	if c.VK.Link != "abcdef" {
		t.Errorf("VK.Link: %q (expected abcdef)", c.VK.Link)
	}
	if c.Obf.Key != nil {
		t.Errorf("Obf.Key should be nil when -obf absent")
	}
	if c.Proxy.Mode != ProxyModeUDP {
		t.Errorf("Proxy.Mode default: %q (expected udp)", c.Proxy.Mode)
	}
}

func TestParseClient_VKLinkStrip(t *testing.T) {
	args := []string{
		"-peer", "1.2.3.4:5000",
		"-link", "https://vk.com/call/join/CODE123?foo=bar",
	}
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.VK.Link != "CODE123" {
		t.Errorf("VK.Link: %q (expected CODE123)", c.VK.Link)
	}
}

func TestParseClient_MissingPeer(t *testing.T) {
	_, err := ParseClient([]string{"-link", "https://vk.com/call/join/X"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Errorf("expected peer error, got %v", err)
	}
}

func TestParseClient_MissingVKLink(t *testing.T) {
	_, err := ParseClient([]string{"-peer", "1.2.3.4:5000"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-link") {
		t.Errorf("expected vk-link error, got %v", err)
	}
}

func TestParseClient_InvalidDNS(t *testing.T) {
	args := append(validClientArgs(), "-dns-mode", "garbage")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -dns-mode") {
		t.Errorf("expected dns error, got %v", err)
	}
}

func TestParseClient_BondWithoutTCPMode(t *testing.T) {
	args := append(validClientArgs(), "-bond")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-bond requires -mode tcp") {
		t.Errorf("expected bond error, got %v", err)
	}
}

func TestParseClient_ObfMissingKey(t *testing.T) {
	args := append(validClientArgs(), "-obf")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf requires -obf-key") {
		t.Errorf("expected obf-key error, got %v", err)
	}
}

func TestParseClient_ObfKeyOK(t *testing.T) {
	args := append(validClientArgs(), "-obf", "-obf-key", strings.Repeat("ab", 32))
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Obf.Key) != 32 {
		t.Errorf("Obf.Key len: %d", len(c.Obf.Key))
	}
}

func TestParseClient_StreamsPerCredNonPositive(t *testing.T) {
	args := append(validClientArgs(), "-streams-per-cred", "0")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "streams-per-cred") {
		t.Errorf("expected streams-per-cred error, got %v", err)
	}
}

func TestParseClient_NClampedToTen(t *testing.T) {
	args := append(validClientArgs(), "-n", "-5")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.TURN.N != 10 {
		t.Errorf("TURN.N: %d (expected 10)", c.TURN.N)
	}
}

func TestParseClient_DNSServersSplit(t *testing.T) {
	args := append(validClientArgs(), "-dns-servers", "1.1.1.1,8.8.8.8:53")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DNS.Servers) != 2 || c.DNS.Servers[0] != "1.1.1.1" || c.DNS.Servers[1] != "8.8.8.8:53" {
		t.Errorf("DNS.Servers: %v", c.DNS.Servers)
	}
}

func TestParseClient_GenObfKeySkipsPeerCheck(t *testing.T) {
	c, err := ParseClient([]string{"-gen-obf-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-obf-key should not require peer/vk-link: %v", err)
	}
	if !c.Obf.GenKey {
		t.Errorf("Obf.GenKey not set")
	}
}

func TestParseClient_HelpReturnsErrHelp(t *testing.T) {
	var buf bytes.Buffer
	_, err := ParseClient([]string{"-h"}, &buf)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("expected flag.ErrHelp, got %v", err)
	}
}

func TestParseClient_ProxyMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want ProxyMode
	}{
		{"default-udp", nil, ProxyModeUDP},
		{"tcp", []string{"-mode", "tcp"}, ProxyModeTCPFwd},
		{"tcp-bond", []string{"-mode", "tcp", "-bond"}, ProxyModeTCPFwdBond},
		{"default-udp-no-bond", nil, ProxyModeUDP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(validClientArgs(), tc.args...)
			c, err := ParseClient(args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if c.Proxy.Mode != tc.want {
				t.Errorf("Proxy.Mode = %q, want %q", c.Proxy.Mode, tc.want)
			}
		})
	}
}

func TestParseServer_Defaults(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "backend:1234"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Proxy.Listen != "0.0.0.0:56000" {
		t.Errorf("Proxy.Listen default: %q", s.Proxy.Listen)
	}
	if s.Proxy.Connect != "backend:1234" {
		t.Errorf("Proxy.Connect: %q", s.Proxy.Connect)
	}
	if s.Proxy.Mode != ProxyModeUDP {
		t.Errorf("Proxy.Mode default: %q", s.Proxy.Mode)
	}
}

func TestParseServer_MissingConnect(t *testing.T) {
	_, err := ParseServer(nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "server address") {
		t.Errorf("expected connect error, got %v", err)
	}
}

func TestParseServer_ObfMissingKey(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-obf"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf requires -obf-key") {
		t.Errorf("expected obf-key error, got %v", err)
	}
}

func TestParseServer_ObfKeyBadHex(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-obf", "-obf-key", "zz"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid hex") {
		t.Errorf("expected hex error, got %v", err)
	}
}

func TestParseServer_ObfKeyOK(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "x:1", "-obf", "-obf-key", strings.Repeat("cd", 32)}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Obf.Key) != 32 {
		t.Errorf("Obf.Key len: %d", len(s.Obf.Key))
	}
}

func TestParseServer_GenObfKeySkipsConnectCheck(t *testing.T) {
	s, err := ParseServer([]string{"-gen-obf-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-obf-key should not require -connect: %v", err)
	}
	if !s.Obf.GenKey {
		t.Errorf("Obf.GenKey not set")
	}
}

func TestParseServer_ProxyMode(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "x:1", "-mode", "tcp"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Proxy.Mode != ProxyModeTCPFwd {
		t.Errorf("Proxy.Mode = %q, want tcpfwd", s.Proxy.Mode)
	}
}
