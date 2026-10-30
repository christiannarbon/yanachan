// Package api wires the HTTP surface of the dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/board"
	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
	"github.com/christiannarbon/yanachan-4k/backend/internal/stats"
)

type Server struct {
	cfg config.Config
	// Local mode sets store and local; server mode sets kv and server.
	store  *state.Store
	local  *authService
	kv     kv.Store
	server *serverAuth
	// ra answers whose token and settings a request uses. See requestauth.go.
	ra requestAuth
	ui http.Handler

	// allowedHosts is the lowercased Host allowlist, loopback aside. Built once
	// at construction from the listen address, the dev origin and the operator's
	// GHDASH_ALLOWED_HOSTS.
	allowedHosts map[string]bool
}

func NewServer(cfg config.Config, store *state.Store, ui http.Handler) *Server {
	s := newServerBase(cfg, ui)
	s.store = store
	s.local = newAuthService(cfg, store)
	s.ra = &localAuth{svc: s.local, store: store}
	return s
}

// NewServerMode builds the server for GHDASH_AUTH_MODE=server: identity comes
// from the session cookie; sessions and per-user settings live in Redis/Valkey.
// It connects and pings (10s) before returning, so a wrong URL or password
// stops the process at startup instead of failing every sign-in later.
func NewServerMode(ctx context.Context, cfg config.Config, ui http.Handler) (*Server, error) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store, err := kv.NewRedis(pctx, kv.RedisOptions{
		URL:      cfg.RedisURL,
		Password: cfg.RedisPassword,
		CAFile:   cfg.RedisCAFile,
		Prefix:   cfg.RedisPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to redis: %w", err)
	}
	s, err := newServerModeWith(cfg, store, ui, time.Now)
	if err != nil {
		store.Close()
		return nil, err
	}
	return s, nil
}

// newServerModeWith is NewServerMode over a store and clock the caller provides.
func newServerModeWith(cfg config.Config, store kv.Store, ui http.Handler, now func() time.Time) (*Server, error) {
	ring, err := seal.NewKeyring(cfg.SessionKeys)
	if err != nil {
		return nil, fmt.Errorf("open session keyring: %w", err)
	}
	sessions := session.New(store, ring, cfg.SessionIdle, cfg.SessionMax)
	sessions.SetClock(now)
	users := state.NewUserSettings(store, state.DefaultSettings(cfg.DefaultLimit))

	s := newServerBase(cfg, ui)
	s.kv = store
	s.server = newServerAuth(cfg, sessions, users, ring)
	s.server.now = now
	s.ra = s.server
	return s, nil
}

func newServerBase(cfg config.Config, ui http.Handler) *Server {
	s := &Server{
		cfg:          cfg,
		ui:           ui,
		allowedHosts: map[string]bool{},
	}
	for _, h := range cfg.AllowedHosts {
		s.allowedHosts[hostname(h)] = true
	}
	// The address we were told to listen on is a legitimate Host by definition,
	// unless it is a wildcard, which never appears in a Host header.
	if h := hostname(cfg.Addr); h != "" && h != "0.0.0.0" && h != "::" {
		s.allowedHosts[h] = true
	}
	// Vite proxies with changeOrigin:false, so the browser's Host survives.
	if cfg.DevOrigin != "" {
		if u, err := url.Parse(cfg.DevOrigin); err == nil {
			s.allowedHosts[hostname(u.Host)] = true
		}
	}
	if h := cfg.PublicHost(); h != "" {
		s.allowedHosts[h] = true
	}
	delete(s.allowedHosts, "")
	return s
}

