package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The page GitHub's edge serves when it hiccups. What used to end up on the
// dashboard, verbatim.
const nginx502 = `<html>
<head><title>502 Bad Gateway</title></head>
<body><center><h1>502 Bad Gateway</h1></center><hr><center>nginx</center></body>
</html>`

// flaky answers with statuses in order, then 200 for every request after that.
func flaky(t *testing.T, statuses ...int) (*Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := calls
		calls++
		if i < len(statuses) && statuses[i] != http.StatusOK {
			w.WriteHeader(statuses[i])
			_, _ = w.Write([]byte(nginx502))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"ada"}}}`))
	}))
	t.Cleanup(srv.Close)
	cl := New(srv.URL, "token")
	cl.retryWait = 0
	return cl, &calls
}

// The bug this covers: one 502 out of GitHub's edge blanked the tab you land
// on, when the next attempt would have worked.
func TestDoRidesOutATransient502(t *testing.T) {
	cl, calls := flaky(t, http.StatusBadGateway)

	login, err := cl.Viewer(context.Background())
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if login != "ada" {
		t.Errorf("login = %q, want ada", login)
	}
	if *calls != 2 {
		t.Errorf("sent %d requests, want 2", *calls)
	}
}

func TestDoRetriesTheOtherGatewayStatuses(t *testing.T) {
	cl, calls := flaky(t, http.StatusServiceUnavailable, http.StatusGatewayTimeout)

	if _, err := cl.Viewer(context.Background()); err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if *calls != 3 {
		t.Errorf("sent %d requests, want 3", *calls)
	}
}

func TestDoGivesUpAfterThreeAttempts(t *testing.T) {
	cl, calls := flaky(t, http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway)

	_, err := cl.Viewer(context.Background())
	if err == nil {
		t.Fatal("want an error once the retries are spent")
	}
	if *calls != 3 {
		t.Errorf("sent %d requests, want 3", *calls)
	}
	if strings.Contains(err.Error(), "nginx") || strings.Contains(err.Error(), "<html>") {
		t.Errorf("the edge's HTML reached the message: %q", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("message should still say which status: %q", err)
	}
}

// A 4xx is the request's own fault, so another one is a waste of everybody's
// time -- and GitHub's JSON wording is worth keeping.
func TestDoDoesNotRetryARejection(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	t.Cleanup(srv.Close)
	cl := New(srv.URL, "token")
	cl.retryWait = 0

	_, err := cl.Viewer(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Errorf("sent %d requests, want 1", calls)
	}
	if !strings.Contains(err.Error(), "Resource not accessible") {
		t.Errorf("GitHub's own wording was dropped: %q", err)
	}
}

func TestDoReportsAnUnauthorizedTokenOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	cl := New(srv.URL, "token")
	cl.retryWait = 0

	_, err := cl.Viewer(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if calls != 1 {
		t.Errorf("sent %d requests, want 1", calls)
	}
}

func TestViewerIdentity(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		query = body.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat","databaseId":583231}}}`))
	}))
	t.Cleanup(srv.Close)

	login, id, err := New(srv.URL, "token").ViewerIdentity(context.Background())
	if err != nil {
		t.Fatalf("ViewerIdentity: %v", err)
	}
	if login != "octocat" || id != 583231 {
		t.Errorf("got %q, %d, want octocat, 583231", login, id)
	}
	if !strings.Contains(query, "databaseId") {
		t.Errorf("query %q does not ask for databaseId", query)
	}
}

func TestOrgMembership(t *testing.T) {
	var vars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		vars = body.Variables
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"o0":{"viewerIsAMember":true},"o1":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to an Organization"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := New(srv.URL, "token").OrgMembership(context.Background(), []string{"acme", "ghost"})
	if err != nil {
		t.Fatalf("OrgMembership: %v", err)
	}
	if len(got) != 2 || !got["acme"] || got["ghost"] {
		t.Errorf("got %v, want acme: true, ghost: false", got)
	}
	if vars["o0"] != "acme" || vars["o1"] != "ghost" {
		t.Errorf("variables = %v", vars)
	}
}

func TestOrgMembershipTransportError(t *testing.T) {
	cl, calls := flaky(t, http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway)

	if _, err := cl.OrgMembership(context.Background(), []string{"acme"}); err == nil {
		t.Fatal("OrgMembership succeeded after three 502s")
	}
	if *calls != 3 {
		t.Errorf("sent %d requests, want 3", *calls)
	}
}
