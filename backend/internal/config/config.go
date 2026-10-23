// Package config resolves runtime configuration from the environment.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Auth modes. The mode is always explicit, never inferred.
const (
	AuthLocal  = "local"
	AuthServer = "server"
)

const (
	DefaultSessionIdle = 168 * time.Hour
	DefaultSessionMax  = 720 * time.Hour
	DefaultTokenMaxAge = 8 * time.Hour
	MinTokenMaxAge     = 15 * time.Minute
	DefaultRedisPrefix = "yana:"
)

type Config struct {
	// Addr is the listen address for the HTTP server.
	Addr string
	// StateDir holds settings.json and session.json.
	StateDir string
	// GraphQLEndpoint is the GitHub GraphQL endpoint.
	GraphQLEndpoint string
	// RESTEndpoint is the GitHub REST base, used for device flow discovery only.
	WebEndpoint string
	// ClientID enables the OAuth device flow when set.
	ClientID string
	// AllowGhCLI permits the backend to read the local gh CLI session.
	AllowGhCLI bool
	// EnvToken is a token handed in by the environment (docker/k8s bridge).
	EnvToken string
	// DevOrigin, when set, is allowed as a CORS origin for the vite dev server.
	DevOrigin string
	// AllowedHosts are extra Host header values accepted beyond loopback, for
	// deployments reached under a real hostname. See api.hostAllowed.
	AllowedHosts []string
	// DefaultLimit is the per-query PR cap.
	DefaultLimit int

	// AuthMode is AuthLocal (default) or AuthServer.
	AuthMode string
	// ClientSecret is the GitHub App / OAuth App client secret. Server mode only.
	ClientSecret string
	// PublicURL is the origin users reach the app at. Server mode only.
	PublicURL string
	// SessionKeys seal tokens at rest. The first seals, all open.
	SessionKeys [][]byte
	// AllowedOrgs, AllowedUsers and AllowAnyUser are the server-mode access policy.
	AllowedOrgs  []string
	AllowedUsers []string
	AllowAnyUser bool
	// SessionIdle and SessionMax bound a server-mode session's life.
	SessionIdle time.Duration
	SessionMax  time.Duration
	// TokenMaxAge is the shared token rotation default; the two
	// overrides replace it per app kind. Read through MaxTokenAge.
	TokenMaxAge          time.Duration
	OAuthAppTokenMaxAge  time.Duration
	GitHubAppTokenMaxAge time.Duration
	// RESTEndpoint is the GitHub REST base, used for token revocation.
	RESTEndpoint string
	// RedisURL is redis:// or rediss://, without a password.
	RedisURL      string
	RedisPassword string
	RedisCAFile   string
	RedisPrefix   string

	// problems are parse failures found by Load, reported by Validate.
	problems []string
}

