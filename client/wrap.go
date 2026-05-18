// SPDX-License-Identifier: MIT

package main

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire format — DTLS 1.2 ApplicationData mimicry:
//
//	[1B content_type=0x17 | 2B version=0xFEFD | 2B epoch | 6B seq_num | 2B length |
//	 AEAD_ciphertext(padded plaintext) | 16B Poly1305 tag]
//
// Plaintext before encryption: [2B payload_length (BE) | payload | random padding]
// Padded to the nearest bucket in padBuckets to defeat size fingerprinting.
//
// Nonce (12B) = XOR(SHA-256(key)[0:12], [00 00 | epoch(2B) | seq(6B) | 00 00])
// AAD = the 13-byte DTLS record header.
//
// Epoch MSB encodes direction: 0 = client, 1 = server.
// This guarantees no nonce reuse between client and server under the same key.

const (
	wrapKeyLen    = 32
	wrapHeaderLen = 13 // DTLS record header
	wrapTagLen    = 16 // Poly1305 tag
	wrapLenPrefix = 2  // uint16 BE real payload length
	wrapOverhead  = wrapHeaderLen + wrapTagLen + wrapLenPrefix // 31
)

// padBuckets defines plaintext size buckets. The padded plaintext
// ([2B length | payload | padding]) is rounded up to the nearest bucket.
var padBuckets = [...]int{128, 256, 512, 768, 1024, 1300}

// wrapConn holds per-connection state for DTLS-mimicry obfuscation.
type wrapConn struct {
	aead      cipher.AEAD
	nonceMask [12]byte // SHA-256(key)[0:12]
	epoch     uint16
	seq       atomic.Uint64
}

// newWrapConn creates wrap state. isServer must be true on the server side.
func newWrapConn(key []byte, isServer bool) (*wrapConn, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("wrap: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("wrap: aead init: %w", err)
	}
	h := sha256.Sum256(key)
	var mask [12]byte
	copy(mask[:], h[:12])

	var eb [2]byte
	if _, err := rand.Read(eb[:]); err != nil {
		return nil, fmt.Errorf("wrap: epoch rand: %w", err)
	}
	epoch := binary.BigEndian.Uint16(eb[:])
	if isServer {
		epoch |= 0x8000
	} else {
		epoch &^= 0x8000
	}
	return &wrapConn{aead: aead, nonceMask: mask, epoch: epoch}, nil
}

// deriveNonce builds the 12-byte AEAD nonce from header fields.
func (w *wrapConn) deriveNonce(epoch uint16, seqNum uint64) [12]byte {
	var explicit [12]byte
	binary.BigEndian.PutUint16(explicit[2:4], epoch)
	binary.BigEndian.PutUint16(explicit[4:6], uint16(seqNum>>32))
	binary.BigEndian.PutUint32(explicit[6:10], uint32(seqNum))
	var nonce [12]byte
	for i := range 12 {
		nonce[i] = w.nonceMask[i] ^ explicit[i]
	}
	return nonce
}

// padPlaintextSize returns the padded plaintext size for a given payload.
func padPlaintextSize(payloadLen int) int {
	needed := wrapLenPrefix + payloadLen
	for _, b := range padBuckets {
		if b >= needed {
			return b
		}
	}
	return needed
}

// WrapMaxWire returns the maximum wire length for a given payload size.
func wrapMaxWire(payloadLen int) int {
	return wrapHeaderLen + padPlaintextSize(payloadLen) + wrapTagLen
}

