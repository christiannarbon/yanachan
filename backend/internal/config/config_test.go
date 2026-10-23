package config

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var allVars = []string{
	"GHDASH_AUTH_MODE", "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GHDASH_PUBLIC_URL",
	"GHDASH_SESSION_KEYS", "GHDASH_ALLOWED_ORGS", "GHDASH_ALLOWED_USERS", "GHDASH_ALLOW_ANY_USER",
	"GHDASH_SESSION_IDLE", "GHDASH_SESSION_MAX", "GHDASH_TOKEN_MAX_AGE",
	"GHDASH_OAUTH_APP_TOKEN_MAX_AGE", "GHDASH_GITHUB_APP_TOKEN_MAX_AGE", "GHDASH_REST_ENDPOINT",
	"GHDASH_REDIS_URL", "GHDASH_REDIS_PASSWORD", "GHDASH_REDIS_CA_FILE", "GHDASH_REDIS_PREFIX",
	"GH_TOKEN", "GITHUB_TOKEN", "GHDASH_GITHUB_TOKEN", "GHDASH_ALLOW_GH_CLI", "GHDASH_STATE_DIR",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allVars {
		t.Setenv(k, "")
	}
}

func newKey(t *testing.T, n int) string {
	t.Helper()
	key := make([]byte, n)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// validServerEnv sets every variable a server-mode process needs. The key
// is generated per test; no key is ever committed.
func validServerEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("GHDASH_AUTH_MODE", "server")
	t.Setenv("GITHUB_CLIENT_ID", "Iv1.test")
	t.Setenv("GITHUB_CLIENT_SECRET", "test-secret")
	t.Setenv("GHDASH_PUBLIC_URL", "https://yana.example.com")
	t.Setenv("GHDASH_SESSION_KEYS", newKey(t, 32))
	t.Setenv("GHDASH_ALLOWED_ORGS", "acme")
	t.Setenv("GHDASH_REDIS_URL", "redis://127.0.0.1:6379/0")
}

