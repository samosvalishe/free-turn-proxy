package vkauth

import (
	"strings"
	"sync"
	"sync/atomic"
)

// StreamCredentialsCache holds the resolved TURN creds for a group of streams
// plus auth-error tracking for invalidation decisions.
type StreamCredentialsCache struct {
	creds         TurnCredentials
	mutex         sync.RWMutex
	errorCount    atomic.Int32
	lastErrorTime atomic.Int64
}

// Store maps cache-id (streamID / streamsPerCache) -> StreamCredentialsCache.
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

func (s *Store) CacheID(streamID int) int {
	return streamID / s.streamsPerCache
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

// Invalidate clears the creds for the given stream's cache and resets error state.
func (c *StreamCredentialsCache) Invalidate() {
	c.mutex.Lock()
	c.creds = TurnCredentials{}
	c.mutex.Unlock()

	c.errorCount.Store(0)
	c.lastErrorTime.Store(0)
}

// IsAuthError matches the historical heuristic used by the TURN client: a TURN
// allocate failure mentioning auth/401/stale-nonce should trigger cache invalidate.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") ||
		strings.Contains(s, "Unauthorized") ||
		strings.Contains(s, "authentication") ||
		strings.Contains(s, "invalid credential") ||
		strings.Contains(s, "stale nonce")
}
