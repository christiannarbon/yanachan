package api

import (
	"context"
	"net/http"

	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
)

// requestAuth resolves who a request acts as. Handlers only ever ask this
// interface, so no handler can reach for "the" token.
type requestAuth interface {
	// Client returns the GitHub client and viewer for r, or errNoSession.
	Client(r *http.Request) (*github.Client, viewer, error)
	// Settings returns the settings r reads and writes, or errNoSession.
	Settings(r *http.Request) (settingsStore, error)
}

type viewer struct {
	Login string
	Mode  string // "gh-cli" | "oauth" | "env-token" (local) or "github" (server)
}

type settingsStore interface {
	Get(ctx context.Context) (state.Settings, error)
	Save(ctx context.Context, in state.Settings) (state.Settings, error)
}

// localAuth is the single-user behaviour behind the seam.
type localAuth struct {
	svc   *authService
	store *state.Store
}

func (l *localAuth) Client(r *http.Request) (*github.Client, viewer, error) {
	cl, sess, err := l.svc.client()
	if err != nil {
		return nil, viewer{}, err
	}
	return cl, viewer{Login: sess.Login, Mode: sess.Mode}, nil
}

// Settings never fails in local mode: settings are readable and writable
// before anyone signs in.
func (l *localAuth) Settings(r *http.Request) (settingsStore, error) {
	return storeSettings{l.store}, nil
}

// storeSettings adapts state.Store to settingsStore.
type storeSettings struct{ store *state.Store }

func (s storeSettings) Get(ctx context.Context) (state.Settings, error) {
	return s.store.Settings(), nil
}

func (s storeSettings) Save(ctx context.Context, in state.Settings) (state.Settings, error) {
	return s.store.SaveSettings(in)
}
