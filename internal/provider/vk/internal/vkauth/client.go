package vkauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/personanet"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"

	tlsclient "github.com/bogdanfinn/tls-client"
)

type Config struct {
	Credentials     []VKCredentials
	Dialer          net.Dialer
	ManualOnly      bool
	StreamsPerCache int
	StreamsAlive    func() int32
	AutoSolver      AutoSolveFunc
	ManualSolver    ManualSolveFunc
	Platform        browserprofile.Platform
	FingerprintSeed string
	StatePaths      []string
	Log             logx.Logger
}

type Client struct {
	credentials []VKCredentials
	dialer      net.Dialer
	manualOnly  bool
	platform    browserprofile.Platform
	streamsFn   func() int32
	autoSolver  AutoSolveFunc
	manualSolve ManualSolveFunc
	log         logx.Logger

	store *Store

	lockout atomic.Int64

	personaMu sync.RWMutex
	identity  browserprofile.Identity
	persona   browserprofile.Profile
	gens      genStore

	fetchMu            sync.Mutex
	lastFetchTime      time.Time
	captchaAttempt     int
	tokenChain         tokenChainFn
	minFetchIntervalFn func() time.Duration
}

type tokenChainFn func(ctx context.Context, link string, streamID int, creds VKCredentials, jar tlsclient.CookieJar) (string, string, []string, error)

func New(cfg Config) *Client {
	c := &Client{
		credentials: cfg.Credentials,
		dialer:      cfg.Dialer,
		manualOnly:  cfg.ManualOnly,
		platform:    cfg.Platform,
		streamsFn:   cfg.StreamsAlive,
		autoSolver:  cfg.AutoSolver,
		manualSolve: cfg.ManualSolver,
		log:         cfg.Log,
		store:       NewStore(cfg.StreamsPerCache),
	}
	if len(c.credentials) == 0 {
		c.credentials = DefaultCredentials
	}
	if c.log == nil {
		c.log = logx.Nop()
	}
	if c.streamsFn == nil {
		c.streamsFn = func() int32 { return 1 }
	}
	c.tokenChain = c.getTokenChain
	c.minFetchIntervalFn = func() time.Duration {
		return 3*time.Second + time.Duration(randx.Intn(3000))*time.Millisecond
	}
	seed := cfg.FingerprintSeed
	if seed == "" {
		seed = randx.Hex(16)
	}
	c.gens = genStore{paths: cfg.StatePaths}
	c.identity = browserprofile.Identity{Seed: seed, Gen: c.gens.load(seed)}
	c.persona = browserprofile.For(c.platform, c.identity)
	if c.identity.Gen > 0 {
		c.log.Debugf("[VK Auth] Persona gen=%d restored | User-Agent: %s", c.identity.Gen, c.persona.UserAgent)
	}
	return c
}

func (c *Client) currentPersona() browserprofile.Profile {
	c.personaMu.RLock()
	defer c.personaMu.RUnlock()
	return c.persona
}

func (c *Client) burnPersona(streamID int) {
	c.personaMu.Lock()
	c.identity.Gen++
	c.persona = browserprofile.For(c.platform, c.identity)
	ua, gen, seed := c.persona.UserAgent, c.identity.Gen, c.identity.Seed
	c.personaMu.Unlock()
	c.log.Infof("[STREAM %d] [VK Auth] Persona burned, gen=%d | User-Agent: %s", streamID, gen, ua)
	if !c.gens.save(seed, gen) && len(c.gens.paths) > 0 {
		c.log.Warnf("[STREAM %d] [VK Auth] Persona gen not persisted (%v) - burned fingerprint returns after restart", streamID, c.gens.paths)
	}
}

// GetCredentials возвращает учетные данные TURN, используя кеш или запрашивая их у VK.
func (c *Client) GetCredentials(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)

	if creds, addrs, ok := cache.lookup(link, streamID); ok {
		c.log.Debugf("[STREAM %d] [VK Auth] Using cached credentials (cache=%d, expires in %v, server=%s)",
			streamID, cacheID, time.Until(creds.ExpiresAt), addrs[0])
		return creds.Username, creds.Password, addrs, nil
	}

	cache.fetchMu.Lock()
	defer cache.fetchMu.Unlock()

	if creds, addrs, ok := cache.lookup(link, streamID); ok {
		return creds.Username, creds.Password, addrs, nil
	}

	user, pass, addrs, err := c.fetchSerialized(ctx, link, streamID)
	if err != nil {
		return "", "", nil, err
	}

	cache.store(TurnCredentials{
		Username:    user,
		Password:    pass,
		ServerAddrs: addrs,
		ExpiresAt:   time.Now().Add(CredentialLifetime - CacheSafetyMargin).Round(0),
		Link:        link,
	})
	return user, pass, orderAddrs(addrs, streamID), nil
}

