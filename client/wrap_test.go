package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWrapConnRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	payload := []byte("dtls record bytes")

	client, err := newWrapConn(key, false)
	if err != nil {
		t.Fatalf("newWrapConn(client): %v", err)
	}
	server, err := newWrapConn(key, true)
	if err != nil {
		t.Fatalf("newWrapConn(server): %v", err)
	}

	// Client → Server
	wire := make([]byte, wrapMaxWire(len(payload)))
	n, err := client.wrapInto(wire, payload)
	if err != nil {
		t.Fatalf("wrapInto: %v", err)
	}
	wire = wire[:n]

	if bytes.Contains(wire, payload) {
		t.Fatalf("wrapped packet contains plaintext payload")
	}

	dst := make([]byte, 1600)
	m, err := server.unwrapPacket(wire, dst)
	if err != nil {
		t.Fatalf("unwrapPacket: %v", err)
	}
	if m != len(payload) {
		t.Fatalf("unwrapped len = %d, want %d", m, len(payload))
	}
	if !bytes.Equal(dst[:m], payload) {
		t.Fatalf("round trip mismatch: got %q want %q", dst[:m], payload)
	}

	// Server → Client
	wire2 := make([]byte, wrapMaxWire(len(payload)))
	n2, err := server.wrapInto(wire2, payload)
	if err != nil {
		t.Fatalf("server wrapInto: %v", err)
	}
	m2, err := client.unwrapPacket(wire2[:n2], dst)
	if err != nil {
		t.Fatalf("client unwrapPacket: %v", err)
	}
	if !bytes.Equal(dst[:m2], payload) {
		t.Fatalf("server→client round trip mismatch")
	}
}

func TestWrapPrefixLenInRange(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	wc, _ := newWrapConn(key, false)
	payload := []byte("x")

	// Run many trials; every prefix_len must fall in [1..8].
	for range 200 {
		wire := make([]byte, wrapMaxWire(len(payload)))
		n, err := wc.wrapInto(wire, payload)
		if err != nil {
			t.Fatalf("wrapInto: %v", err)
		}
		prefixLen := wrapPrefixMin + int(wire[0]&0x07)
		if prefixLen < 1 || prefixLen > 8 {
			t.Fatalf("prefix_len out of range: %d", prefixLen)
		}
		expected := prefixLen + wrapNonceLen + len(payload) + wrapTagLen
		if n != expected {
			t.Fatalf("wire size mismatch: got %d want %d (prefix_len=%d)", n, expected, prefixLen)
		}
	}
}

func TestWrapDirectionBit(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	client, _ := newWrapConn(key, false)
	server, _ := newWrapConn(key, true)

	if client.sessionID[0]&0x80 != 0 {
		t.Fatalf("client sessionID MSB should be 0, got 0x%02X", client.sessionID[0])
	}
	if server.sessionID[0]&0x80 == 0 {
		t.Fatalf("server sessionID MSB should be 1, got 0x%02X", server.sessionID[0])
	}
}

func TestDecodeWrapKeyRequiresValidKeyWhenEnabled(t *testing.T) {
	if key, err := decodeWrapKey(false, ""); err != nil || key != nil {
		t.Fatalf("disabled decodeWrapKey = (%v, %v), want (nil, nil)", key, err)
	}

	if _, err := decodeWrapKey(true, ""); err == nil {
		t.Fatalf("decodeWrapKey accepted empty key")
	}

	shortHex := strings.Repeat("ab", wrapKeyLen-1)
	if _, err := decodeWrapKey(true, shortHex); err == nil {
		t.Fatalf("decodeWrapKey accepted short key")
	}

	fullHex := strings.Repeat("ab", wrapKeyLen)
	key, err := decodeWrapKey(true, fullHex)
	if err != nil {
		t.Fatalf("decodeWrapKey returned error: %v", err)
	}
	if len(key) != wrapKeyLen {
		t.Fatalf("decoded key len = %d, want %d", len(key), wrapKeyLen)
	}
}

func TestUnwrapRejectsShortPacket(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	wc, _ := newWrapConn(key, false)
	if _, err := wc.unwrapPacket([]byte("short"), make([]byte, 16)); err == nil {
		t.Fatalf("unwrapPacket accepted short packet")
	}
}

func TestUnwrapRejectsTamperedPacket(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	client, _ := newWrapConn(key, false)
	server, _ := newWrapConn(key, true)

	payload := []byte("integrity test")
	wire := make([]byte, wrapMaxWire(len(payload)))
	n, _ := client.wrapInto(wire, payload)
	wire = wire[:n]

	// Flip a bit in the ciphertext (past prefix+nonce).
	prefixLen := wrapPrefixMin + int(wire[0]&0x07)
	wire[prefixLen+wrapNonceLen+1] ^= 0xFF

	dst := make([]byte, 1600)
	if _, err := server.unwrapPacket(wire, dst); err == nil {
		t.Fatalf("unwrapPacket accepted tampered packet")
	}
}
