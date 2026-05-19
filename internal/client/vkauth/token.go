package vkauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	neturl "net/url"
	"strings"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/client/browserprofile"
	"github.com/cacggghp/vk-turn-proxy/internal/client/captcha"
	"github.com/cacggghp/vk-turn-proxy/internal/client/namegen"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
)

// getTokenChain walks the 4-step VK token exchange for one client_id/secret
// pair and returns the resulting TURN allocate triple. Captcha errors trigger
// the configured auto/manual solver chain.
func (c *Client) getTokenChain(ctx context.Context, link string, streamID int, creds VKCredentials, jar tlsclient.CookieJar) (string, string, []string, error) {
	profile := browserprofile.Profile{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Not(A:Brand";v="99", "Google Chrome";v="146", "Chromium";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	}

	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
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

	log.Printf("[STREAM %d] [VK Auth] Connecting Identity - Name: %s | User-Agent: %s", streamID, name, profile.UserAgent)

	doRequest := func(data string, url string) (resp map[string]any, err error) {
		parsedURL, err := neturl.Parse(url)
		if err != nil {
			return nil, fmt.Errorf("parse request URL: %w", err)
		}
		domain := parsedURL.Hostname()

		req, err := fhttp.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte(data)))
		if err != nil {
			return nil, err
		}
		req.Host = domain
		browserprofile.ApplyFhttp(req, profile)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Origin", "https://vk.ru")
		req.Header.Set("Referer", "https://vk.ru/")
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Priority", "u=1, i")

		httpResp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := httpResp.Body.Close(); closeErr != nil {
				log.Printf("close response body: %s", closeErr)
			}
		}()

		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	}

	// Token 1: anonymous app token.
	data := fmt.Sprintf("client_id=%s&token_type=messages&client_secret=%s&version=1&app_id=%s", creds.ClientID, creds.ClientSecret, creds.ClientID)
	resp, err := doRequest(data, "https://login.vk.ru/?act=get_anonym_token")
	if err != nil {
		return "", "", nil, err
	}
	dataMap, ok := resp["data"].(map[string]any)
	if !ok {
		return "", "", nil, fmt.Errorf("unexpected anon token response: %v", resp)
	}
	token1, ok := dataMap["access_token"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing access_token in response: %v", resp)
	}

	vkDelayRandom(100, 150)

	// Token 1 -> getCallPreview (warmup, non-fatal).
	data = fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&fields=photo_200&access_token=%s", link, token1)
	if _, prevErr := doRequest(data, "https://api.vk.ru/method/calls.getCallPreview?v=5.275&client_id="+creds.ClientID); prevErr != nil {
		log.Printf("[STREAM %d] [VK Auth] Warning: getCallPreview failed: %v", streamID, prevErr)
	}

	vkDelayRandom(200, 400)

	// Token 2: anonymous call token (captcha may trigger here).
	data = fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&access_token=%s", link, escapedName, token1)
	urlAddr := fmt.Sprintf("https://api.vk.ru/method/calls.getAnonymousToken?v=5.275&client_id=%s", creds.ClientID)

	var token2 string
	for attempt := 0; ; attempt++ {
		resp, err = doRequest(data, urlAddr)
		if err != nil {
			return "", "", nil, err
		}

		if errObj, hasErr := resp["error"].(map[string]any); hasErr {
			captchaErr := captcha.ParseError(errObj)
			if captchaErr != nil && captchaErr.IsCaptcha() {
				solveMode, hasSolveMode := CaptchaSolveModeForAttempt(attempt, c.manualOnly)
				if !hasSolveMode {
					log.Printf("[STREAM %d] [Captcha] No more solve modes available (attempt %d)", streamID, attempt+1)
					c.engageLockout(60 * time.Second)
					if c.streamsFn() == 0 {
						log.Printf("[STREAM %d] [FATAL] 0 connected streams and captcha solve modes exhausted.", streamID)
						return "", "", nil, ErrFatalCaptchaNoStreams
					}
					return "", "", nil, ErrCaptchaWaitRequired
				}

				var successToken string
				var captchaKey string
				var solveErr error

				switch solveMode {
				case CaptchaSolveModeAuto:
					solveFn := c.autoSolver
					if solveFn == nil {
						solveFn = DefaultAutoSolve
					}
					if captchaErr.SessionToken != "" && captchaErr.RedirectURI != "" {
						successToken, solveErr = solveFn(ctx, captchaErr, streamID, client, profile)
						if solveErr != nil {
							log.Printf("[STREAM %d] [Captcha] Auto captcha failed: %v", streamID, solveErr)
						}
					} else {
						solveErr = fmt.Errorf("missing fields for auto solve")
					}
				case CaptchaSolveModeManual:
					if c.manualSolve == nil {
						solveErr = fmt.Errorf("manual captcha solver not configured")
						break
					}
					log.Printf("[STREAM %d] [Captcha] Triggering manual captcha fallback...", streamID)
					// Manual solver gets its own 3-min budget so a tight parent
					// deadline doesn't cut user solve time. We still propagate
					// parent cancellation (app shutdown) so the in-flight solver
					// goroutine doesn't outlive the process.
					manualCtx, manualCancel := context.WithTimeout(ctx, 3*time.Minute)

					type manualRes struct {
						token string
						key   string
						err   error
					}
					resCh := make(chan manualRes, 1)
					go func() {
						t, k, e := c.manualSolve(manualCtx, captchaErr, c.dialer)
						resCh <- manualRes{t, k, e}
					}()

					select {
					case res := <-resCh:
						successToken = res.token
						captchaKey = res.key
						solveErr = res.err
						if successToken != "" || captchaKey != "" {
							if solveErr != nil {
								log.Printf("[STREAM %d] [Captcha] Token received (ignoring cleanup error: %v)", streamID, solveErr)
								solveErr = nil
							}
							log.Printf("[STREAM %d] [Captcha] Successfully got token from browser", streamID)
						} else if solveErr != nil {
							log.Printf("[STREAM %d] [Captcha] manual solver returned error: %v", streamID, solveErr)
						}
					case <-manualCtx.Done():
						if errors.Is(manualCtx.Err(), context.DeadlineExceeded) {
							solveErr = fmt.Errorf("manual captcha timed out after 3m")
						} else {
							solveErr = fmt.Errorf("manual captcha interrupted: %w", manualCtx.Err())
						}
					}
					manualCancel()
				}

				if solveErr != nil {
					log.Printf("[STREAM %d] [Captcha] %s failed (attempt %d): %v", streamID, CaptchaSolveModeLabel(solveMode), attempt+1, solveErr)
					nextSolveMode, hasNextSolveMode := CaptchaSolveModeForAttempt(attempt+1, c.manualOnly)
					if hasNextSolveMode {
						log.Printf("[STREAM %d] [Captcha] Falling back to %s...", streamID, CaptchaSolveModeLabel(nextSolveMode))
						continue
					}
					c.engageLockout(60 * time.Second)
					if c.streamsFn() == 0 {
						log.Printf("[STREAM %d] [FATAL] 0 connected streams and manual captcha failed/timed out.", streamID)
						return "", "", nil, ErrFatalCaptchaNoStreams
					}
					return "", "", nil, ErrCaptchaWaitRequired
				}

				if captchaErr.CaptchaAttempt == "0" || captchaErr.CaptchaAttempt == "" {
					captchaErr.CaptchaAttempt = "1"
				}
				if captchaKey != "" {
					data = fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&captcha_key=%s&captcha_sid=%s&access_token=%s",
						link, escapedName, neturl.QueryEscape(captchaKey), captchaErr.CaptchaSid, token1)
				} else {
					data = fmt.Sprintf("vk_join_link=https://vk.com/call/join/%s&name=%s&captcha_key=&captcha_sid=%s&is_sound_captcha=0&success_token=%s&captcha_ts=%s&captcha_attempt=%s&access_token=%s",
						link, escapedName, captchaErr.CaptchaSid, neturl.QueryEscape(successToken), captchaErr.CaptchaTs, captchaErr.CaptchaAttempt, token1)
				}
				continue
			}
			return "", "", nil, fmt.Errorf("VK API error: %v", errObj)
		}

		respMap, okLoop := resp["response"].(map[string]any)
		if !okLoop {
			return "", "", nil, fmt.Errorf("unexpected getAnonymousToken response: %v", resp)
		}
		token2, okLoop = respMap["token"].(string)
		if !okLoop {
			return "", "", nil, fmt.Errorf("missing token in response: %v", resp)
		}
		break
	}

	vkDelayRandom(100, 150)

	// Token 3: ok.ru session_key.
	sessionData := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, uuid.New())
	data = fmt.Sprintf("session_data=%s&method=auth.anonymLogin&format=JSON&application_key=CGMMEJLGDIHBABABA", neturl.QueryEscape(sessionData))
	resp, err = doRequest(data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", "", nil, err
	}
	token3, ok := resp["session_key"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing session_key in response: %v", resp)
	}

	vkDelayRandom(100, 150)

	// Token 4 -> TURN creds.
	data = fmt.Sprintf("joinLink=%s&isVideo=false&protocolVersion=5&capabilities=2F7F&anonymToken=%s&method=vchat.joinConversationByLink&format=JSON&application_key=CGMMEJLGDIHBABABA&session_key=%s", link, token2, token3)
	resp, err = doRequest(data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", "", nil, err
	}
	tsRaw, ok := resp["turn_server"].(map[string]any)
	if !ok {
		return "", "", nil, fmt.Errorf("missing turn_server in response: %v", resp)
	}
	user, ok := tsRaw["username"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing username in turn_server")
	}
	pass, ok := tsRaw["credential"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing credential in turn_server")
	}
	urlsRaw, ok := tsRaw["urls"].([]any)
	if !ok || len(urlsRaw) == 0 {
		return "", "", nil, fmt.Errorf("missing or empty urls in turn_server")
	}

	log.Printf("[STREAM %d] [VK Auth] TURN urls (%d total):", streamID, len(urlsRaw))
	for i, u := range urlsRaw {
		log.Printf("[STREAM %d] [VK Auth]   [%d] %v", streamID, i, u)
	}

	var addresses []string
	for _, u := range urlsRaw {
		urlStr, ok := u.(string)
		if !ok {
			continue
		}
		clean := strings.Split(urlStr, "?")[0]
		address := strings.TrimPrefix(strings.TrimPrefix(clean, "turn:"), "turns:")
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return "", "", nil, fmt.Errorf("no valid TURN addresses found")
	}
	return user, pass, addresses, nil
}
