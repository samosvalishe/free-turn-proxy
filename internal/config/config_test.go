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
		"-vk-link", "https://vk.com/call/join/abcdef",
	}
}

func TestParseClient_Defaults(t *testing.T) {
	c, err := ParseClient(validClientArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.Listen != "127.0.0.1:9000" {
		t.Errorf("Listen default: %q", c.Listen)
	}
	if c.N != 10 {
		t.Errorf("N default: %d", c.N)
	}
	if c.DNSMode != "auto" {
		t.Errorf("DNSMode default: %q", c.DNSMode)
	}
	if c.StreamsPerCred != defaultStreamsPerCache {
		t.Errorf("StreamsPerCred default: %d", c.StreamsPerCred)
	}
	if c.VKLink != "abcdef" {
		t.Errorf("VKLink: %q (expected abcdef)", c.VKLink)
	}
	if c.WrapKey != nil {
		t.Errorf("WrapKey should be nil when -wrap absent")
	}
}

func TestParseClient_VKLinkStrip(t *testing.T) {
	args := []string{
		"-peer", "1.2.3.4:5000",
		"-vk-link", "https://vk.com/call/join/CODE123?foo=bar",
	}
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.VKLink != "CODE123" {
		t.Errorf("VKLink: %q (expected CODE123)", c.VKLink)
	}
}

func TestParseClient_MissingPeer(t *testing.T) {
	_, err := ParseClient([]string{"-vk-link", "https://vk.com/call/join/X"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Errorf("expected peer error, got %v", err)
	}
}

func TestParseClient_MissingVKLink(t *testing.T) {
	_, err := ParseClient([]string{"-peer", "1.2.3.4:5000"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "vk-link") {
		t.Errorf("expected vk-link error, got %v", err)
	}
}

func TestParseClient_InvalidDNS(t *testing.T) {
	args := append(validClientArgs(), "-dns", "garbage")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -dns") {
		t.Errorf("expected dns error, got %v", err)
	}
}

func TestParseClient_WrapDirectConflict(t *testing.T) {
	args := append(validClientArgs(), "-wrap", "-no-dtls", "-wrap-key", strings.Repeat("aa", 32))
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-wrap requires DTLS") {
		t.Errorf("expected wrap/direct conflict, got %v", err)
	}
}

func TestParseClient_WrapMissingKey(t *testing.T) {
	args := append(validClientArgs(), "-wrap")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-wrap requires -wrap-key") {
		t.Errorf("expected wrap-key error, got %v", err)
	}
}

func TestParseClient_WrapKeyOK(t *testing.T) {
	args := append(validClientArgs(), "-wrap", "-wrap-key", strings.Repeat("ab", 32))
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.WrapKey) != 32 {
		t.Errorf("WrapKey len: %d", len(c.WrapKey))
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
	if c.N != 10 {
		t.Errorf("N: %d (expected 10)", c.N)
	}
}

func TestParseClient_DNSServersSplit(t *testing.T) {
	args := append(validClientArgs(), "-dns-servers", "1.1.1.1,8.8.8.8:53")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DNSServers) != 2 || c.DNSServers[0] != "1.1.1.1" || c.DNSServers[1] != "8.8.8.8:53" {
		t.Errorf("DNSServers: %v", c.DNSServers)
	}
}

func TestParseClient_GenWrapKeySkipsPeerCheck(t *testing.T) {
	c, err := ParseClient([]string{"-gen-wrap-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-wrap-key should not require peer/vk-link: %v", err)
	}
	if !c.GenWrapKey {
		t.Errorf("GenWrapKey not set")
	}
}

func TestParseClient_HelpReturnsErrHelp(t *testing.T) {
	var buf bytes.Buffer
	_, err := ParseClient([]string{"-h"}, &buf)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("expected flag.ErrHelp, got %v", err)
	}
}

func TestParseServer_Defaults(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "backend:1234"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Listen != "0.0.0.0:56000" {
		t.Errorf("Listen default: %q", s.Listen)
	}
	if s.Connect != "backend:1234" {
		t.Errorf("Connect: %q", s.Connect)
	}
}

func TestParseServer_MissingConnect(t *testing.T) {
	_, err := ParseServer(nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "server address") {
		t.Errorf("expected connect error, got %v", err)
	}
}

func TestParseServer_WrapMissingKey(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-wrap"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-wrap requires -wrap-key") {
		t.Errorf("expected wrap-key error, got %v", err)
	}
}

func TestParseServer_WrapKeyBadHex(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-wrap", "-wrap-key", "zz"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid hex") {
		t.Errorf("expected hex error, got %v", err)
	}
}

func TestParseServer_WrapKeyOK(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "x:1", "-wrap", "-wrap-key", strings.Repeat("cd", 32)}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.WrapKey) != 32 {
		t.Errorf("WrapKey len: %d", len(s.WrapKey))
	}
}

func TestParseServer_GenWrapKeySkipsConnectCheck(t *testing.T) {
	s, err := ParseServer([]string{"-gen-wrap-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-wrap-key should not require -connect: %v", err)
	}
	if !s.GenWrapKey {
		t.Errorf("GenWrapKey not set")
	}
}