// Close releases the kv store in server mode; local mode has nothing to close.
func (s *Server) Close() error {
	if s.kv == nil {
		return nil
	}
	return s.kv.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now()})
	})

	mux.HandleFunc("GET /api/ready", s.handleReady)

	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	if s.local != nil {
		mux.HandleFunc("POST /api/auth/gh-cli/approve", s.handleApproveGhCLI)
		mux.HandleFunc("POST /api/auth/env-token/approve", s.handleApproveEnvToken)
		mux.HandleFunc("POST /api/auth/device/start", s.handleDeviceStart)
		mux.HandleFunc("POST /api/auth/device/poll", s.handleDevicePoll)
		mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	}
	if s.server != nil {
		mux.HandleFunc("GET /api/auth/login", s.server.handleLogin)
		mux.HandleFunc("GET /api/auth/callback", s.server.handleCallback)
		mux.HandleFunc("POST /api/auth/logout", s.server.handleLogout)
	}

	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/suggestions", s.handleSuggestions)
	mux.HandleFunc("GET /api/board", s.handleBoard)
	mux.HandleFunc("GET /api/stats", s.handleStats)

	// Anything under /api/ that did not match above is an API miss, not a
	// client-side route: answer in JSON rather than handing back index.html
	// with a 200, which the frontend would try to parse as an error body.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path)
	})

	if s.ui != nil {
		mux.Handle("/", s.ui)
	}
	return s.withMiddleware(mux)
}

// withMiddleware applies the Host allowlist, the response security headers,
// request logging, the dev-server CORS allowance and a same-origin guard on
// mutating requests.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health and readiness are exempt: Kubernetes probes address the pod by
		// IP, and neither endpoint returns anything worth stealing.
		if r.URL.Path != "/api/health" && r.URL.Path != "/api/ready" && !s.hostAllowed(r) {
			writeError(w, http.StatusForbidden, "unrecognised Host header; set GHDASH_ALLOWED_HOSTS to serve this app under that name")
			return
		}
		setSecurityHeaders(w, s.server != nil && s.cfg.Secure())
		if origin := r.Header.Get("Origin"); origin != "" && s.cfg.DevOrigin != "" && origin == s.cfg.DevOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// setSecurityHeaders locks down how a browser may treat our responses.
//
// frame-ancestors is the load-bearing one. This server holds a GitHub token and
// authenticates ambiently -- there is no cookie, so any request that arrives is
// already authorised. Without a framing ban, a remote page could iframe the
// dashboard and bait a click onto the "use my gh CLI session" button; the
// resulting POST is same-origin and sails past originAllowed, and the backend
// reads the real token. Refusing to be framed is what stops that.
//
// HSTS is for server mode over https only: local mode is plain-http loopback,
// where pinning the browser to https would break it.
func setSecurityHeaders(w http.ResponseWriter, hsts bool) {
	h := w.Header()
	if hsts {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		// Vue writes inline style attributes; the webfont stylesheets are the
		// only third-party resource the app loads (see composables/useTheme.ts).
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com",
		"img-src 'self' data:",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"form-action 'none'",
		"object-src 'none'",
	}, "; "))
}

