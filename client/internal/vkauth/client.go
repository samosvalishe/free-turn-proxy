package vkauth

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// Config configures a Client. Zero values pick safe defaults except for Dialer
// (must be set explicitly to preserve the existing custom-DNS behavior).
type Config struct {
	// Credentials to try in order. nil/empty -> DefaultCredentials.
	Credentials []VKCredentials

	// Dialer used by the HTTP transport for VK API requests.
	Dialer net.Dialer

	// ManualOnly forces the manual captcha path on the first attempt.
	ManualOnly bool

	// StreamsPerCache is the divisor for streamID -> cacheID. <=0 -> default.
	StreamsPerCache int

	// StreamsAlive returns the count of currently-connected streams; used to
	// decide whether an exhausted captcha is fatal vs throttleable. nil -> 1.
	StreamsAlive func() int32

	// AutoSolver / ManualSolver are pluggable captcha solvers. nil values
	// disable that path (the flow will fall through to the next attempt).
	AutoSolver   AutoSolveFunc
	ManualSolver ManualSolveFunc

	// Debugf is the gated debug log sink. nil -> no-op.
	Debugf func(format string, v ...any)
}

// Client is the VK auth + creds-cache facade used by callers. It owns the
// per-stream-group cache, the global request throttle, the captcha lockout
// timer, and the auth-error counter used to invalidate stale TURN creds.
type Client struct {
	credentials []VKCredentials
	dialer      net.Dialer
	manualOnly  bool
	streamsFn   func() int32
	autoSolver  AutoSolveFunc
	manualSolve ManualSolveFunc
	debugf      func(format string, v ...any)

	store *Store

	lockout atomic.Int64

	fetchMu       sync.Mutex
	lastFetchTime time.Time

	// tokenChain is the per-creds 4-step fetcher. Production wires this to
	// (*Client).getTokenChain; tests inject a fake.
	tokenChain tokenChainFn

	// minFetchInterval gates back-to-back VK requests. Tests can lower this.
	minFetchIntervalFn func() time.Duration
}

type tokenChainFn func(ctx context.Context, link string, streamID int, creds VKCredentials, jar tlsclient.CookieJar) (string, string, []string, error)

// New builds a Client from cfg.
func New(cfg Config) *Client {
	c := &Client{
		credentials: cfg.Credentials,
		dialer:      cfg.Dialer,
		manualOnly:  cfg.ManualOnly,
		streamsFn:   cfg.StreamsAlive,
		autoSolver:  cfg.AutoSolver,
		manualSolve: cfg.ManualSolver,
		debugf:      cfg.Debugf,
		store:       NewStore(cfg.StreamsPerCache),
	}
	if len(c.credentials) == 0 {
		c.credentials = DefaultCredentials
	}
	if c.debugf == nil {
		c.debugf = func(string, ...any) {}
	}
	if c.streamsFn == nil {
		c.streamsFn = func() int32 { return 1 }
	}
	c.tokenChain = c.getTokenChain
	c.minFetchIntervalFn = func() time.Duration {
		return 3*time.Second + time.Duration(rand.Intn(3000))*time.Millisecond
	}
	return c
}

// GetCredentials returns (username, password, server-addr) suitable for a TURN
// allocate, fetching from VK (with throttle + cache) only when needed.
func (c *Client) GetCredentials(ctx context.Context, link string, streamID int) (string, string, string, error) {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)

	cache.mutex.RLock()
	if cache.creds.Link == link && time.Now().Before(cache.creds.ExpiresAt) && len(cache.creds.ServerAddrs) > 0 {
		expires := time.Until(cache.creds.ExpiresAt)
		u, p := cache.creds.Username, cache.creds.Password
		addr := cache.creds.ServerAddrs[streamID%len(cache.creds.ServerAddrs)]
		cache.mutex.RUnlock()
		c.debugf("[STREAM %d] [VK Auth] Using cached credentials (cache=%d, expires in %v, server=%s)", streamID, cacheID, expires, addr)
		return u, p, addr, nil
	}
	cache.mutex.RUnlock()

	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if cache.creds.Link == link && time.Now().Before(cache.creds.ExpiresAt) && len(cache.creds.ServerAddrs) > 0 {
		addr := cache.creds.ServerAddrs[streamID%len(cache.creds.ServerAddrs)]
		return cache.creds.Username, cache.creds.Password, addr, nil
	}

	user, pass, addrs, err := c.fetchSerialized(ctx, link, streamID)
	if err != nil {
		return "", "", "", err
	}

	cache.creds = TurnCredentials{
		Username:    user,
		Password:    pass,
		ServerAddrs: addrs,
		ExpiresAt:   time.Now().Add(CredentialLifetime - CacheSafetyMargin),
		Link:        link,
	}
	addr := addrs[streamID%len(addrs)]
	return user, pass, addr, nil
}