// wrapInto encrypts payload into dst. Returns total wire bytes written.
// dst must have len >= wrapMaxWire(len(payload)).
func (w *wrapConn) wrapInto(dst, payload []byte) (int, error) {
	paddedLen := padPlaintextSize(len(payload))
	wireLen := wrapHeaderLen + paddedLen + wrapTagLen
	if len(dst) < wireLen {
		return 0, errors.New("wrap: dst buffer too small")
	}

	seq := w.seq.Add(1) - 1

	// --- DTLS record header (13 bytes) ---
	dst[0] = 0x17 // ApplicationData
	dst[1] = 0xFE // DTLS 1.2
	dst[2] = 0xFD
	binary.BigEndian.PutUint16(dst[3:5], w.epoch)
	binary.BigEndian.PutUint16(dst[5:7], uint16(seq>>32))
	binary.BigEndian.PutUint32(dst[7:11], uint32(seq))
	binary.BigEndian.PutUint16(dst[11:13], uint16(paddedLen+wrapTagLen))

	// --- Build plaintext: [2B len | payload | padding] ---
	plain := dst[wrapHeaderLen : wrapHeaderLen+paddedLen]
	binary.BigEndian.PutUint16(plain[0:2], uint16(len(payload)))
	copy(plain[wrapLenPrefix:], payload)
	// Random padding for the tail
	padStart := wrapLenPrefix + len(payload)
	if padStart < paddedLen {
		_, _ = rand.Read(plain[padStart:paddedLen])
	}

	// --- AEAD encrypt in-place ---
	nonce := w.deriveNonce(w.epoch, seq)
	aad := dst[:wrapHeaderLen]
	w.aead.Seal(dst[wrapHeaderLen:wrapHeaderLen], nonce[:], plain, aad)

	return wireLen, nil
}

// unwrapPacket decrypts a wire-format packet into dst. Returns payload bytes.
func (w *wrapConn) unwrapPacket(wire, dst []byte) (int, error) {
	if len(wire) < wrapHeaderLen+wrapTagLen+wrapLenPrefix {
		return 0, errors.New("wrap: packet too short")
	}
	if wire[0] != 0x17 {
		return 0, fmt.Errorf("wrap: unexpected content type 0x%02X", wire[0])
	}

	epoch := binary.BigEndian.Uint16(wire[3:5])
	seqHigh := uint64(binary.BigEndian.Uint16(wire[5:7]))
	seqLow := uint64(binary.BigEndian.Uint32(wire[7:11]))
	seqNum := (seqHigh << 32) | seqLow
	bodyLen := int(binary.BigEndian.Uint16(wire[11:13]))

	if wrapHeaderLen+bodyLen > len(wire) {
		return 0, errors.New("wrap: truncated packet")
	}

	nonce := w.deriveNonce(epoch, seqNum)
	aad := wire[:wrapHeaderLen]
	ciphertext := wire[wrapHeaderLen : wrapHeaderLen+bodyLen]

	plain, err := w.aead.Open(ciphertext[:0], nonce[:], ciphertext, aad)
	if err != nil {
		return 0, fmt.Errorf("wrap: AEAD open: %w", err)
	}

	if len(plain) < wrapLenPrefix {
		return 0, errors.New("wrap: plaintext too short for length prefix")
	}
	realLen := int(binary.BigEndian.Uint16(plain[0:2]))
	if realLen > len(plain)-wrapLenPrefix {
		return 0, fmt.Errorf("wrap: payload length %d exceeds plaintext %d", realLen, len(plain)-wrapLenPrefix)
	}
	if realLen > len(dst) {
		return 0, errors.New("wrap: dst buffer too small")
	}
	copy(dst[:realLen], plain[wrapLenPrefix:wrapLenPrefix+realLen])
	return realLen, nil
}

// --- Helper functions (unchanged API for flag parsing) ---

func genWrapKeyHex() (string, error) {
	key := make([]byte, wrapKeyLen)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("wrap: key gen: %w", err)
	}
	return hex.EncodeToString(key), nil
}

func decodeWrapKey(enabled bool, raw string) ([]byte, error) {
	if !enabled {
		return nil, nil
	}
	if raw == "" {
		return nil, errors.New("-wrap requires -wrap-key")
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("-wrap-key invalid hex: %w", err)
	}
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("-wrap-key must decode to %d bytes (got %d)", wrapKeyLen, len(key))
	}
	return key, nil
}