func Load() Config {
	c := Config{
		Addr:            env("GHDASH_ADDR", "127.0.0.1:19080"),
		StateDir:        env("GHDASH_STATE_DIR", defaultStateDir()),
		GraphQLEndpoint: env("GHDASH_GRAPHQL_ENDPOINT", "https://api.github.com/graphql"),
		WebEndpoint:     env("GHDASH_GITHUB_WEB", "https://github.com"),
		ClientID:        env("GITHUB_CLIENT_ID", ""),
		AllowGhCLI:      envBool("GHDASH_ALLOW_GH_CLI", true),
		EnvToken:        firstNonEmpty(os.Getenv("GHDASH_GITHUB_TOKEN"), os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")),
		DevOrigin:       env("GHDASH_DEV_ORIGIN", ""),
		AllowedHosts:    envList("GHDASH_ALLOWED_HOSTS"),
		DefaultLimit:    envInt("GHDASH_LIMIT", 25),
	}
	c.AuthMode = strings.ToLower(env("GHDASH_AUTH_MODE", AuthLocal))
	c.ClientSecret = env("GITHUB_CLIENT_SECRET", "")
	c.PublicURL = strings.TrimRight(env("GHDASH_PUBLIC_URL", ""), "/")
	c.AllowedOrgs = envList("GHDASH_ALLOWED_ORGS")
	c.AllowedUsers = envList("GHDASH_ALLOWED_USERS")
	c.AllowAnyUser = envBool("GHDASH_ALLOW_ANY_USER", false)
	c.RESTEndpoint = env("GHDASH_REST_ENDPOINT", "https://api.github.com")
	c.SessionKeys = c.parseKeys(os.Getenv("GHDASH_SESSION_KEYS"))
	c.SessionIdle = c.parseDuration("GHDASH_SESSION_IDLE", DefaultSessionIdle)
	c.SessionMax = c.parseDuration("GHDASH_SESSION_MAX", DefaultSessionMax)
	c.TokenMaxAge = c.parseDuration("GHDASH_TOKEN_MAX_AGE", DefaultTokenMaxAge)
	c.OAuthAppTokenMaxAge = c.parseDuration("GHDASH_OAUTH_APP_TOKEN_MAX_AGE", c.TokenMaxAge)
	c.GitHubAppTokenMaxAge = c.parseDuration("GHDASH_GITHUB_APP_TOKEN_MAX_AGE", c.TokenMaxAge)
	c.RedisURL = env("GHDASH_REDIS_URL", "")
	// Not env: a password may have leading or trailing spaces.
	c.RedisPassword = os.Getenv("GHDASH_REDIS_PASSWORD")
	c.RedisCAFile = env("GHDASH_REDIS_CA_FILE", "")
	c.RedisPrefix = env("GHDASH_REDIS_PREFIX", DefaultRedisPrefix)
	if c.AuthMode == AuthServer {
		// No gh in the image, and server mode writes nothing to disk.
		c.AllowGhCLI = false
		c.StateDir = ""
	}
	return c
}

// parseKeys reads comma-separated standard-base64 keys. A malformed entry is
// recorded by its position, never by its value.
func (c *Config) parseKeys(raw string) [][]byte {
	var keys [][]byte
	for i, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		k, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			c.problems = append(c.problems, fmt.Sprintf("GHDASH_SESSION_KEYS entry %d is not valid base64", i+1))
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

func (c *Config) parseDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		c.problems = append(c.problems, fmt.Sprintf("%s: %q is not a duration such as 168h", key, v))
		return fallback
	}
	return d
}

// Secure reports whether the public URL is https.
func (c Config) Secure() bool {
	u, err := url.Parse(c.PublicURL)
	return err == nil && u.Scheme == "https"
}

// PublicHost is the lowercased hostname of PublicURL, without port, or "".
func (c Config) PublicHost() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// MaxTokenAge picks the GitHub App age for sessions with a refresh token and
// the OAuth App age for those without.
func (c Config) MaxTokenAge(refreshable bool) time.Duration {
	if refreshable {
		return c.GitHubAppTokenMaxAge
	}
	return c.OAuthAppTokenMaxAge
}

