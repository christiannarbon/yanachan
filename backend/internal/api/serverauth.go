package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/access"
	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghauth"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghcli"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
)

// serverAuth is identity for server mode: every request acts as the GitHub
// user whose session cookie it carries.
type serverAuth struct {
	cfg      config.Config
	sessions *session.Store
	users    *state.UserSettings
	web      *ghauth.WebClient
	ring     *seal.Keyring
	kv       kv.Store // the refresh lock; set by newServerModeWith
	policy   access.Policy
	// allow decides whether a signed-in GitHub user may use this deployment.
	// It is policy.Check unless a test swaps it out.
	allow func(ctx context.Context, cl *github.Client, login string) error
	now   func() time.Time
}

const (
	sessionCookieBase = "yana-session"
	oauthCookieBase   = "yana-oauth"
	oauthCookieMaxAge = 600 // seconds
)

// errReauth: the token is past its maximum age and cannot be refreshed (OAuth App).
// The browser must re-authorise; the session itself stays.
var errReauth = errors.New("sign-in needs renewing")

// errUpstream: GitHub could not be reached and the current token is no longer usable.
var errUpstream = errors.New("github is unreachable; try again")

const (
	// refreshSkew: refresh this long before GitHub's own expiry, so a request that
	// starts just before the deadline does not reach GitHub with a dead token.
	refreshSkew = 5 * time.Minute
	// refreshLockTTL bounds how long a crashed replica can hold the refresh lock.
	refreshLockTTL = 30 * time.Second
	// refreshWait is how long a replica that lost the lock waits for the winner's result.
	refreshWait = 2 * time.Second
)

func newServerAuth(cfg config.Config, sessions *session.Store, users *state.UserSettings, ring *seal.Keyring) *serverAuth {
	a := &serverAuth{
		cfg:      cfg,
		sessions: sessions,
		users:    users,
		web: ghauth.NewWebClient(cfg.ClientID, cfg.ClientSecret, cfg.WebEndpoint,
			strings.TrimRight(cfg.PublicURL, "/")+"/api/auth/callback"),
		ring:   ring,
		policy: access.PolicyFromConfig(cfg),
		now:    time.Now,
	}
	a.allow = func(ctx context.Context, cl *github.Client, login string) error {
		return a.policy.Check(ctx, cl, login)
	}
	return a
}

// cookieName adds the __Host- prefix when cookies are Secure. The prefix makes
// the browser refuse the cookie unless it is Secure, Path=/ and host-only.
func cookieName(base string, secure bool) string {
	if secure {
		return "__Host-" + base
	}
	return base
}

func (a *serverAuth) setCookie(w http.ResponseWriter, base, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(base, a.cfg.Secure()),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cfg.Secure(),
	})
}

func (a *serverAuth) clearCookie(w http.ResponseWriter, base string) {
	a.setCookie(w, base, "", -1)
}

func authErrorRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/?authError="+url.QueryEscape(code), http.StatusFound)
}

type oauthRoundTrip struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Issued   int64  `json:"i"` // unix seconds; refused after oauthCookieMaxAge
	Return   string `json:"r"` // validated path to land on after sign-in, or ""
}

var returnPathRE = regexp.MustCompile(`^/[A-Za-z0-9/_-]*$`)

// safeReturn keeps only same-site paths, so the callback can never be turned
// into an open redirect. Anything else lands on "/".
func safeReturn(p string) string {
	if !returnPathRE.MatchString(p) || strings.HasPrefix(p, "//") {
		return ""
	}
	return p
}

func (a *serverAuth) handleLogin(w http.ResponseWriter, r *http.Request) {
	st, err := ghauth.NewState()
	if err != nil {
		authErrorRedirect(w, r, "server")
		return
	}
	verifier, challenge, err := ghauth.NewPKCE()
	if err != nil {
		authErrorRedirect(w, r, "server")
		return
	}
	raw, err := json.Marshal(oauthRoundTrip{
		State:    st,
		Verifier: verifier,
		Issued:   a.now().Unix(),
		Return:   safeReturn(r.URL.Query().Get("return")),
	})
	if err != nil {
		authErrorRedirect(w, r, "server")
		return
	}
	sealed, err := a.ring.Seal(raw, "oauth")
	if err != nil {
		authErrorRedirect(w, r, "server")
		return
	}
	a.setCookie(w, oauthCookieBase, sealed, oauthCookieMaxAge)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.web.AuthorizeURL(st, challenge), http.StatusFound)
}

