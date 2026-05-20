package vkauth

import (
	"context"
	"fmt"
	neturl "net/url"

	"github.com/google/uuid"
	"github.com/samosvalishe/btp/internal/client/browserprofile"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchOkRuSession performs step 3 of the token chain: it obtains an anonymous
// ok.ru session_key via auth.anonymLogin.
func (c *Client) fetchOkRuSession(ctx context.Context, httpClient tlsclient.HttpClient, profile browserprofile.Profile) (string, error) {
	sessionData := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, uuid.New())
	data := fmt.Sprintf("session_data=%s&method=auth.anonymLogin&format=JSON&application_key=CGMMEJLGDIHBABABA",
		neturl.QueryEscape(sessionData))
	resp, err := c.doRequest(ctx, httpClient, profile, data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", err
	}
	sessionKey, ok := resp["session_key"].(string)
	if !ok {
		return "", fmt.Errorf("missing session_key in response: %v", resp)
	}
	return sessionKey, nil
}
