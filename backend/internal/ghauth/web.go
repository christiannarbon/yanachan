package ghauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebClient drives the OAuth web application flow used by server mode. It
// works unchanged for GitHub Apps and OAuth Apps: GitHub Apps ignore the scope
// and add a refresh token; OAuth Apps return a token that never expires.
type WebClient struct {
	HTTP         *http.Client
	ClientID     string
	ClientSecret string
	WebBase      string // https://github.com, or the GHES base
	RedirectURL  string // <public url>/api/auth/callback
}

func NewWebClient(clientID, clientSecret, webBase, redirectURL string) *WebClient {
	return &WebClient{
		HTTP:         &http.Client{Timeout: 20 * time.Second},
		ClientID:     clientID,
		ClientSecret: clientSecret,
		WebBase:      strings.TrimRight(webBase, "/"),
		RedirectURL:  redirectURL,
	}
}

// Token is a user access token. Zero times mean "does not expire" (an OAuth
// App). When the token was issued is the caller's business: session.Record
// keeps TokenIssuedAt, which drives the rotation policy.
type Token struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

var (
	ErrBadCode    = errors.New("bad_verification_code")
	ErrBadRefresh = errors.New("bad_refresh_token")
)

func NewPKCE() (verifier, challenge string, err error) {
	verifier, err = randomString()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func NewState() (string, error) {
	return randomString()
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *WebClient) AuthorizeURL(state, challenge string) string {
	v := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {c.RedirectURL},
		"scope":                 {Scopes},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	return c.WebBase + "/login/oauth/authorize?" + v.Encode()
}

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

func (r tokenResponse) token(now time.Time) Token {
	t := Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		t.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshTokenExpiresIn > 0 {
		t.RefreshExpiresAt = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return t
}

func (c *WebClient) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"code_verifier": {verifier},
	}
	return c.requestToken(ctx, form, "bad_verification_code", ErrBadCode)
}

func (c *WebClient) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	return c.requestToken(ctx, form, "bad_refresh_token", ErrBadRefresh)
}

func (c *WebClient) requestToken(ctx context.Context, form url.Values, badCode string, badErr error) (Token, error) {
	var out tokenResponse
	if err := postForm(ctx, c.HTTP, c.WebBase+"/login/oauth/access_token", form, &out); err != nil {
		return Token{}, err
	}
	switch out.Error {
	case "":
	case badCode:
		return Token{}, badErr
	default:
		if out.ErrorDescription != "" {
			return Token{}, errors.New(out.ErrorDescription)
		}
		return Token{}, errors.New(out.Error)
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("github returned an empty access token")
	}
	return out.token(time.Now()), nil
}

// Revoke deletes accessToken at GitHub. A token that is already gone counts
// as revoked.
func (c *WebClient) Revoke(ctx context.Context, restEndpoint, accessToken string) error {
	body, err := json.Marshal(map[string]string{"access_token": accessToken})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(restEndpoint, "/") + "/applications/" + url.PathEscape(c.ClientID) + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("revoke token: github returned %s", resp.Status)
}