func (a *serverAuth) handleCallback(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, oauthCookieBase)
	q := r.URL.Query()

	if q.Get("error") != "" {
		log.Printf("sign-in cancelled at github")
		authErrorRedirect(w, r, "denied")
		return
	}

	rt, ok := a.readRoundTrip(r)
	if !ok || subtle.ConstantTimeCompare([]byte(rt.State), []byte(q.Get("state"))) != 1 {
		authErrorRedirect(w, r, "state")
		return
	}
	code := q.Get("code")
	if code == "" {
		authErrorRedirect(w, r, "state")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tok, err := a.web.Exchange(ctx, code, rt.Verifier)
	if err != nil {
		log.Printf("sign-in failed: code exchange: %v", err)
		authErrorRedirect(w, r, "exchange")
		return
	}
	cl := github.New(a.cfg.GraphQLEndpoint, tok.AccessToken)
	login, id, err := cl.ViewerIdentity(ctx)
	if err != nil {
		log.Printf("sign-in failed: viewer identity: %v", err)
		a.revokeQuietly(tok.AccessToken, "unknown user")
		authErrorRedirect(w, r, "server")
		return
	}
	if err := a.allow(ctx, cl, login); err != nil {
		// A token we will never use must not outlive the request.
		a.revokeQuietly(tok.AccessToken, login)
		if errors.Is(err, access.ErrNotAllowed) {
			log.Printf("sign-in refused: %s (not allowed)", login)
			authErrorRedirect(w, r, "not_allowed")
			return
		}
		log.Printf("sign-in failed: %s (access check: %v)", login, err)
		authErrorRedirect(w, r, "server")
		return
	}

	now := a.now()
	sid := ""
	if c, err := r.Cookie(cookieName(sessionCookieBase, a.cfg.Secure())); err == nil {
		if old, err := a.sessions.Get(ctx, c.Value); err == nil {
			if old.UserID == id {
				rec := old
				rec.Token = tok.AccessToken
				rec.RefreshToken = tok.RefreshToken
				rec.TokenExpiresAt = tok.ExpiresAt
				rec.RefreshExpiresAt = tok.RefreshExpiresAt
				rec.TokenIssuedAt = now
				rec.VerifiedAt = now
				if err := a.sessions.Update(ctx, c.Value, rec); err != nil {
					log.Printf("sign-in failed: update session: %v", err)
					authErrorRedirect(w, r, "server")
					return
				}
				a.revokeQuietly(old.Token, login)
				log.Printf("token rotated: %s (%d)", login, id)
				sid = c.Value
			} else {
				a.endSessionRecord(ctx, c.Value, old, "another user signed in on this browser")
			}
		}
	}

	if sid == "" {
		sid, err = a.sessions.Create(ctx, session.Record{
			UserID:           id,
			Login:            login,
			Token:            tok.AccessToken,
			RefreshToken:     tok.RefreshToken,
			TokenIssuedAt:    now,
			TokenExpiresAt:   tok.ExpiresAt,
			RefreshExpiresAt: tok.RefreshExpiresAt,
			VerifiedAt:       now,
		})
		if err != nil {
			log.Printf("sign-in failed: create session: %v", err)
			authErrorRedirect(w, r, "server")
			return
		}
		log.Printf("sign-in: %s (%d)", login, id)
	}

	a.setCookie(w, sessionCookieBase, sid, int(a.cfg.SessionMax.Seconds()))
	dest := rt.Return
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (a *serverAuth) readRoundTrip(r *http.Request) (oauthRoundTrip, bool) {
	c, err := r.Cookie(cookieName(oauthCookieBase, a.cfg.Secure()))
	if err != nil || c.Value == "" {
		return oauthRoundTrip{}, false
	}
	raw, err := a.ring.Open(c.Value, "oauth")
	if err != nil {
		return oauthRoundTrip{}, false
	}
	var rt oauthRoundTrip
	if err := json.Unmarshal(raw, &rt); err != nil {
		return oauthRoundTrip{}, false
	}
	if a.now().Unix()-rt.Issued > oauthCookieMaxAge {
		return oauthRoundTrip{}, false
	}
	return rt, true
}

// revokeQuietly revokes a token we no longer use. Failure is logged, never
// returned: the user's sign-in must not fail because GitHub's revocation
// endpoint was slow. The token itself is never logged.
func (a *serverAuth) revokeQuietly(token, login string) {
	// Not tied to the request: a browser navigating away must not cancel it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.web.Revoke(ctx, a.cfg.RESTEndpoint, token); err != nil {
		log.Printf("revoke failed: %s (%v)", login, err)
	}
}

var _ requestAuth = (*serverAuth)(nil)

