// Package randx предоставляет обёртку над crypto/rand с API в стиле math/rand.
package randx

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
)

// IntN возвращает равномерное случайное число из [0, n).
func IntN(n int) int {
	if n <= 0 {
		return 0
	}
	un := uint64(n) //nolint:gosec // n > 0 проверено выше
	limit := ^uint64(0) / un * un
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0
		}
		v := binary.LittleEndian.Uint64(b[:])
		if v < limit {
			return int(v % un) //nolint:gosec // v%un < n
		}
	}
}

func Intn(n int) int { return IntN(n) }

// Hex возвращает hex-строку из n случайных байт.
func Hex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Float64 возвращает случайное число из [0.0, 1.0).
func Float64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	u := binary.LittleEndian.Uint64(b[:]) >> 11
	return float64(u) / float64(1<<53)
}
