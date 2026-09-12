package vkauth

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/stun/v3"
)

type StreamCredentialsCache struct {
	creds         TurnCredentials
	mutex         sync.RWMutex
	errorCount    atomic.Int32
	lastErrorTime atomic.Int64
}

type Store struct {
	mu              sync.RWMutex
	caches          map[int]*StreamCredentialsCache
	streamsPerCache int
}

func NewStore(streamsPerCache int) *Store {
	if streamsPerCache <= 0 {
		streamsPerCache = DefaultStreamsPerCache
	}
	return &Store{
		caches:          make(map[int]*StreamCredentialsCache),
		streamsPerCache: streamsPerCache,
	}
}

// CacheID группирует потоки в блоки по streamsPerCache: потоки 1..streamsPerCache
// делят один кэш реквизитов, streamsPerCache+1.. - следующий. streamID 1-based;
// первый поток блока инициирует fetch к VK, остальные переиспользуют тёплый кэш.
func (s *Store) CacheID(streamID int) int {
	if streamID < 1 {
		return 0
	}
	return (streamID - 1) / s.streamsPerCache
}

func (s *Store) Get(streamID int) *StreamCredentialsCache {
	cacheID := s.CacheID(streamID)

	s.mu.RLock()
	cache, exists := s.caches[cacheID]
	s.mu.RUnlock()
	if exists {
		return cache
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cache, exists = s.caches[cacheID]; exists {
		return cache
	}
	cache = &StreamCredentialsCache{}
	s.caches[cacheID] = cache
	return cache
}

func (c *StreamCredentialsCache) Invalidate() bool {
	c.mutex.Lock()
	had := c.creds.Username != ""
	c.creds = TurnCredentials{}
	c.mutex.Unlock()

	c.errorCount.Store(0)
	c.lastErrorTime.Store(0)
	return had
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	// Ответ TURN-сервера приходит типизированным - код берём из него, а не из текста.
	if turnErr, ok := errors.AsType[*stun.TurnError](err); ok {
		// 486 означает занятую квоту, а не невалидные реквизиты.
		switch turnErr.ErrorCodeAttr.Code {
		case stun.CodeUnauthorized, stun.CodeWrongCredentials, stun.CodeStaleNonce:
			return true
		default:
			return false
		}
	}
	// Ошибки не от TURN-сервера (получение кредов у провайдера) типа не несут.
	s := err.Error()
	return strings.Contains(s, "401") ||
		strings.Contains(s, "Unauthorized") ||
		strings.Contains(s, "authentication") ||
		strings.Contains(s, "invalid credential") ||
		strings.Contains(s, "stale nonce")
}
