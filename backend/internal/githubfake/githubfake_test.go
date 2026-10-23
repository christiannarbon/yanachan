package githubfake_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
)

const (
	verifier = "a-verifier-that-is-long-enough-for-pkce-0123456789"
	callback = "http://app.test/api/auth/callback"
)

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func authorize(t *testing.T, fake *githubfake.Server, method string) *http.Response {
	t.Helper()
	q := url.Values{
		"client_id":             {githubfake.ClientID},
		"redirect_uri":          {callback},
		"state":                 {"st-123"},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {method},
		"scope":                 {"repo read:org"},
		"allow_signup":          {"false"},
	}
	resp, err := noRedirect.Get(fake.URL + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func codeFrom(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != "st-123" {
		t.Fatalf("state = %q, want st-123", got)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}
	return code
}

func postToken(t *testing.T, fake *githubfake.Server, form url.Values) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, fake.URL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func exchange(t *testing.T, fake *githubfake.Server, code, v string) map[string]any {
	t.Helper()
	status, body := postToken(t, fake, url.Values{
		"client_id":     {githubfake.ClientID},
		"client_secret": {githubfake.ClientSecret},
		"code":          {code},
		"code_verifier": {v},
		"redirect_uri":  {callback},
	})
	if status != http.StatusOK {
		t.Fatalf("token endpoint = %d, want 200", status)
	}
	return body
}

func signIn(t *testing.T, fake *githubfake.Server, u githubfake.User) string {
	t.Helper()
	fake.AddUser(u)
	fake.SignInAs(u.Login)
	body := exchange(t, fake, codeFrom(t, authorize(t, fake, "S256")), verifier)
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("no access token: %v", body)
	}
	return token
}

func graphql(t *testing.T, fake *githubfake.Server, token, query string, vars map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequest(http.MethodPost, fake.URL+"/graphql", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func revoke(t *testing.T, fake *githubfake.Server, token string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequest(http.MethodDelete, fake.URL+"/applications/"+githubfake.ClientID+"/token", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(githubfake.ClientID, githubfake.ClientSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuthorizeRedirectsWithCodeAndState(t *testing.T) {
	fake := githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "alice", ID: 1})
	fake.SignInAs("alice")
	resp := authorize(t, fake, "S256")
	if !strings.HasPrefix(resp.Header.Get("Location"), callback+"?") {
		t.Fatalf("Location = %q, want the callback", resp.Header.Get("Location"))
	}
	if code := codeFrom(t, resp); !strings.HasPrefix(code, "code_fake_") {
		t.Fatalf("code = %q", code)
	}
}

func TestRepeatAuthorizeRedirectsImmediately(t *testing.T) {
	fake := githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "alice", ID: 1})
	fake.SignInAs("alice")
	first := codeFrom(t, authorize(t, fake, "S256"))
	second := codeFrom(t, authorize(t, fake, "S256"))
	if first == second {
		t.Fatal("two authorizes gave the same code")
	}
	if got := fake.AuthorizeCount(); got != 2 {
		t.Fatalf("AuthorizeCount() = %d, want 2", got)
	}
}

func TestAuthorizeRequiresS256(t *testing.T) {
	fake := githubfake.New(t)
	fake.SignInAs("alice")
	for _, method := range []string{"plain", ""} {
		if resp := authorize(t, fake, method); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("method %q = %d, want 400", method, resp.StatusCode)
		}
	}
}

func TestAuthorizeUnknownClientIs404(t *testing.T) {
	fake := githubfake.New(t)
	resp, err := noRedirect.Get(fake.URL + "/login/oauth/authorize?client_id=nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAuthorizeWithoutSignInIs500(t *testing.T) {
	fake := githubfake.New(t)
	if resp := authorize(t, fake, "S256"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestDenyRedirectsWithAccessDenied(t *testing.T) {
	fake := githubfake.New(t)
	fake.SignInAs("alice")
	fake.Deny()
	resp := authorize(t, fake, "S256")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "st-123" {
		t.Fatalf("Location = %s", loc)
	}
	if loc.Query().Get("code") != "" {
		t.Fatal("a denied authorize carried a code")
	}
	codeFrom(t, authorize(t, fake, "S256"))
}

func TestExchangeChecksVerifierAndIsSingleUse(t *testing.T) {
	fake := githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "alice", ID: 1})
	fake.SignInAs("alice")

	code := codeFrom(t, authorize(t, fake, "S256"))
	body := exchange(t, fake, code, verifier)
	token, _ := body["access_token"].(string)
	if !strings.HasPrefix(token, "gho_fake_") || body["token_type"] != "bearer" {
		t.Fatalf("body = %v", body)
	}
	if _, ok := body["refresh_token"]; ok {
		t.Fatal("refresh_token without ExpiringTokens")
	}
	if got := fake.AccessTokenFor("alice"); got != token {
		t.Fatalf("AccessTokenFor = %q, want %q", got, token)
	}
	if body := exchange(t, fake, code, verifier); body["error"] != "bad_verification_code" {
		t.Fatalf("reused code: %v", body)
	}
	fresh := codeFrom(t, authorize(t, fake, "S256"))
	if body := exchange(t, fake, fresh, "the-wrong-verifier"); body["error"] != "bad_verification_code" {
		t.Fatalf("wrong verifier: %v", body)
	}
	if body := exchange(t, fake, fresh, verifier); body["error"] != "bad_verification_code" {
		t.Fatalf("code reused after a failed exchange: %v", body)
	}
}

func TestExchangeRedirectURIMismatch(t *testing.T) {
	fake := githubfake.New(t)
	fake.SignInAs("alice")
	code := codeFrom(t, authorize(t, fake, "S256"))
	_, body := postToken(t, fake, url.Values{
		"client_id":     {githubfake.ClientID},
		"client_secret": {githubfake.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {"http://elsewhere.test/cb"},
	})
	if body["error"] != "redirect_uri_mismatch" {
		t.Fatalf("body = %v", body)
	}
}

func TestExchangeWrongClientSecret(t *testing.T) {
	fake := githubfake.New(t)
	status, body := postToken(t, fake, url.Values{
		"client_id":     {githubfake.ClientID},
		"client_secret": {"wrong"},
		"code":          {"anything"},
	})
	if status != http.StatusOK || body["error"] != "incorrect_client_credentials" {
		t.Fatalf("status = %d, body = %v", status, body)
	}
}

func TestExpiringTokensAndRefreshRotation(t *testing.T) {
	fake := githubfake.New(t)
	fake.ExpiringTokens = true
	fake.AddUser(githubfake.User{Login: "alice", ID: 1})
	fake.SignInAs("alice")

	body := exchange(t, fake, codeFrom(t, authorize(t, fake, "S256")), verifier)
	oldToken, _ := body["access_token"].(string)
	rt, _ := body["refresh_token"].(string)
	if !strings.HasPrefix(rt, "ghr_fake_") || body["expires_in"] != float64(28800) || body["refresh_token_expires_in"] != float64(15897600) {
		t.Fatalf("body = %v", body)
	}

	refresh := func(token string) map[string]any {
		_, body := postToken(t, fake, url.Values{
			"client_id":     {githubfake.ClientID},
			"client_secret": {githubfake.ClientSecret},
			"grant_type":    {"refresh_token"},
			"refresh_token": {token},
		})
		return body
	}
	next := refresh(rt)
	newToken, _ := next["access_token"].(string)
	if newToken == "" || next["refresh_token"] == rt {
		t.Fatalf("refresh = %v", next)
	}
	if code, _ := graphql(t, fake, newToken, "query{viewer{login}}", nil); code != http.StatusOK {
		t.Fatalf("refreshed token = %d on /graphql", code)
	}
	if code, _ := graphql(t, fake, oldToken, "query{viewer{login}}", nil); code != http.StatusUnauthorized {
		t.Fatalf("old access token after a refresh = %d, want 401", code)
	}
	if again := refresh(rt); again["error"] != "bad_refresh_token" {
		t.Fatalf("reused refresh token: %v", again)
	}
	if unknown := refresh("ghr_fake_nope"); unknown["error"] != "bad_refresh_token" {
		t.Fatalf("unknown refresh token: %v", unknown)
	}
}

func TestGraphQLViewerIdentity(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 42, Orgs: []string{"acme"}})

	_, body := graphql(t, fake, token, "query{viewer{login databaseId}}", nil)
	viewer := body["data"].(map[string]any)["viewer"].(map[string]any)
	if viewer["login"] != "alice" || viewer["databaseId"] != float64(42) {
		t.Fatalf("viewer = %v", viewer)
	}

	_, body = graphql(t, fake, token, "query{viewer{login}}", nil)
	viewer = body["data"].(map[string]any)["viewer"].(map[string]any)
	if viewer["login"] != "alice" {
		t.Fatalf("viewer = %v", viewer)
	}

	_, body = graphql(t, fake, token, "query{ viewer { organizations(first: 100) { nodes { login teams(first: 100, role: MEMBER) { nodes { combinedSlug } } } } } }", nil)
	nodes := body["data"].(map[string]any)["viewer"].(map[string]any)["organizations"].(map[string]any)["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["login"] != "acme" {
		t.Fatalf("organizations = %v", nodes)
	}

	if code, body := graphql(t, fake, token, "query{repository{name}}", nil); code != http.StatusOK || body["errors"] == nil {
		t.Fatalf("unsupported query = %d %v", code, body)
	}
}

func TestGraphQLUnknownTokenIs401(t *testing.T) {
	fake := githubfake.New(t)
	code, body := graphql(t, fake, "gho_fake_nope", "query{viewer{login}}", nil)
	if code != http.StatusUnauthorized || body["message"] != "Bad credentials" {
		t.Fatalf("status = %d, body = %v", code, body)
	}
}

func TestExpiredAccessTokenIs401(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1})
	fake.ExpireAccessToken(token)
	if code, _ := graphql(t, fake, token, "query{viewer{login}}", nil); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestGraphQLOrgMembership(t *testing.T) {
	fake := githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "bob", ID: 2, Orgs: []string{"globex"}})
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1, Orgs: []string{"acme"}})

	query := `query($o0: String!, $o1: String!, $o2: String!) {
  o0: organization(login: $o0) { viewerIsAMember }
  o1: organization(login: $o1) { viewerIsAMember }
  o2: organization(login: $o2) { viewerIsAMember }
}`
	_, body := graphql(t, fake, token, query, map[string]any{"o0": "acme", "o1": "globex", "o2": "nowhere"})
	data := body["data"].(map[string]any)
	if data["o0"].(map[string]any)["viewerIsAMember"] != true {
		t.Errorf("acme = %v, want member", data["o0"])
	}
	if data["o1"].(map[string]any)["viewerIsAMember"] != false {
		t.Errorf("globex = %v, want not a member", data["o1"])
	}
	if v, ok := data["o2"]; !ok || v != nil {
		t.Errorf("nowhere = %v, want null", v)
	}
	errs, _ := body["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["type"] != "NOT_FOUND" {
		t.Fatalf("errors = %v", body["errors"])
	}
}

