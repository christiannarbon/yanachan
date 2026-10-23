// Package githubfake is an in-process stand-in for the parts of GitHub that
// server mode talks to. Tests only: nothing outside a _test.go file may import it.
package githubfake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	ClientID     = "fake-client-id"
	ClientSecret = "fake-client-secret"
)

type User struct {
	Login string
	ID    int64
	Orgs  []string
}

type Server struct {
	URL string
	// ExpiringTokens makes token responses look like a GitHub App with
	// expiring user tokens: expires_in, refresh_token, refresh_token_expires_in.
	ExpiringTokens bool

	mu             sync.Mutex
	users          map[string]User
	signedIn       string
	deny           bool
	codes          map[string]*grant
	access         map[string]*accessToken
	refresh        map[string]*refreshToken
	latest         map[string]string
	revoked        []string
	seen           []string
	authorizeCount int
	failRevoke     bool
}

type grant struct {
	login     string
	challenge string
	redirect  string
	used      bool
}

type accessToken struct {
	login   string
	refresh string
	expired bool
	revoked bool
}

type refreshToken struct {
	login  string
	access string
	used   bool
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		users:   map[string]User{},
		codes:   map[string]*grant{},
		access:  map[string]*accessToken{},
		refresh: map[string]*refreshToken{},
		latest:  map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.handleAccessToken)
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("DELETE /applications/{client_id}/token", s.handleRevoke)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "githubfake: no route for "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.URL = ts.URL
	return s
}

// AddUser adds u, replacing any existing user with the same Login.
func (s *Server) AddUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.Login] = u
}

// SignInAs sets who is signed in to GitHub. Every authorize from now on
// approves as them straight away, as GitHub does for an app already authorised.
func (s *Server) SignInAs(login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signedIn = login
}

// Deny makes the user refuse the next authorize.
func (s *Server) Deny() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = true
}

func (s *Server) ExpireAccessToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.access[token]; ok {
		a.expired = true
	}
}

// Revoked lists the access tokens revoked through the REST endpoint.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// TokensSeen lists the bearer tokens seen on /graphql, in order.
func (s *Server) TokensSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// AccessTokenFor is the latest access token issued to login.
func (s *Server) AccessTokenFor(login string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest[login]
}

// AuthorizeCount is how many authorize requests were answered with a code.
func (s *Server) AuthorizeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorizeCount
}

// FailRevoke makes token revocation answer 500 until ResumeRevoke.
func (s *Server) FailRevoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRevoke = true
}

func (s *Server) ResumeRevoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRevoke = false
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID {
		http.Error(w, "githubfake: unknown client_id", http.StatusNotFound)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "githubfake: PKCE with S256 is required", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "githubfake: bad redirect_uri", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	back := redirect.Query()
	back.Set("state", q.Get("state"))
	switch {
	case s.deny:
		s.deny = false
		back.Set("error", "access_denied")
		back.Set("error_description", "The user has denied your application access.")
	case s.signedIn == "":
		http.Error(w, "githubfake: SignInAs was not called", http.StatusInternalServerError)
		return
	default:
		code := random("code_fake_")
		s.codes[code] = &grant{
			login:     s.signedIn,
			challenge: q.Get("code_challenge"),
			redirect:  q.Get("redirect_uri"),
		}
		s.authorizeCount++
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) handleAccessToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "bad_request")
		return
	}
	if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret {
		oauthError(w, "incorrect_client_credentials")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var login string
	if r.PostForm.Get("grant_type") == "refresh_token" {
		rt, ok := s.refresh[r.PostForm.Get("refresh_token")]
		if !ok || rt.used {
			oauthError(w, "bad_refresh_token")
			return
		}
		// GitHub retires the old access token along with the refresh token.
		rt.used = true
		if old, ok := s.access[rt.access]; ok {
			old.expired = true
		}
		login = rt.login
	} else {
		g, ok := s.codes[r.PostForm.Get("code")]
		if !ok || g.used {
			oauthError(w, "bad_verification_code")
			return
		}
		g.used = true
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			oauthError(w, "bad_verification_code")
			return
		}
		if ru := r.PostForm.Get("redirect_uri"); ru != "" && ru != g.redirect {
			oauthError(w, "redirect_uri_mismatch")
			return
		}
		login = g.login
	}

	token := random("gho_fake_")
	body := map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"scope":        "repo,read:org",
	}
	at := &accessToken{login: login}
	if s.ExpiringTokens {
		rt := random("ghr_fake_")
		s.refresh[rt] = &refreshToken{login: login, access: token}
		at.refresh = rt
		body["expires_in"] = 28800
		body["refresh_token"] = rt
		body["refresh_token_expires_in"] = 15897600
	}
	s.access[token] = at
	s.latest[login] = token
	writeJSON(w, http.StatusOK, body)
}

var orgVar = regexp.MustCompile(`^o[0-9]+$`)

func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, token)
	at, ok := s.access[token]
	if !ok || at.expired || at.revoked {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Problems parsing JSON"})
		return
	}
	user := s.users[at.login]
	if user.Login == "" {
		user.Login = at.login
	}

	switch {
	case strings.Contains(req.Query, "databaseId"):
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"viewer": map[string]any{"login": user.Login, "databaseId": user.ID},
		}})
	case strings.Contains(req.Query, "viewerIsAMember"):
		data := map[string]any{}
		var errs []map[string]string
		for name, v := range req.Variables {
			if !orgVar.MatchString(name) {
				continue
			}
			org, _ := v.(string)
			if !s.orgExists(org) {
				data[name] = nil
				errs = append(errs, map[string]string{
					"type":    "NOT_FOUND",
					"message": fmt.Sprintf("Could not resolve to an Organization with the login of '%s'.", org),
				})
				continue
			}
			data[name] = map[string]bool{"viewerIsAMember": hasOrg(user, org)}
		}
		body := map[string]any{"data": data}
		if len(errs) > 0 {
			body["errors"] = errs
		}
		writeJSON(w, http.StatusOK, body)
	case strings.Contains(req.Query, "organizations("):
		nodes := []map[string]any{}
		for _, org := range user.Orgs {
			nodes = append(nodes, map[string]any{"login": org, "teams": map[string]any{"nodes": []any{}}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"viewer": map[string]any{"organizations": map[string]any{"nodes": nodes}},
		}})
	case strings.Contains(req.Query, "viewer"):
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"viewer": map[string]any{"login": user.Login},
		}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]string{
			{"message": "githubfake: unsupported query"},
		}})
	}
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRevoke {
		http.Error(w, "githubfake: revocation is failing", http.StatusInternalServerError)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok || id != ClientID || secret != ClientSecret || r.PathValue("client_id") != ClientID {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	at, found := s.access[body.AccessToken]
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	at.revoked = true
	if rt, ok := s.refresh[at.refresh]; ok {
		rt.used = true
	}
	s.revoked = append(s.revoked, body.AccessToken)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) orgExists(org string) bool {
	for _, u := range s.users {
		if hasOrg(u, org) {
			return true
		}
	}
	return false
}

func hasOrg(u User, org string) bool {
	for _, o := range u.Orgs {
		if strings.EqualFold(o, org) {
			return true
		}
	}
	return false
}

func oauthError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusOK, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func random(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