// Validate reports every configuration problem at once, never a secret's value.
func (c Config) Validate() error {
	switch c.AuthMode {
	case AuthLocal:
		// Local mode never reads the server-mode variables, so a stray typo in
		// one of them must not stop a laptop from starting.
		return nil
	case AuthServer:
	default:
		return errors.New(`GHDASH_AUTH_MODE must be "local" or "server"`)
	}

	problems := append([]string(nil), c.problems...)
	add := func(msg string) { problems = append(problems, msg) }

	if c.ClientID == "" {
		add("GITHUB_CLIENT_ID is required in server mode")
	}
	if c.ClientSecret == "" {
		add("GITHUB_CLIENT_SECRET is required in server mode")
	}
	if c.PublicURL == "" {
		add("GHDASH_PUBLIC_URL is required in server mode")
	} else if u, err := url.Parse(c.PublicURL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		add("GHDASH_PUBLIC_URL must be an origin such as https://yana.example.com, with no path")
	}
	if len(c.SessionKeys) == 0 {
		add("GHDASH_SESSION_KEYS is required in server mode (generate one with: openssl rand -base64 32)")
	}
	for i, k := range c.SessionKeys {
		if len(k) != 32 {
			add(fmt.Sprintf("GHDASH_SESSION_KEYS entry %d is %d bytes, want 32", i+1, len(k)))
		}
	}
	if len(c.AllowedOrgs) == 0 && len(c.AllowedUsers) == 0 && !c.AllowAnyUser {
		add("server mode needs an access policy: set GHDASH_ALLOWED_ORGS, GHDASH_ALLOWED_USERS, or GHDASH_ALLOW_ANY_USER=true")
	}
	if c.EnvToken != "" {
		add("GH_TOKEN, GITHUB_TOKEN and GHDASH_GITHUB_TOKEN must be unset in server mode: every user signs in with their own account")
	}
	if c.SessionMax < c.SessionIdle {
		add("GHDASH_SESSION_MAX must not be shorter than GHDASH_SESSION_IDLE")
	}
	outOfRange := func(d time.Duration) bool { return d < MinTokenMaxAge || d > c.SessionMax }
	if outOfRange(c.TokenMaxAge) {
		add("GHDASH_TOKEN_MAX_AGE must be between 15m and GHDASH_SESSION_MAX")
	}
	// Only when set, so an out-of-range shared value is reported once.
	if env("GHDASH_OAUTH_APP_TOKEN_MAX_AGE", "") != "" && outOfRange(c.OAuthAppTokenMaxAge) {
		add("GHDASH_OAUTH_APP_TOKEN_MAX_AGE must be between 15m and GHDASH_SESSION_MAX")
	}
	if env("GHDASH_GITHUB_APP_TOKEN_MAX_AGE", "") != "" && outOfRange(c.GitHubAppTokenMaxAge) {
		add("GHDASH_GITHUB_APP_TOKEN_MAX_AGE must be between 15m and GHDASH_SESSION_MAX")
	}
	if c.RedisURL == "" {
		add("GHDASH_REDIS_URL is required in server mode (redis://host:6379/0 or rediss://host:6378/0)")
	} else if u, err := url.Parse(c.RedisURL); err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Host == "" {
		add("GHDASH_REDIS_URL must be redis://host:port[/db] or rediss://…")
	} else {
		// Never echo the URL here: it carries the password.
		if _, ok := u.User.Password(); ok {
			add("GHDASH_REDIS_URL must not contain a password; put it in GHDASH_REDIS_PASSWORD")
		}
		if c.RedisCAFile != "" && u.Scheme == "redis" {
			add("GHDASH_REDIS_CA_FILE needs a rediss:// URL")
		}
	}

	errs := make([]error, len(problems))
	for i, p := range problems {
		errs[i] = errors.New(p)
	}
	return errors.Join(errs...)
}

func (c Config) SettingsPath() string { return filepath.Join(c.StateDir, "settings.json") }
func (c Config) SessionPath() string  { return filepath.Join(c.StateDir, "session.json") }

// defaultStateDir picks where settings.json and session.json live.
//
// The app was called github-dashboarder before it was called Yana-chan 4K, and
// the stored gh session is the expensive thing to lose -- losing it means
// authenticating again. So if the old directory is present and the new one is
// not, we keep reading the old one rather than silently starting fresh. Once
// the new directory exists it always wins.
func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".yana"
	}
	current := filepath.Join(dir, "yana-chan-4k")
	if _, err := os.Stat(current); err == nil {
		return current
	}
	legacy := filepath.Join(dir, "github-dashboarder")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// envList reads a comma-separated list, dropping empty entries.
func envList(key string) []string {
	var out []string
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