// current resolves the session cookie on r. It is the one place a request
// becomes a session.
func (a *serverAuth) current(r *http.Request) (id string, rec session.Record, err error) {
	c, err := r.Cookie(cookieName(sessionCookieBase, a.cfg.Secure()))
	if err != nil || c.Value == "" {
		return "", session.Record{}, errNoSession
	}
	rec, err = a.sessions.Get(r.Context(), c.Value)
	if err != nil {
		// Not found, expired, unopenable — all mean "sign in again". A Redis
		// outage also lands here; /api/ready is what reports that one.
		return "", session.Record{}, errNoSession
	}
	id = c.Value
	var reauth error
	rec, err = a.rotate(r.Context(), id, rec)
	switch {
	case errors.Is(err, errReauth):
		// The old token still works at GitHub; the session goes on until the
		// browser re-authorises.
		reauth = err
	case err != nil:
		return "", session.Record{}, err
	}
	if !a.policy.AllowAny && a.now().Sub(rec.VerifiedAt) > access.ReverifyAfter {
		cl := github.New(a.cfg.GraphQLEndpoint, rec.Token)
		switch err := a.allow(r.Context(), cl, rec.Login); {
		case err == nil:
			rec.VerifiedAt = a.now()
			_ = a.sessions.Update(r.Context(), id, rec)
		case errors.Is(err, access.ErrNotAllowed):
			a.endSessionRecord(r.Context(), id, rec, "no longer allowed")
			return "", session.Record{}, errNoSession
		default:
			// GitHub could not answer. Keep the session; try again next request.
			log.Printf("re-checking access for %s failed: %v", rec.Login, err)
		}
	}
	_ = a.sessions.Touch(r.Context(), id)
	return id, rec, reauth
}

func needsRotation(rec session.Record, now time.Time, maxAge time.Duration) bool {
	if now.Sub(rec.TokenIssuedAt) >= maxAge {
		return true
	}
	return !rec.TokenExpiresAt.IsZero() && !now.Add(refreshSkew).Before(rec.TokenExpiresAt)
}

func tokenUsable(rec session.Record, now time.Time) bool {
	return rec.TokenExpiresAt.IsZero() || now.Before(rec.TokenExpiresAt)
}

// rotate keeps a session's token within its maximum age. A GitHub App token is
// refreshed by one replica at a time; an OAuth App token can only be replaced
// by signing in again, which rotate reports as errReauth.
func (a *serverAuth) rotate(ctx context.Context, id string, rec session.Record) (session.Record, error) {
	now := a.now()
	// The record says which kind of app issued the token: a GitHub App with
	// token expiry turned off behaves like an OAuth App.
	if !needsRotation(rec, now, a.cfg.MaxTokenAge(rec.RefreshToken != "")) {
		return rec, nil
	}
	if rec.RefreshToken == "" {
		return rec, errReauth
	}
	if !rec.RefreshExpiresAt.IsZero() && now.After(rec.RefreshExpiresAt) {
		a.endSessionRecord(ctx, id, rec, "refresh token expired")
		return session.Record{}, errNoSession
	}

	lockKey := "lock:refresh:" + session.KeyHash(id)
	owner, err := ghauth.NewState()
	won := false
	if err == nil {
		won, err = a.kv.SetNX(ctx, lockKey, []byte(owner), refreshLockTTL)
	}
	if err != nil || !won {
		return a.awaitRefresh(ctx, id, rec, now)
	}
	defer a.kv.DeleteIfEqual(context.WithoutCancel(ctx), lockKey, []byte(owner))

	fresh, err := a.sessions.Get(ctx, id)
	if err != nil {
		return session.Record{}, errNoSession
	}
	if !needsRotation(fresh, now, a.cfg.MaxTokenAge(fresh.RefreshToken != "")) {
		return fresh, nil
	}
	tok, err := a.web.Refresh(ctx, fresh.RefreshToken)
	switch {
	case errors.Is(err, ghauth.ErrBadRefresh):
		a.endSessionRecord(ctx, id, fresh, "refresh refused by GitHub")
		return session.Record{}, errNoSession
	case err != nil && tokenUsable(fresh, now):
		log.Printf("token refresh failed for %s, using the current token: %v", fresh.Login, err)
		return fresh, nil
	case err != nil:
		log.Printf("token refresh failed for %s: %v", fresh.Login, err)
		return session.Record{}, errUpstream
	}
	fresh.Token = tok.AccessToken
	fresh.RefreshToken = tok.RefreshToken
	fresh.TokenExpiresAt = tok.ExpiresAt
	fresh.RefreshExpiresAt = tok.RefreshExpiresAt
	fresh.TokenIssuedAt = now
	if err := a.sessions.Update(ctx, id, fresh); err != nil {
		log.Printf("token refresh for %s not saved: %v", fresh.Login, err)
		return session.Record{}, errUpstream
	}
	log.Printf("token refreshed: %s (%d)", fresh.Login, fresh.UserID)
	return fresh, nil
}

