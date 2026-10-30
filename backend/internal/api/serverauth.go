package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghauth"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
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
	// allow decides whether a signed-in GitHub user may use this deployment.
	// Everyone is allowed until the access policy replaces it.
	allow func(ctx context.Context, cl *github.Client, login string) error
	now   func() time.Time
}

const (
	sessionCookieBase = "yana-session"
	oauthCookieBase   = "yana-oauth"
	oauthCookieMaxAge = 600 // seconds
)

func newServerAuth(cfg config.Config, sessions *session.Store, users *state.UserSettings, ring *seal.Keyring) *serverAuth {
	return &serverAuth{
		cfg:      cfg,
		sessions: sessions,
		users:    users,
		web: ghauth.NewWebClient(cfg.ClientID, cfg.ClientSecret, cfg.WebEndpoint,
			strings.TrimRight(cfg.PublicURL, "/")+"/api/auth/callback"),
		ring:  ring,
		allow: func(context.Context, *github.Client, string) error { return nil },
		now:   time.Now,
	}
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
		authErrorRedirect(w, r, "server")
		return
	}
	if err := a.allow(ctx, cl, login); err != nil {
		log.Printf("sign-in refused: %s (%v)", login, err)
		authErrorRedirect(w, r, "not_allowed")
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
				if err := a.sessions.Delete(ctx, c.Value); err != nil {
					log.Printf("end previous session: %s (%v)", old.Login, err)
				}
				a.revokeQuietly(old.Token, old.Login)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.web.Revoke(ctx, a.cfg.RESTEndpoint, token); err != nil {
		log.Printf("revoke failed: %s (%v)", login, err)
	}
}
