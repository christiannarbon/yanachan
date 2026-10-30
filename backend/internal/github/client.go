// Package github is a small GraphQL client for the queries this dashboard needs.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	http     *http.Client
	endpoint string
	token    string
	// First backoff between attempts; each retry doubles it. A field rather
	// than a constant so the tests do not sleep.
	retryWait time.Duration
}

func New(endpoint, token string) *Client {
	return &Client{
		http:      &http.Client{Timeout: 45 * time.Second},
		endpoint:  endpoint,
		token:     token,
		retryWait: 400 * time.Millisecond,
	}
}

// How many times a request is sent before its failure is the caller's problem.
const attempts = 3

type gqlError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Do executes a GraphQL document and decodes the "data" object into out.
// GraphQL errors are returned even when data is partially populated, so
// callers that tolerate partial results should inspect the returned data first.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	body, err := c.send(ctx, payload)
	if err != nil {
		return err
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}
	if len(envelope.Data) > 0 && out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode graphql data: %w", err)
		}
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("github graphql: %s", strings.Join(msgs, "; "))
	}
	return nil
}

var ErrUnauthorized = errors.New("github rejected the token (401)")

// send posts the payload and returns the response body, retrying the failures
// that are the upstream's mood rather than a problem with the request.
//
// GitHub's edge answers a 502 or a 503 now and again, usually for one request
// out of a burst, and the old behaviour was to hand that straight to the
// dashboard -- as a page of nginx HTML, on the tab you land on. Nearly all of
// them clear on the next attempt.
//
// Only transport failures and 502/503/504 are retried. A 429 is not: a rate
// limit wants the window to pass, not another request half a second later.
func (c *Client) send(ctx context.Context, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			wait := c.retryWait << (attempt - 1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		body, status, err := c.once(ctx, payload)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, err
		case err != nil:
			lastErr = err
		case status == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case status == http.StatusOK:
			return body, nil
		case retryable(status):
			lastErr = upstreamError(status, body)
		default:
			return nil, upstreamError(status, body)
		}
	}
	return nil, lastErr
}

// once is a single attempt. The status comes back alongside the body so send
// can decide whether it is worth another go.
func (c *Client) once(ctx context.Context, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "yana-chan-4k")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func retryable(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// upstreamError words a non-200 for somebody reading the dashboard.
//
// The body is quoted only when GitHub sent JSON, which is when it has
// something to say. A 502 from the edge is a page of HTML, and pasting that
// into the notice is how this looked like a broken app rather than a blip.
func upstreamError(status int, body []byte) error {
	if msg := jsonMessage(body); msg != "" {
		return fmt.Errorf("github graphql: %s: %s", http.StatusText(status), truncate(msg, 300))
	}
	if retryable(status) {
		return fmt.Errorf("github is having trouble (%d %s); it usually clears in a moment",
			status, http.StatusText(status))
	}
	return fmt.Errorf("github graphql: %d %s", status, http.StatusText(status))
}

// jsonMessage pulls GitHub's own wording out of an error body, or returns ""
// when the body is not JSON.
func jsonMessage(body []byte) string {
	var envelope struct {
		Message string     `json:"message"`
		Errors  []gqlError `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if envelope.Message != "" {
		return envelope.Message
	}
	msgs := make([]string, 0, len(envelope.Errors))
	for _, e := range envelope.Errors {
		if e.Message != "" {
			msgs = append(msgs, e.Message)
		}
	}
	return strings.Join(msgs, "; ")
}

// Viewer returns the login of the authenticated user.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := c.Do(ctx, `query{viewer{login}}`, nil, &data); err != nil {
		return "", err
	}
	if data.Viewer.Login == "" {
		return "", errors.New("github returned an empty viewer login")
	}
	return data.Viewer.Login, nil
}

// ViewerIdentity returns the login and the numeric user ID (databaseId) of the
// authenticated user. Server mode keys everything by the ID, which unlike the
// login can never be renamed or reused.
func (c *Client) ViewerIdentity(ctx context.Context) (login string, id int64, err error) {
	var data struct {
		Viewer struct {
			Login      string `json:"login"`
			DatabaseID int64  `json:"databaseId"`
		} `json:"viewer"`
	}
	if err := c.Do(ctx, `query{viewer{login databaseId}}`, nil, &data); err != nil {
		return "", 0, err
	}
	if data.Viewer.Login == "" || data.Viewer.DatabaseID <= 0 {
		return "", 0, errors.New("github returned an incomplete viewer identity")
	}
	return data.Viewer.Login, data.Viewer.DatabaseID, nil
}

// OrgMembership reports, for each org login, whether the viewer is a member.
// An org GitHub cannot resolve for this token counts as "not a member": it
// may not exist, or the org may be hiding itself from this app.
func (c *Client) OrgMembership(ctx context.Context, orgs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(orgs))
	if len(orgs) == 0 {
		return out, nil
	}
	params := make([]string, len(orgs))
	fields := make([]string, len(orgs))
	vars := make(map[string]any, len(orgs))
	for i, org := range orgs {
		alias := "o" + strconv.Itoa(i)
		params[i] = "$" + alias + ": String!"
		fields[i] = alias + ": organization(login: $" + alias + ") { viewerIsAMember }"
		vars[alias] = org
	}
	q := "query(" + strings.Join(params, ", ") + ") {\n  " + strings.Join(fields, "\n  ") + "\n}"

	var data map[string]*struct {
		ViewerIsAMember bool `json:"viewerIsAMember"`
	}
	err := c.Do(ctx, q, vars, &data)
	if err != nil && len(data) == 0 {
		return nil, err
	}
	for i, org := range orgs {
		m, ok := data["o"+strconv.Itoa(i)]
		if !ok {
			if err == nil {
				err = errors.New("github left an org out of the membership answer")
			}
			return nil, err
		}
		out[org] = m != nil && m.ViewerIsAMember
	}
	return out, nil
}

type Membership struct {
	Orgs  []string `json:"orgs"`
	Teams []string `json:"teams"`
}

// Memberships lists the orgs and teams the viewer belongs to, used to offer
// suggestions in the settings dialog. Requires the read:org scope.
func (c *Client) Memberships(ctx context.Context) (Membership, error) {
	const q = `query {
  viewer {
    organizations(first: 100) {
      nodes {
        login
        teams(first: 100, role: MEMBER) { nodes { combinedSlug } }
      }
    }
  }
}`
	var data struct {
		Viewer struct {
			Organizations struct {
				Nodes []struct {
					Login string `json:"login"`
					Teams struct {
						Nodes []struct {
							CombinedSlug string `json:"combinedSlug"`
						} `json:"nodes"`
					} `json:"teams"`
				} `json:"nodes"`
			} `json:"organizations"`
		} `json:"viewer"`
	}
	err := c.Do(ctx, q, nil, &data)
	m := Membership{Orgs: []string{}, Teams: []string{}}
	for _, org := range data.Viewer.Organizations.Nodes {
		if org.Login != "" {
			m.Orgs = append(m.Orgs, org.Login)
		}
		for _, t := range org.Teams.Nodes {
			if t.CombinedSlug != "" {
				m.Teams = append(m.Teams, t.CombinedSlug)
			}
		}
	}
	return m, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