// awaitRefresh waits for the replica holding the refresh lock to store the
// new token.
func (a *serverAuth) awaitRefresh(ctx context.Context, id string, rec session.Record, now time.Time) (session.Record, error) {
	deadline := time.Now().Add(refreshWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return session.Record{}, ctx.Err()
		case <-tick.C:
		}
		got, err := a.sessions.Get(ctx, id)
		if errors.Is(err, session.ErrNotFound) {
			return session.Record{}, errNoSession
		}
		if err == nil && got.TokenIssuedAt.After(rec.TokenIssuedAt) {
			return got, nil
		}
	}
	if tokenUsable(rec, now) {
		return rec, nil
	}
	return session.Record{}, errUpstream
}

// endSessionRecord deletes a session and revokes its token at GitHub. Used when a
// refresh is refused, a refresh token has expired, GitHub rejects the token, another
// user signs in on the same browser, access is withdrawn, and on sign-out.
func (a *serverAuth) endSessionRecord(ctx context.Context, id string, rec session.Record, reason string) {
	if err := a.sessions.Delete(ctx, id); err != nil {
		log.Printf("delete session for %s (%d): %v", rec.Login, rec.UserID, err)
	}
	if rec.Token != "" {
		a.revokeQuietly(rec.Token, rec.Login)
	}
	log.Printf("session ended: %s (%d) %s", rec.Login, rec.UserID, reason)
}

// endSession ends the session r's cookie names, if there is one.
func (a *serverAuth) endSession(r *http.Request, reason string) {
	c, err := r.Cookie(cookieName(sessionCookieBase, a.cfg.Secure()))
	if err != nil || c.Value == "" {
		return
	}
	rec, err := a.sessions.Get(r.Context(), c.Value)
	if err != nil {
		_ = a.sessions.Delete(r.Context(), c.Value)
		return
	}
	a.endSessionRecord(r.Context(), c.Value, rec, reason)
}

func (a *serverAuth) Client(r *http.Request) (*github.Client, viewer, error) {
	_, rec, err := a.current(r)
	if err != nil {
		return nil, viewer{}, err
	}
	return github.New(a.cfg.GraphQLEndpoint, rec.Token), viewer{Login: rec.Login, Mode: "github"}, nil
}

// Settings needs no GitHub token, so a session waiting to re-authorise can
// still read and save them.
func (a *serverAuth) Settings(r *http.Request) (settingsStore, error) {
	_, rec, err := a.current(r)
	if err != nil && !errors.Is(err, errReauth) {
		return nil, err
	}
	return userSettingsView{users: a.users, id: rec.UserID}, nil
}

// userSettingsView pins a UserSettings to one user, so handlers never see a user ID.
type userSettingsView struct {
	users *state.UserSettings
	id    int64
}

func (v userSettingsView) Get(ctx context.Context) (state.Settings, error) {
	return v.users.Get(ctx, v.id)
}

func (v userSettingsView) Save(ctx context.Context, in state.Settings) (state.Settings, error) {
	return v.users.Save(ctx, v.id, in)
}

func (a *serverAuth) status(r *http.Request) authStatus {
	st := authStatus{
		AuthMode: config.AuthServer,
		LoginURL: "/api/auth/login",
		Scopes:   ghauth.Scopes,
		GhCLI:    ghcli.Status{Detail: "not available in server mode"},
	}
	if _, rec, err := a.current(r); err == nil || errors.Is(err, errReauth) {
		st.Authenticated = true
		st.Session = &sessionView{Mode: "github", Login: rec.Login}
	}
	return st
}

func (a *serverAuth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName(sessionCookieBase, a.cfg.Secure())); err == nil && c.Value != "" {
		if rec, err := a.sessions.Get(r.Context(), c.Value); err == nil {
			a.endSessionRecord(r.Context(), c.Value, rec, "signed out")
			log.Printf("sign-out: %s (%d)", rec.Login, rec.UserID)
		} else {
			_ = a.sessions.Delete(r.Context(), c.Value)
		}
	}
	a.clearCookie(w, sessionCookieBase)
	a.clearCookie(w, oauthCookieBase)
	writeJSON(w, http.StatusOK, map[string]any{"status": "signed out"})
}