func TestLocalModeIsTheDefault(t *testing.T) {
	clearEnv(t)
	c := Load()
	if c.AuthMode != AuthLocal {
		t.Fatalf("AuthMode = %q, want %q", c.AuthMode, AuthLocal)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !c.AllowGhCLI {
		t.Fatal("AllowGhCLI should default to true in local mode")
	}
}

func TestLocalModeIgnoresServerVariables(t *testing.T) {
	clearEnv(t)
	t.Setenv("GH_TOKEN", "ghp_local")
	t.Setenv("GHDASH_SESSION_KEYS", "not-base64")
	if err := Load().Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidServerConfig(t *testing.T) {
	validServerEnv(t)
	t.Setenv("GHDASH_ALLOW_GH_CLI", "true")
	t.Setenv("GHDASH_STATE_DIR", t.TempDir())
	c := Load()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !c.Secure() {
		t.Error("Secure() = false for an https URL")
	}
	if got := c.PublicHost(); got != "yana.example.com" {
		t.Errorf("PublicHost() = %q", got)
	}
	if c.SessionIdle != DefaultSessionIdle || c.SessionMax != DefaultSessionMax {
		t.Errorf("session bounds = %v/%v, want the defaults", c.SessionIdle, c.SessionMax)
	}
	if c.AllowGhCLI {
		t.Error("AllowGhCLI must be forced off in server mode")
	}
	for name, d := range map[string]time.Duration{
		"TokenMaxAge":          c.TokenMaxAge,
		"OAuthAppTokenMaxAge":  c.OAuthAppTokenMaxAge,
		"GitHubAppTokenMaxAge": c.GitHubAppTokenMaxAge,
	} {
		if d != 8*time.Hour {
			t.Errorf("%s = %v, want 8h", name, d)
		}
	}
	if c.RedisPrefix != "yana:" {
		t.Errorf("RedisPrefix = %q", c.RedisPrefix)
	}
	if c.StateDir != "" {
		t.Errorf("StateDir = %q, want empty in server mode", c.StateDir)
	}
}

func TestServerModeProblems(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing client ID", map[string]string{"GITHUB_CLIENT_ID": ""}, "GITHUB_CLIENT_ID is required"},
		{"missing secret", map[string]string{"GITHUB_CLIENT_SECRET": ""}, "GITHUB_CLIENT_SECRET is required"},
		{"missing public URL", map[string]string{"GHDASH_PUBLIC_URL": ""}, "GHDASH_PUBLIC_URL is required"},
		{"public URL with a path", map[string]string{"GHDASH_PUBLIC_URL": "https://x.example/yana"}, "GHDASH_PUBLIC_URL must be an origin"},
		{"public URL not http", map[string]string{"GHDASH_PUBLIC_URL": "ftp://x"}, "GHDASH_PUBLIC_URL must be an origin"},
		{"missing keys", map[string]string{"GHDASH_SESSION_KEYS": ""}, "GHDASH_SESSION_KEYS is required"},
		{"short key", map[string]string{"GHDASH_SESSION_KEYS": "AAAAAAAAAAAAAAAAAAAAAA=="}, "entry 1 is 16 bytes, want 32"},
		{"non-base64 key", map[string]string{"GHDASH_SESSION_KEYS": "not-base64!"}, "entry 1 is not valid base64"},
		{"empty policy", map[string]string{"GHDASH_ALLOWED_ORGS": ""}, "needs an access policy"},
		{"GH_TOKEN set", map[string]string{"GH_TOKEN": "ghp_x"}, "must be unset in server mode"},
		{"idle not a duration", map[string]string{"GHDASH_SESSION_IDLE": "nonsense"}, `GHDASH_SESSION_IDLE: "nonsense" is not a duration`},
		{"max shorter than idle", map[string]string{"GHDASH_SESSION_MAX": "1h", "GHDASH_SESSION_IDLE": "2h"}, "GHDASH_SESSION_MAX must not be shorter"},
		{"token age too short", map[string]string{"GHDASH_TOKEN_MAX_AGE": "5m"}, "GHDASH_TOKEN_MAX_AGE must be between"},
		{"token age over max", map[string]string{"GHDASH_TOKEN_MAX_AGE": "1000h"}, "GHDASH_TOKEN_MAX_AGE must be between"},
		{"OAuth App age too short", map[string]string{"GHDASH_OAUTH_APP_TOKEN_MAX_AGE": "5m"}, "GHDASH_OAUTH_APP_TOKEN_MAX_AGE must be between"},
		{"GitHub App age over max", map[string]string{"GHDASH_GITHUB_APP_TOKEN_MAX_AGE": "1000h"}, "GHDASH_GITHUB_APP_TOKEN_MAX_AGE must be between"},
		{"OAuth App age not a duration", map[string]string{"GHDASH_OAUTH_APP_TOKEN_MAX_AGE": "soon"}, `GHDASH_OAUTH_APP_TOKEN_MAX_AGE: "soon" is not a duration`},
		{"missing Redis URL", map[string]string{"GHDASH_REDIS_URL": ""}, "GHDASH_REDIS_URL is required"},
		{"Redis URL not redis", map[string]string{"GHDASH_REDIS_URL": "http://x:6379"}, "GHDASH_REDIS_URL must be redis://"},
		{"Redis URL with password", map[string]string{"GHDASH_REDIS_URL": "redis://:pw@x:6379"}, "must not contain a password"},
		{"CA file without TLS", map[string]string{"GHDASH_REDIS_CA_FILE": "/ca.pem"}, "GHDASH_REDIS_CA_FILE needs a rediss:// URL"},
		{"unknown mode", map[string]string{"GHDASH_AUTH_MODE": "sever"}, `GHDASH_AUTH_MODE must be "local" or "server"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validServerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := Load().Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestTokenMaxAgeOverrides(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		oauth, ghub time.Duration
	}{
		{"nothing", nil, 8 * time.Hour, 8 * time.Hour},
		{"shared only", map[string]string{"GHDASH_TOKEN_MAX_AGE": "4h"}, 4 * time.Hour, 4 * time.Hour},
		{"OAuth App only", map[string]string{"GHDASH_OAUTH_APP_TOKEN_MAX_AGE": "24h"}, 24 * time.Hour, 8 * time.Hour},
		{"GitHub App only", map[string]string{"GHDASH_GITHUB_APP_TOKEN_MAX_AGE": "1h"}, 8 * time.Hour, time.Hour},
		{"all three", map[string]string{
			"GHDASH_TOKEN_MAX_AGE":            "4h",
			"GHDASH_OAUTH_APP_TOKEN_MAX_AGE":  "24h",
			"GHDASH_GITHUB_APP_TOKEN_MAX_AGE": "30m",
		}, 24 * time.Hour, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validServerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			c := Load()
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
			if got := c.MaxTokenAge(false); got != tc.oauth {
				t.Errorf("MaxTokenAge(false) = %v, want %v", got, tc.oauth)
			}
			if got := c.MaxTokenAge(true); got != tc.ghub {
				t.Errorf("MaxTokenAge(true) = %v, want %v", got, tc.ghub)
			}
		})
	}
}

func TestOutOfRangeSharedAgeReportedOnce(t *testing.T) {
	validServerEnv(t)
	t.Setenv("GHDASH_TOKEN_MAX_AGE", "5m")
	err := Load().Validate()
	if err == nil || !strings.Contains(err.Error(), "GHDASH_TOKEN_MAX_AGE") {
		t.Fatalf("Validate() = %v, want GHDASH_TOKEN_MAX_AGE reported", err)
	}
	for _, v := range []string{"GHDASH_OAUTH_APP_TOKEN_MAX_AGE", "GHDASH_GITHUB_APP_TOKEN_MAX_AGE"} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("Validate() mentions %s, which is unset: %q", v, err)
		}
	}
}

func TestRedisACLUserAllowed(t *testing.T) {
	validServerEnv(t)
	t.Setenv("GHDASH_REDIS_URL", "rediss://yana@x:6378/0")
	t.Setenv("GHDASH_REDIS_CA_FILE", "/ca.pem")
	if err := Load().Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHDASH_AUTH_MODE", "server")
	err := Load().Validate()
	if err == nil {
		t.Fatal("Validate() = nil")
	}
	for _, want := range []string{
		"GITHUB_CLIENT_ID is required",
		"GITHUB_CLIENT_SECRET is required",
		"GHDASH_PUBLIC_URL is required",
		"GHDASH_SESSION_KEYS is required",
		"GHDASH_REDIS_URL is required",
		"needs an access policy",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() is missing %q", want)
		}
	}
}

func TestValidateNeverPrintsSecrets(t *testing.T) {
	clearEnv(t)
	key := newKey(t, 16)
	t.Setenv("GHDASH_AUTH_MODE", "server")
	t.Setenv("GITHUB_CLIENT_SECRET", "s3cr3t-value")
	t.Setenv("GH_TOKEN", "ghp_tokenvalue")
	t.Setenv("GHDASH_SESSION_KEYS", key)
	t.Setenv("GHDASH_REDIS_URL", "redis://:redispw-value@x:6379")
	err := Load().Validate()
	if err == nil {
		t.Fatal("Validate() = nil")
	}
	for _, secret := range []string{"s3cr3t-value", "ghp_tokenvalue", "redispw-value", key} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("Validate() leaks %q", secret)
		}
	}
}

func TestAllowAnyUserSatisfiesPolicy(t *testing.T) {
	validServerEnv(t)
	t.Setenv("GHDASH_ALLOWED_ORGS", "")
	t.Setenv("GHDASH_ALLOW_ANY_USER", "true")
	if err := Load().Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestHTTPPublicURLIsNotSecure(t *testing.T) {
	validServerEnv(t)
	t.Setenv("GHDASH_PUBLIC_URL", "http://localhost:19080")
	c := Load()
	if c.Secure() {
		t.Error("Secure() = true for an http URL")
	}
	if got := c.PublicHost(); got != "localhost" {
		t.Errorf("PublicHost() = %q", got)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}