func TestAddUserReplacesByLogin(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1, Orgs: []string{"acme", "globex"}})
	fake.AddUser(githubfake.User{Login: "alice", ID: 1, Orgs: []string{"globex"}})

	_, body := graphql(t, fake, token, `query($o0: String!){ o0: organization(login: $o0) { viewerIsAMember } }`, map[string]any{"o0": "acme"})
	if v := body["data"].(map[string]any)["o0"]; v != nil {
		t.Fatalf("acme = %v, want null once nobody lists it", v)
	}
}

func TestRevokeMakesTokenUnusable(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1})
	if code := revoke(t, fake, token); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
	if code, _ := graphql(t, fake, token, "query{viewer{login}}", nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d on /graphql, want 401", code)
	}
	if !slices.Contains(fake.Revoked(), token) {
		t.Fatalf("Revoked() = %v", fake.Revoked())
	}
	if code := revoke(t, fake, "gho_fake_nope"); code != http.StatusNotFound {
		t.Fatalf("revoke of an unknown token = %d, want 404", code)
	}
}

func TestRevokeRefusesWrongClient(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1})
	req, _ := http.NewRequest(http.MethodDelete, fake.URL+"/applications/"+githubfake.ClientID+"/token",
		strings.NewReader(`{"access_token":"`+token+`"}`))
	req.SetBasicAuth(githubfake.ClientID, "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(fake.Revoked()) != 0 {
		t.Fatal("a refused revoke still revoked")
	}
}

func TestTokensSeenRecordsEveryBearer(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1})
	graphql(t, fake, token, "query{viewer{login}}", nil)
	graphql(t, fake, "gho_fake_nope", "query{viewer{login}}", nil)
	graphql(t, fake, token, "query{viewer{login}}", nil)
	want := []string{token, "gho_fake_nope", token}
	if got := fake.TokensSeen(); !slices.Equal(got, want) {
		t.Fatalf("TokensSeen() = %v, want %v", got, want)
	}
}

func TestFailRevoke(t *testing.T) {
	fake := githubfake.New(t)
	token := signIn(t, fake, githubfake.User{Login: "alice", ID: 1})
	fake.FailRevoke()
	if code := revoke(t, fake, token); code != http.StatusInternalServerError {
		t.Fatalf("revoke = %d, want 500", code)
	}
	if code, _ := graphql(t, fake, token, "query{viewer{login}}", nil); code != http.StatusOK {
		t.Fatalf("token after a failed revoke = %d, want 200", code)
	}
	fake.ResumeRevoke()
	if code := revoke(t, fake, token); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	fake := githubfake.New(t)
	resp, err := http.Get(fake.URL + "/user")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
