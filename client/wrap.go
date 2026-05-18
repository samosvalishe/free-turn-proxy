// SPDX-License-Identifier: MIT

package main

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire format — noise-only AEAD (no protocol mimicry):
//
//	[1B header | (prefix_len-1)B random | 12B nonce | AEAD ciphertext | 16B tag]
//
// prefix_len = 1 + (header & 0x07), so byte 0 is uniformly random across all
// 256 values while encoding 1..8 bytes of leading random padding. There is
// no fixed byte, no version, no length field — everything past the header
// byte is indistinguishable from random to a passive observer.
//
// Nonce (12B) = 4B sessionID || 8B counter (big-endian). sessionID has its
// MSB set on server side, cleared on client side, preventing nonce reuse
// between directions under the same key. counter starts at a random uint64
// to avoid reuse across process restarts.
//
// AAD = nonce. The random prefix is not authenticated (it carries no
// information).

const (
	wrapKeyLen    = 32
	wrapNonceLen  = 12
	wrapTagLen    = 16
	wrapPrefixMin = 1
	wrapPrefixMax = 8
)

type wrapConn struct {
	aead      cipher.AEAD
	sessionID [4]byte
	counter   atomic.Uint64
}

func newWrapConn(key []byte, isServer bool) (*wrapConn, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("wrap: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("wrap: aead init: %w", err)
	}
	w := &wrapConn{aead: aead}
	if _, err := rand.Read(w.sessionID[:]); err != nil {
		return nil, fmt.Errorf("wrap: sessionID rand: %w", err)
	}
	if isServer {
		w.sessionID[0] |= 0x80
	} else {
		w.sessionID[0] &^= 0x80
	}
	var cb [8]byte
	if _, err := rand.Read(cb[:]); err != nil {
		return nil, fmt.Errorf("wrap: counter rand: %w", err)
	}
	w.counter.Store(binary.BigEndian.Uint64(cb[:]))
	return w, nil
}

// wrapMaxWire returns max wire bytes for a given payload size.
func wrapMaxWire(payloadLen int) int {
	return wrapPrefixMax + wrapNonceLen + payloadLen + wrapTagLen
}

func (w *wrapConn) wrapInto(dst, payload []byte) (int, error) {
	// Header + prefix length.
	var hdr [1]byte
	if _, err := rand.Read(hdr[:]); err != nil {
		return 0, fmt.Errorf("wrap: prefix rand: %w", err)
	}
	prefixLen := wrapPrefixMin + int(hdr[0]&0x07) // 1..8

	wireLen := prefixLen + wrapNonceLen + len(payload) + wrapTagLen
	if len(dst) < wireLen {
		return 0, errors.New("wrap: dst buffer too small")
	}

	dst[0] = hdr[0]
	if prefixLen > 1 {
		if _, err := rand.Read(dst[1:prefixLen]); err != nil {
			return 0, fmt.Errorf("wrap: prefix rand: %w", err)
		}
	}

	// Nonce: 4B sessionID || 8B counter.
	noncePos := prefixLen
	copy(dst[noncePos:noncePos+4], w.sessionID[:])
	ctr := w.counter.Add(1) - 1
	binary.BigEndian.PutUint64(dst[noncePos+4:noncePos+wrapNonceLen], ctr)

	nonce := dst[noncePos : noncePos+wrapNonceLen]
	ctPos := noncePos + wrapNonceLen
	// In-place: write plaintext at ctPos, then Seal over it.
	copy(dst[ctPos:], payload)
	w.aead.Seal(dst[ctPos:ctPos], nonce, dst[ctPos:ctPos+len(payload)], nonce)

	return wireLen, nil
}

func (w *wrapConn) unwrapPacket(wire, dst []byte) (int, error) {
	if len(wire) < wrapPrefixMin+wrapNonceLen+wrapTagLen {
		return 0, errors.New("wrap: packet too short")
	}
	prefixLen := wrapPrefixMin + int(wire[0]&0x07)
	if len(wire) < prefixLen+wrapNonceLen+wrapTagLen {
		return 0, errors.New("wrap: packet too short for prefix")
	}
	nonce := wire[prefixLen : prefixLen+wrapNonceLen]
	ct := wire[prefixLen+wrapNonceLen:]

	plain, err := w.aead.Open(ct[:0], nonce, ct, nonce)
	if err != nil {
		return 0, fmt.Errorf("wrap: AEAD open: %w", err)
	}
	if len(plain) > len(dst) {
		return 0, errors.New("wrap: dst buffer too small")
	}
	copy(dst[:len(plain)], plain)
	return len(plain), nil
}

// --- Helpers ---

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
