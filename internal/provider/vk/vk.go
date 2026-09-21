package vk

import (
	"context"
	"fmt"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"
	manualcaptcha "github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha/manual"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/vkauth"
	"github.com/samosvalishe/free-turn-proxy/internal/statedir"
)

type Config struct {
	Link            string
	Dialer          net.Dialer
	ManualOnly      bool
	Platform        string
	StreamsPerCache int
	StreamsAlive    func() int32
	Credentials     []vkauth.VKCredentials
	FingerprintSeed string
	StatePaths      []string
	Log             logx.Logger
	Debug           bool
}

// ManualSolverFunc определяет сигнатуру функции для интерактивного решения капчи.
type ManualSolverFunc = vkauth.ManualSolveFunc

type Provider struct {
	link string
	auth *vkauth.Client
}

// New создаёт провайдер авторизации VK.
func New(cfg Config, solver ManualSolverFunc) (*Provider, error) {
	if cfg.Link == "" {
		return nil, fmt.Errorf("vk: empty Link")
	}
	captcha.SetLogger(cfg.Log)
	manualcaptcha.SetLogger(cfg.Log)
	manualcaptcha.SetDebug(cfg.Debug)
	auth := vkauth.New(vkauth.Config{
		Credentials:     cfg.Credentials,
		Dialer:          cfg.Dialer,
		ManualOnly:      cfg.ManualOnly,
		Platform:        browserprofile.PlatformFromString(cfg.Platform),
		StreamsPerCache: cfg.StreamsPerCache,
		StreamsAlive:    cfg.StreamsAlive,
		ManualSolver:    solver,
		FingerprintSeed: cfg.FingerprintSeed,
		StatePaths:      cfg.StatePaths,
		Log:             cfg.Log,
	})
	return &Provider{link: cfg.Link, auth: auth}, nil
}

func DefaultStatePaths() []string { return statedir.Paths(vkauth.PersonaStateFile) }

func (p *Provider) GetCredentials(ctx context.Context, streamID int) (provider.Credentials, error) {
	user, pass, addrs, err := p.auth.GetCredentials(ctx, p.link, streamID)
	if err != nil {
		return provider.Credentials{}, err
	}
	return provider.Credentials{User: user, Pass: pass, ServerAddrs: addrs}, nil
}

func (p *Provider) IsAuthError(err error) bool { return p.auth.IsAuthError(err) }

func (p *Provider) HandleAuthError(streamID int) bool { return p.auth.HandleAuthError(streamID) }

func (p *Provider) ResetErrors(streamID int) { p.auth.ResetErrors(streamID) }

func (p *Provider) DropCredentials(streamID int) { p.auth.DropCredentials(streamID) }

func (p *Provider) BackoffUntilUnix() int64 { return p.auth.BackoffUntilUnix() }

func (*Provider) Name() string { return "vk" }

func DefaultManualSolver(ctx context.Context, e *captcha.Error, d net.Dialer, p browserprofile.Profile) (string, error) {
	if e.RedirectURI == "" {
		return "", fmt.Errorf("no redirect_uri")
	}
	return manualcaptcha.SolveViaProxy(ctx, e.RedirectURI, d, p)
}