func orderAddrs(addrs []string, streamID int) []string {
	n := len(addrs)
	if n <= 1 {
		return append([]string(nil), addrs...)
	}
	k := streamID % n
	out := make([]string, 0, n)
	out = append(out, addrs[k:]...)
	out = append(out, addrs[:k]...)
	return out
}

func (c *Client) HandleAuthError(streamID int) bool {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)
	now := time.Now().Unix()

	if now-cache.lastErrorTime.Load() > int64(ErrorWindow.Seconds()) {
		cache.errorCount.Store(0)
	}
	count := cache.errorCount.Add(1)
	cache.lastErrorTime.Store(now)

	c.log.Warnf("[STREAM %d] [VK Auth] Auth error (cache=%d, count=%d/%d)", streamID, cacheID, count, MaxCacheErrors)

	if count >= MaxCacheErrors {
		c.log.Warnf("[VK Auth] Multiple auth errors (%d), invalidating cache %d for stream %d", count, cacheID, streamID)
		cache.Invalidate()
		c.log.Warnf("[STREAM %d] [VK Auth] Credentials cache invalidated", streamID)
		return true
	}
	return false
}

func (c *Client) ResetErrors(streamID int) {
	c.store.Get(streamID).errorCount.Store(0)
}

func (c *Client) DropCredentials(streamID int) {
	if !c.store.Get(streamID).Invalidate() {
		return
	}
	c.log.Warnf("[STREAM %d] [VK Auth] Deallocate unconfirmed - credentials dropped (cache=%d)",
		streamID, c.store.CacheID(streamID))
}

func (c *Client) LockoutUntilUnix() int64 {
	return c.lockout.Load()
}

// BackoffUntilUnix - алиас LockoutUntilUnix: lockout глобальный, а provider.Provider
// требует no-arg сигнатуру (без streamID).
func (c *Client) BackoffUntilUnix() int64 { return c.LockoutUntilUnix() }

func (*Client) Name() string { return "vk" }

func (*Client) IsAuthError(err error) bool { return IsAuthError(err) }

func (c *Client) engageLockout(d time.Duration) {
	c.lockout.Store(time.Now().Add(d).Unix())
}

// fetchSerialized сериализует fetch и держит min-интервал между запросами (анти
// rate-limit VK).
func (c *Client) fetchSerialized(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	minInterval := c.minFetchIntervalFn()
	elapsed := time.Since(c.lastFetchTime)
	if !c.lastFetchTime.IsZero() && elapsed < minInterval {
		wait := minInterval - elapsed
		c.log.Debugf("[STREAM %d] [VK Auth] Throttling: waiting %v to prevent rate limit", streamID, wait.Truncate(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	user, pass, addrs, err := c.fetch(ctx, link, streamID)
	if ctx.Err() == nil {
		c.lastFetchTime = time.Now()
	}
	return user, pass, addrs, err
}

func (c *Client) fetch(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	if time.Now().Unix() < c.lockout.Load() {
		return "", "", nil, fmt.Errorf("%w: %w", ErrCaptchaWaitRequired, ErrLockoutActive)
	}

	c.captchaAttempt = 0

	var lastErr error
	burns := 0
	jar := personanet.NewCookieJar()
	for i := 0; i < len(c.credentials); {
		creds := c.credentials[i]
		c.log.Debugf("[STREAM %d] [VK Auth] Trying credentials: client_id=%s", streamID, creds.ClientID)

		user, pass, addrs, err := c.tokenChain(ctx, link, streamID, creds, jar)
		if err == nil {
			c.log.Debugf("[STREAM %d] [VK Auth] Success with client_id=%s", streamID, creds.ClientID)
			return user, pass, addrs, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", "", nil, err
		}
		c.log.Warnf("[STREAM %d] [VK Auth] Failed with client_id=%s: %v", streamID, creds.ClientID, err)

		// Личность сменилась - тот же client_id проходится заново с чистыми
		// куками, пока не кончатся режимы решения captcha.
		if errors.Is(err, ErrPersonaBurned) && burns < maxPersonaBurns {
			burns++
			jar = personanet.NewCookieJar()
			continue
		}
		i++

		if errors.Is(err, ErrCaptchaWaitRequired) || errors.Is(err, ErrFatalCaptchaNoStreams) ||
			errors.Is(err, ErrInvalidJoinLink) || errors.Is(err, ErrAnonymousBlocked) ||
			errors.Is(err, ErrCallFull) || errors.Is(err, captcha.ErrUnavailable) {
			return "", "", nil, err
		}
		if errors.Is(err, ErrVKRateLimit) {
			c.log.Warnf("[STREAM %d] [VK Auth] Rate limit detected, trying next credentials", streamID)
		}
	}
	return "", "", nil, fmt.Errorf("all VK credentials failed: %w", lastErr)
}

func vkDelayRandom(ctx context.Context, minMs, maxMs int) error {
	ms := minMs + randx.Intn(maxMs-minMs+1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	}
}
