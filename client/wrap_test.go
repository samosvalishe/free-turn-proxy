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

	// Check DTLS header mimicry
	if wire[0] != 0x17 {
		t.Fatalf("content_type = 0x%02X, want 0x17", wire[0])
	}
	if wire[1] != 0xFE || wire[2] != 0xFD {
		t.Fatalf("version = 0x%02X%02X, want 0xFEFD", wire[1], wire[2])
	}
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

func TestWrapPaddingHidesSize(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	wc, _ := newWrapConn(key, false)

	// Two payloads of different size should produce same wire size if in same bucket
	p1 := make([]byte, 10)
	p2 := make([]byte, 100)

	w1 := make([]byte, wrapMaxWire(len(p1)))
	w2 := make([]byte, wrapMaxWire(len(p2)))

	n1, _ := wc.wrapInto(w1, p1)
	n2, _ := wc.wrapInto(w2, p2)

	if n1 != n2 {
		t.Fatalf("different wire sizes for payloads in same bucket: %d vs %d", n1, n2)
	}
}

func TestWrapEpochDirection(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, wrapKeyLen)
	client, _ := newWrapConn(key, false)
	server, _ := newWrapConn(key, true)

	if client.epoch&0x8000 != 0 {
		t.Fatalf("client epoch MSB should be 0, got 0x%04X", client.epoch)
	}
	if server.epoch&0x8000 == 0 {
		t.Fatalf("server epoch MSB should be 1, got 0x%04X", server.epoch)
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

	// Flip a bit in the ciphertext
	wire[wrapHeaderLen+5] ^= 0xFF

	dst := make([]byte, 1600)
	if _, err := server.unwrapPacket(wire, dst); err == nil {
		t.Fatalf("unwrapPacket accepted tampered packet")
	}
}
