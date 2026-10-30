package access_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/christiannarbon/yanachan-4k/backend/internal/access"
	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/ghauth"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
)

type stubMember struct {
	member map[string]bool
	err    error
	calls  int
}

func (s *stubMember) OrgMembership(ctx context.Context, orgs []string) (map[string]bool, error) {
	s.calls++
	return s.member, s.err
}

// clientFor signs login in to fake and returns a GraphQL client with their token.
func clientFor(t *testing.T, login string) *github.Client {
	t.Helper()
	fake := githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "alice", ID: 1, Orgs: []string{"acme"}})
	fake.AddUser(githubfake.User{Login: "carol", ID: 3, Orgs: []string{"other"}})
	fake.SignInAs(login)

	wc := ghauth.NewWebClient(githubfake.ClientID, githubfake.ClientSecret, fake.URL, "http://app.test/api/auth/callback")
	verifier, challenge, err := ghauth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(wc.AuthorizeURL("st", challenge))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := wc.Exchange(context.Background(), loc.Query().Get("code"), verifier)
	if err != nil {
		t.Fatal(err)
	}
	return github.New(fake.URL+"/graphql", tok.AccessToken)
}

func TestPolicyAllowAny(t *testing.T) {
	m := &stubMember{}
	if err := (access.Policy{AllowAny: true}).Check(context.Background(), m, "anyone"); err != nil {
		t.Fatal(err)
	}
	if m.calls != 0 {
		t.Errorf("asked github %d times, want 0", m.calls)
	}
}

func TestPolicyUserListCaseInsensitive(t *testing.T) {
	m := &stubMember{}
	if err := (access.Policy{Users: []string{"Alice"}}).Check(context.Background(), m, "alice"); err != nil {
		t.Fatal(err)
	}
	if m.calls != 0 {
		t.Errorf("asked github %d times, want 0", m.calls)
	}
}

func TestPolicyOrgMember(t *testing.T) {
	err := (access.Policy{Orgs: []string{"acme"}}).Check(context.Background(), clientFor(t, "alice"), "alice")
	if err != nil {
		t.Fatal(err)
	}
}

func TestPolicyOrgNonMember(t *testing.T) {
	err := (access.Policy{Orgs: []string{"acme"}}).Check(context.Background(), clientFor(t, "carol"), "carol")
	if !errors.Is(err, access.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
}

func TestPolicyUnknownOrg(t *testing.T) {
	err := (access.Policy{Orgs: []string{"ghost"}}).Check(context.Background(), clientFor(t, "alice"), "alice")
	if !errors.Is(err, access.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
}

func TestPolicyEmptyDenies(t *testing.T) {
	m := &stubMember{member: map[string]bool{}}
	if err := (access.Policy{}).Check(context.Background(), m, "alice"); !errors.Is(err, access.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
}

func TestPolicyTransientError(t *testing.T) {
	m := &stubMember{err: errors.New("github returned 502 Bad Gateway")}
	err := (access.Policy{Orgs: []string{"acme"}}).Check(context.Background(), m, "alice")
	if err == nil || errors.Is(err, access.ErrNotAllowed) {
		t.Fatalf("err = %v, want a non-refusal error", err)
	}
}

func TestPolicyFromConfig(t *testing.T) {
	got := access.PolicyFromConfig(config.Config{
		AllowedOrgs:  []string{"acme"},
		AllowedUsers: []string{"alice"},
		AllowAnyUser: true,
	})
	want := access.Policy{Orgs: []string{"acme"}, Users: []string{"alice"}, AllowAny: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
