package vkauth

import (
	"context"
	"fmt"
	neturl "net/url"

	"github.com/samosvalishe/btp/internal/client/browserprofile"
	"github.com/samosvalishe/btp/internal/client/namegen"

	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// getTokenChain выполняет 4-шаговый обмен токенами VK для одной пары client_id/secret
// и возвращает тройку TURN-allocate. Ошибки captcha запускают настроенную цепочку
// auto/manual solver.
func (c *Client) getTokenChain(ctx context.Context, link string, streamID int, creds VKCredentials, jar tlsclient.CookieJar) (string, string, []string, error) {
	profile := browserprofile.Profile{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Not(A:Brand";v="99", "Google Chrome";v="146", "Chromium";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	}

	httpClient, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profiles.Chrome_146),
		tlsclient.WithCookieJar(jar),
		tlsclient.WithDialer(c.dialer),
	)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to initialize tls_client: %w", err)
	}

	name := namegen.Generate()
	escapedName := neturl.QueryEscape(name)

	c.log.Infof("[STREAM %d] [VK Auth] Connecting Identity - Name: %s | User-Agent: %s", streamID, name, profile.UserAgent)

	// Шаг 1: анонимный app-токен.
	token1, err := c.fetchAnonToken(ctx, httpClient, profile, creds)
	if err != nil {
		return "", "", nil, err
	}

	if delayErr := vkDelayRandom(ctx, 100, 150); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 1a: прогрев getCallPreview (не критично).
	previewData := fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&fields=photo_200&access_token=%s", link, token1)
	if _, prevErr := c.doRequest(ctx, httpClient, profile, previewData,
		"https://api.vk.ru/method/calls.getCallPreview?v=5.275&client_id="+creds.ClientID); prevErr != nil {
		c.log.Warnf("[STREAM %d] [VK Auth] getCallPreview failed: %v", streamID, prevErr)
	}

	if delayErr := vkDelayRandom(ctx, 200, 400); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 2: анонимный call-токен (здесь может сработать captcha).
	token2, err := c.fetchCallToken(ctx, httpClient, profile, streamID, link, escapedName, token1, creds)
	if err != nil {
		return "", "", nil, err
	}

	if delayErr := vkDelayRandom(ctx, 100, 150); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 3: ok.ru session_key.
	sessionKey, err := c.fetchOkRuSession(ctx, httpClient, profile)
	if err != nil {
		return "", "", nil, err
	}

	if delayErr := vkDelayRandom(ctx, 100, 150); delayErr != nil {
		return "", "", nil, delayErr
	}

	// Шаг 4: TURN-реквизиты.
	return c.fetchTurnCreds(ctx, httpClient, profile, streamID, link, token2, sessionKey)
}