// HandleAuthError increments the auth-error counter for the stream's cache and
// invalidates when the threshold is reached within the rolling window.
// Returns true when the cache was invalidated.
func (c *Client) HandleAuthError(streamID int) bool {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)
	now := time.Now().Unix()

	if now-cache.lastErrorTime.Load() > int64(ErrorWindow.Seconds()) {
		cache.errorCount.Store(0)
	}
	count := cache.errorCount.Add(1)
	cache.lastErrorTime.Store(now)

	log.Printf("[STREAM %d] Auth error (cache=%d, count=%d/%d)", streamID, cacheID, count, MaxCacheErrors)

	if count >= MaxCacheErrors {
		log.Printf("[VK Auth] Multiple auth errors detected (%d), invalidating cache %d for stream %d...", count, cacheID, streamID)
		cache.Invalidate()
		log.Printf("[STREAM %d] [VK Auth] Credentials cache invalidated", streamID)
		return true
	}
	return false
}

// ResetErrors zeroes the auth-error counter (call on successful allocate).
func (c *Client) ResetErrors(streamID int) {
	c.store.Get(streamID).errorCount.Store(0)
}

// LockoutUntilUnix returns the unix-second deadline the global captcha lockout
// is set to, or 0 when no lockout is active.
func (c *Client) LockoutUntilUnix() int64 {
	return c.lockout.Load()
}

// engageLockout arms the global captcha lockout for d from now.
func (c *Client) engageLockout(d time.Duration) {
	c.lockout.Store(time.Now().Add(d).Unix())
}

// fetchSerialized enforces the 3s + jitter inter-request gap and then performs
// the try-all-credentials fetch.
func (c *Client) fetchSerialized(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	minInterval := c.minFetchIntervalFn()
	elapsed := time.Since(c.lastFetchTime)
	if !c.lastFetchTime.IsZero() && elapsed < minInterval {
		wait := minInterval - elapsed
		log.Printf("[STREAM %d] [VK Auth] Throttling: waiting %v to prevent rate limit...", streamID, wait.Truncate(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	defer func() { c.lastFetchTime = time.Now() }()
	return c.fetch(ctx, link, streamID)
}

// fetch loops over c.credentials, returning on first success or terminal error.
func (c *Client) fetch(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	if time.Now().Unix() < c.lockout.Load() {
		return "", "", nil, fmt.Errorf("CAPTCHA_WAIT_REQUIRED: global lockout active")
	}

	var lastErr error
	jar := tlsclient.NewCookieJar()
	for _, creds := range c.credentials {
		log.Printf("[STREAM %d] [VK Auth] Trying credentials: client_id=%s", streamID, creds.ClientID)

		user, pass, addrs, err := c.tokenChain(ctx, link, streamID, creds, jar)
		if err == nil {
			log.Printf("[STREAM %d] [VK Auth] Success with client_id=%s", streamID, creds.ClientID)
			return user, pass, addrs, nil
		}
		lastErr = err
		log.Printf("[STREAM %d] [VK Auth] Failed with client_id=%s: %v", streamID, creds.ClientID, err)

		es := err.Error()
		if strings.Contains(es, ErrCaptchaWaitRequired) || strings.Contains(es, "FATAL_CAPTCHA") {
			return "", "", nil, err
		}
		if strings.Contains(es, "error_code:29") || strings.Contains(es, "error_code: 29") || strings.Contains(es, "Rate limit") {
			log.Printf("[STREAM %d] [VK Auth] Rate limit detected, trying next credentials...", streamID)
		}
	}
	return "", "", nil, fmt.Errorf("all VK credentials failed: %w", lastErr)
}

func vkDelayRandom(minMs, maxMs int) {
	ms := minMs + rand.Intn(maxMs-minMs+1)
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
