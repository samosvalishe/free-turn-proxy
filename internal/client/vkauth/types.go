package vkauth

import (
	"errors"
	"time"
)

// VKCredentials is one app_id/app_secret pair used to obtain anonymous tokens.
type VKCredentials struct {
	ClientID     string
	ClientSecret string
}

// TurnCredentials is the resolved TURN allocation for a stream group.
type TurnCredentials struct {
	Username    string
	Password    string
	ServerAddrs []string
	ExpiresAt   time.Time
	Link        string
}

// DefaultCredentials mirrors the public VK SDK app IDs the client tries in order.
var DefaultCredentials = []VKCredentials{
	{ClientID: "6287487", ClientSecret: "QbYic1K3lEV5kTGiqlq2"},  // VK_WEB_APP_ID
	{ClientID: "7879029", ClientSecret: "aR5NKGmm03GYrCiNKsaw"},  // VK_MVK_APP_ID
	{ClientID: "52461373", ClientSecret: "o557NLIkAErNhakXrQ7A"}, // VK_WEB_VKVIDEO_APP_ID
	{ClientID: "52649896", ClientSecret: "WStp4ihWG4l3nmXZgIbC"}, // VK_MVK_VKVIDEO_APP_ID
	{ClientID: "51781872", ClientSecret: "IjjCNl4L4Tf5QZEXIHKK"}, // VK_ID_AUTH_APP
}

const (
	CredentialLifetime = 10 * time.Minute
	CacheSafetyMargin  = 60 * time.Second
	MaxCacheErrors     = 3
	ErrorWindow        = 10 * time.Second

	DefaultStreamsPerCache = 10
)

// Sentinel errors produced by the auth flow. Callers use errors.Is.
// The string forms remain stable for log output.
var (
	ErrCaptchaWaitRequired   = errors.New("CAPTCHA_WAIT_REQUIRED")
	ErrFatalCaptchaNoStreams = errors.New("FATAL_CAPTCHA_FAILED_NO_STREAMS")
	ErrLockoutActive         = errors.New("global lockout active")
)
