package ghauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/ghauth"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
)

const callback = "http://localhost:19080/api/auth/callback"

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func newWebClient(t *testing.T, expiring bool) (*githubfake.Server, *ghauth.WebClient) {
	t.Helper()
	fake := githubfake.New(t)
	fake.ExpiringTokens = expiring
	fake.SignInAs("alice")
	return fake, ghauth.NewWebClient(githubfake.ClientID, githubfake.ClientSecret, fake.URL, callback)
}

func newPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	verifier, challenge, err := ghauth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	return verifier, challenge
}

func authorize(t *testing.T, fake *githubfake.Server, wc *ghauth.WebClient, state, challenge string) string {
	t.Helper()
	resp, err := noRedirect.Get(wc.AuthorizeURL(state, challenge))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != state {
		t.Fatalf("state = %q, want %q", got, state)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}
	return code
}

func signIn(t *testing.T, fake *githubfake.Server, wc *ghauth.WebClient) ghauth.Token {
	t.Helper()
	verifier, challenge := newPKCE(t)
	code := authorize(t, fake, wc, "st", challenge)
	tok, err := wc.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestAuthorizeURL(t *testing.T) {
	wc := ghauth.NewWebClient("cid", "secret", "https://github.example/", callback)
	u, err := url.Parse(wc.AuthorizeURL("st-1", "ch-1"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "github.example" || u.Path != "/login/oauth/authorize" {
		t.Fatalf("url = %s", u)
	}
	want := map[string]string{
		"client_id":             "cid",
		"redirect_uri":          callback,
		"scope":                 ghauth.Scopes,
		"state":                 "st-1",
		"code_challenge":        "ch-1",
		"code_challenge_method": "S256",
		"allow_signup":          "false",
	}
	q := u.Query()
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if q.Has("client_secret") {
		t.Error("authorize url carries the client secret")
	}
}

func TestPKCEChallengeMatches(t *testing.T) {
	v1, c1 := newPKCE(t)
	v2, _ := newPKCE(t)
	sum := sha256.Sum256([]byte(v1))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); c1 != want {
		t.Fatalf("challenge = %q, want %q", c1, want)
	}
	if v1 == v2 {
		t.Fatal("two calls gave the same verifier")
	}
	if len(v1) != 43 || len(v2) != 43 {
		t.Fatalf("verifier lengths = %d, %d, want 43", len(v1), len(v2))
	}
}

func TestExchangeNonExpiring(t *testing.T) {
	fake, wc := newWebClient(t, false)
	tok := signIn(t, fake, wc)
	if tok.AccessToken == "" || tok.AccessToken != fake.AccessTokenFor("alice") {
		t.Fatalf("access token = %q", tok.AccessToken)
	}
	if !tok.ExpiresAt.IsZero() || !tok.RefreshExpiresAt.IsZero() {
		t.Fatalf("expiry = %v / %v, want zero", tok.ExpiresAt, tok.RefreshExpiresAt)
	}
	if tok.RefreshToken != "" {
		t.Fatalf("refresh token = %q, want empty", tok.RefreshToken)
	}
}

func TestExchangeExpiring(t *testing.T) {
	fake, wc := newWebClient(t, true)
	tok := signIn(t, fake, wc)
	if d := time.Until(tok.ExpiresAt) - 8*time.Hour; d < -time.Minute || d > time.Minute {
		t.Fatalf("expires at %v, want about 8h from now", tok.ExpiresAt)
	}
	if tok.RefreshToken == "" {
		t.Fatal("no refresh token")
	}
	if tok.RefreshExpiresAt.IsZero() {
		t.Fatal("refresh expiry is zero")
	}
}

func TestExchangeWrongVerifier(t *testing.T) {
	fake, wc := newWebClient(t, false)
	_, challenge := newPKCE(t)
	other, _ := newPKCE(t)
	code := authorize(t, fake, wc, "st", challenge)
	if _, err := wc.Exchange(context.Background(), code, other); !errors.Is(err, ghauth.ErrBadCode) {
		t.Fatalf("err = %v, want ErrBadCode", err)
	}
}

func TestExchangeCodeSingleUse(t *testing.T) {
	fake, wc := newWebClient(t, false)
	verifier, challenge := newPKCE(t)
	code := authorize(t, fake, wc, "st", challenge)
	if _, err := wc.Exchange(context.Background(), code, verifier); err != nil {
		t.Fatal(err)
	}
	if _, err := wc.Exchange(context.Background(), code, verifier); !errors.Is(err, ghauth.ErrBadCode) {
		t.Fatalf("second exchange err = %v, want ErrBadCode", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	fake, wc := newWebClient(t, true)
	first := signIn(t, fake, wc)
	next, err := wc.Refresh(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken == "" || next.AccessToken == first.AccessToken {
		t.Fatalf("access token not rotated: %q", next.AccessToken)
	}
	if next.RefreshToken == "" || next.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh token not rotated: %q", next.RefreshToken)
	}
	if _, err := wc.Refresh(context.Background(), first.RefreshToken); !errors.Is(err, ghauth.ErrBadRefresh) {
		t.Fatalf("reused refresh err = %v, want ErrBadRefresh", err)
	}
}

func TestRevoke(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expiring bool
	}{
		{"oauth app", false},
		{"github app", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, wc := newWebClient(t, tc.expiring)
			tok := signIn(t, fake, wc)
			if err := wc.Revoke(context.Background(), fake.URL, tok.AccessToken); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(fake.Revoked(), tok.AccessToken) {
				t.Fatalf("revoked = %v, want it to contain the token", fake.Revoked())
			}
			if err := wc.Revoke(context.Background(), fake.URL, tok.AccessToken); err != nil {
				t.Fatalf("second revoke: %v", err)
			}
			if err := wc.Revoke(context.Background(), fake.URL, "gho_never_issued"); err != nil {
				t.Fatalf("revoke of unknown token: %v", err)
			}
		})
	}
}

func TestErrorsDoNotLeakSecrets(t *testing.T) {
	ctx := context.Background()
	fake, wc := newWebClient(t, true)
	verifier, challenge := newPKCE(t)
	other, _ := newPKCE(t)

	var errs []error
	code := authorize(t, fake, wc, "st", challenge)
	_, err := wc.Exchange(ctx, code, other)
	errs = append(errs, err)
	_, err = wc.Exchange(ctx, code, verifier)
	errs = append(errs, err)
	_, err = wc.Refresh(ctx, "ghr_not_a_real_one")
	errs = append(errs, err)

	wrong := ghauth.NewWebClient(githubfake.ClientID, "wrong-secret", fake.URL, callback)
	code2 := authorize(t, fake, wrong, "st", challenge)
	_, err = wrong.Exchange(ctx, code2, verifier)
	errs = append(errs, err)

	tok := signIn(t, fake, wc)
	fake.FailRevoke()
	errs = append(errs, wc.Revoke(ctx, fake.URL, tok.AccessToken))

	secrets := []string{githubfake.ClientSecret, "wrong-secret", code, code2, verifier, other, tok.AccessToken, tok.RefreshToken}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected every call to fail")
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Errorf("error %q contains a secret", err)
			}
		}
	}
}