// hostAllowed defends against DNS rebinding.
//
// originAllowed ends at "does Origin match Host", which an attacker satisfies
// for free: point evil.example at 127.0.0.1, and the browser sends
// Origin: http://evil.example:19080 with Host: evil.example:19080. They match,
// the CSRF guard passes, and -- worse -- the browser now treats the responses
// as same-origin and hands the attacker every pull request on the board. The
// only thing that actually stops it is refusing to answer to a Host we do not
// recognise, which is why loopback is the default and anything else has to be
// named explicitly.
func (s *Server) hostAllowed(r *http.Request) bool {
	if r.Host == "" {
		// HTTP/1.0 and raw socket clients. A browser always sends Host, so this
		// cannot be the rebinding case.
		return true
	}
	host := hostname(r.Host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return s.allowedHosts[host]
}

// hostname strips any port and IPv6 brackets, and lowercases what is left.
func hostname(hostport string) string {
	h := hostport
	if v, _, err := net.SplitHostPort(h); err == nil {
		h = v
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients such as curl send no Origin. Browsers always send
		// Sec-Fetch-Site, so its presence here means a browser stripped Origin
		// somehow and the request should not be trusted.
		return r.Header.Get("Sec-Fetch-Site") == ""
	}
	if s.cfg.DevOrigin != "" && origin == s.cfg.DevOrigin {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.server != nil {
		writeJSON(w, http.StatusOK, s.server.status(r))
		return
	}
	writeJSON(w, http.StatusOK, s.local.status(r.Context()))
}

// handleReady is the readiness probe. In server mode a pod that cannot reach
// Redis cannot serve anyone, so it should stop receiving traffic; liveness
// (/api/health) never touches Redis, so an outage does not restart pods.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.kv != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.kv.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now()})
}

func (s *Server) handleApproveGhCLI(w http.ResponseWriter, r *http.Request) {
	sess, err := s.local.approveGhCLI(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionView{Mode: sess.Mode, Login: sess.Login})
}

func (s *Server) handleApproveEnvToken(w http.ResponseWriter, r *http.Request) {
	sess, err := s.local.approveEnvToken(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionView{Mode: sess.Mode, Login: sess.Login})
}

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	dc, err := s.local.startDevice(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userCode":        dc.UserCode,
		"verificationUri": dc.VerificationURI,
		"expiresIn":       dc.ExpiresIn,
		"interval":        dc.Interval,
	})
}

func (s *Server) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	res, err := s.local.pollDevice(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.local.logout(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "signed out"})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.ra.Settings(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	got, err := st.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.ra.Settings(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var in state.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings payload: "+err.Error())
		return
	}
	if len(in.Teams) > state.MaxRefs || len(in.Orgs) > state.MaxRefs {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d teams and %d organizations may be followed", state.MaxRefs, state.MaxRefs))
		return
	}
	for _, team := range in.Teams {
		if !strings.Contains(strings.TrimSpace(team), "/") {
			writeError(w, http.StatusBadRequest, "team must be in org/team-slug form: "+team)
			return
		}
	}
	for _, org := range in.Orgs {
		if strings.Contains(strings.TrimSpace(org), "/") {
			writeError(w, http.StatusBadRequest, "org must be a bare organization login: "+org)
			return
		}
	}
	saved, err := st.Save(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	cl, _, err := s.ra.Client(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	m, err := cl.Memberships(r.Context())
	if err != nil && len(m.Orgs) == 0 && len(m.Teams) == 0 {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	payload := map[string]any{"orgs": m.Orgs, "teams": m.Teams}
	if err != nil {
		payload["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	cl, v, err := s.ra.Client(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	st, err := s.ra.Settings(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	settings, err := st.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	limit := settings.Limit
	if v := intParam(r, "limit"); v > 0 && v <= 100 {
		limit = v
	}
	hours := settings.WindowHours
	if v := intParam(r, "hours"); v > 0 && v <= state.MaxWindowHours {
		hours = v
	}
	onlyActive := settings.OnlyActive
	if v := r.URL.Query().Get("onlyActive"); v != "" {
		onlyActive, _ = strconv.ParseBool(v)
	}

	req := board.Request{
		Login:      v.Login,
		AuthMode:   v.Mode,
		Teams:      settings.Teams,
		Orgs:       settings.Orgs,
		Limit:      limit,
		Window:     board.ResolveWindow(time.Now(), hours),
		OnlyActive: onlyActive,
	}
	b, err := board.Build(r.Context(), cl, req)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errNoSession) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleStats answers the landing dashboard: the viewer's own week.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	cl, v, err := s.ra.Client(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	days := stats.DefaultDays
	if v := intParam(r, "days"); v > 0 && v <= stats.MaxDays {
		days = v
	}

	out, err := stats.Build(r.Context(), cl, stats.Request{
		Login: v.Login,
		Days:  days,
		Now:   time.Now(),
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errNoSession) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func intParam(r *http.Request, key string) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
