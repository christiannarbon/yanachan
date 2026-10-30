// Package ghauth implements the GitHub OAuth device and web flows.
//
// The device flow needs only a client ID: no client secret, no callback URL,
// which is what makes it work identically on a laptop, in Docker and behind a
// kubectl port-forward.
package ghauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scopes requested from GitHub. repo covers private pull requests, read:org is
// needed to resolve team review requests and to list the user's teams.
const Scopes = "repo read:org"

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type Client struct {
	HTTP     *http.Client
	ClientID string
	WebBase  string
}

func NewClient(clientID, webBase string) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: 20 * time.Second},
		ClientID: clientID,
		WebBase:  strings.TrimRight(webBase, "/"),
	}
}

var (
	ErrPending      = errors.New("authorization_pending")
	ErrSlowDown     = errors.New("slow_down")
	ErrExpiredToken = errors.New("expired_token")
	ErrAccessDenied = errors.New("access_denied")
)

func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceCode, error) {
	if c.ClientID == "" {
		return nil, errors.New("GITHUB_CLIENT_ID is not set; OAuth sign-in is unavailable")
	}
	form := url.Values{"client_id": {c.ClientID}, "scope": {Scopes}}
	var out DeviceCode
	if err := postForm(ctx, c.HTTP, c.WebBase+"/login/device/code", form, &out); err != nil {
		return nil, err
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

// PollDeviceFlow performs a single poll. Callers should retry on ErrPending
// and back off on ErrSlowDown.
func (c *Client) PollDeviceFlow(ctx context.Context, deviceCode string) (string, error) {
	form := url.Values{
		"client_id":   {c.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := postForm(ctx, c.HTTP, c.WebBase+"/login/oauth/access_token", form, &out); err != nil {
		return "", err
	}
	switch out.Error {
	case "":
	case "authorization_pending":
		return "", ErrPending
	case "slow_down":
		return "", ErrSlowDown
	case "expired_token":
		return "", ErrExpiredToken
	case "access_denied":
		return "", ErrAccessDenied
	default:
		if out.ErrorDescription != "" {
			return "", errors.New(out.ErrorDescription)
		}
		return "", errors.New(out.Error)
	}
	if out.AccessToken == "" {
		return "", errors.New("github returned an empty access token")
	}
	return out.AccessToken, nil
}

func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("github returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
