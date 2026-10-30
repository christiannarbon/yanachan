package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghauth"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghcli"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
)

// Auth modes.
const (
	ModeGhCLI    = "gh-cli"
	ModeOAuth    = "oauth"
	ModeEnvToken = "env-token"
)

var errNoSession = errors.New("not authenticated")

// authService owns the token lifecycle. A token is only ever read after an
// explicit approval call from the frontend.
type authService struct {
	cfg    config.Config
	store  *state.Store
	device *ghauth.Client

	mu      sync.Mutex
	pending *pendingDevice
}

type pendingDevice struct {
	code      string
	expiresAt time.Time
	interval  int
}

func newAuthService(cfg config.Config, store *state.Store) *authService {
	return &authService{
		cfg:    cfg,
		store:  store,
		device: ghauth.NewClient(cfg.ClientID, cfg.WebEndpoint),
	}
}

type sessionView struct {
	Mode  string `json:"mode"`
	Login string `json:"login"`
}

type authStatus struct {
	AuthMode      string       `json:"authMode"`           // "local" | "server"
	LoginURL      string       `json:"loginUrl,omitempty"` // server mode only
	Authenticated bool         `json:"authenticated"`
	Session       *sessionView `json:"session"`
	GhCLI         ghcli.Status `json:"ghCli"`
	GhCLIAllowed  bool         `json:"ghCliAllowed"`
	OAuthEnabled  bool         `json:"oauthEnabled"`
	EnvToken      bool         `json:"envTokenAvailable"`
	Scopes        string       `json:"oauthScopes"`
	Pending       *pendingView `json:"pendingDevice,omitempty"`
}

type pendingView struct {
	ExpiresAt time.Time `json:"expiresAt"`
	Interval  int       `json:"interval"`
}

func (a *authService) status(ctx context.Context) authStatus {
	st := authStatus{
		AuthMode:     config.AuthLocal,
		GhCLIAllowed: a.cfg.AllowGhCLI,
		OAuthEnabled: a.cfg.ClientID != "",
		EnvToken:     a.cfg.EnvToken != "",
		Scopes:       ghauth.Scopes,
	}
	if a.cfg.AllowGhCLI {
		st.GhCLI = ghcli.Detect(ctx)
	} else {
		st.GhCLI = ghcli.Status{Detail: "reading the local gh CLI session is disabled by configuration"}
	}
	if sess := a.store.Session(); sess != nil {
		st.Authenticated = true
		st.Session = &sessionView{Mode: sess.Mode, Login: sess.Login}
	}
	a.mu.Lock()
	if a.pending != nil && time.Now().Before(a.pending.expiresAt) {
		st.Pending = &pendingView{ExpiresAt: a.pending.expiresAt, Interval: a.pending.interval}
	}
	a.mu.Unlock()
	return st
}

// approveGhCLI is the consent step: the user has agreed to let the dashboard
// borrow the gh CLI session on this machine.
func (a *authService) approveGhCLI(ctx context.Context) (*state.Session, error) {
	if !a.cfg.AllowGhCLI {
		return nil, errors.New("reading the local gh CLI session is disabled by configuration")
	}
	st := ghcli.Detect(ctx)
	if !st.Available {
		return nil, errors.New("gh CLI was not found on PATH")
	}
	if !st.Authenticated {
		return nil, errors.New("gh CLI is installed but not logged in; run 'gh auth login' first")
	}
	token, err := ghcli.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read the gh CLI token: %w", err)
	}
	return a.persist(ctx, ModeGhCLI, token)
}

func (a *authService) approveEnvToken(ctx context.Context) (*state.Session, error) {
	if a.cfg.EnvToken == "" {
		return nil, errors.New("no token was supplied by the environment")
	}
	return a.persist(ctx, ModeEnvToken, a.cfg.EnvToken)
}

func (a *authService) persist(ctx context.Context, mode, token string) (*state.Session, error) {
	login, err := github.New(a.cfg.GraphQLEndpoint, token).Viewer(ctx)
	if err != nil {
		return nil, fmt.Errorf("token check failed: %w", err)
	}
	sess := state.Session{Mode: mode, Login: login, Token: token, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := a.store.SaveSession(sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (a *authService) startDevice(ctx context.Context) (*ghauth.DeviceCode, error) {
	dc, err := a.device.StartDeviceFlow(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.pending = &pendingDevice{
		code:      dc.DeviceCode,
		expiresAt: time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second),
		interval:  dc.Interval,
	}
	a.mu.Unlock()
	return dc, nil
}

type devicePollResult struct {
	State   string       `json:"state"` // pending | slow_down | complete | expired | denied
	Session *sessionView `json:"session,omitempty"`
}

func (a *authService) pollDevice(ctx context.Context) (devicePollResult, error) {
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return devicePollResult{}, errors.New("no device authorization is in progress")
	}
	if time.Now().After(pending.expiresAt) {
		a.clearPending()
		return devicePollResult{State: "expired"}, nil
	}
	token, err := a.device.PollDeviceFlow(ctx, pending.code)
	switch {
	case err == nil:
	case errors.Is(err, ghauth.ErrPending):
		return devicePollResult{State: "pending"}, nil
	case errors.Is(err, ghauth.ErrSlowDown):
		return devicePollResult{State: "slow_down"}, nil
	case errors.Is(err, ghauth.ErrExpiredToken):
		a.clearPending()
		return devicePollResult{State: "expired"}, nil
	case errors.Is(err, ghauth.ErrAccessDenied):
		a.clearPending()
		return devicePollResult{State: "denied"}, nil
	default:
		return devicePollResult{}, err
	}
	sess, err := a.persist(ctx, ModeOAuth, token)
	if err != nil {
		return devicePollResult{}, err
	}
	a.clearPending()
	return devicePollResult{State: "complete", Session: &sessionView{Mode: sess.Mode, Login: sess.Login}}, nil
}

func (a *authService) clearPending() {
	a.mu.Lock()
	a.pending = nil
	a.mu.Unlock()
}

func (a *authService) logout() error {
	a.clearPending()
	return a.store.ClearSession()
}

// client returns a GraphQL client for the active session.
func (a *authService) client() (*github.Client, *state.Session, error) {
	sess := a.store.Session()
	if sess == nil {
		return nil, nil, errNoSession
	}
	return github.New(a.cfg.GraphQLEndpoint, sess.Token), sess, nil
}
