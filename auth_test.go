package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const claudeCallback = "https://claude.ai/api/mcp/auth_callback"

// fakeGoogle answers the token exchange with an ID token for the next email.
type fakeGoogle struct {
	email string
	nonce string
}

func newAuthServer(t *testing.T) (*httptest.Server, *fakeGoogle) {
	t.Helper()
	g := &fakeGoogle{email: "Mama@Example.com"}
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostFormValue("code") != "google-code" || r.PostFormValue("client_secret") != "google-secret" {
			http.Error(w, "bad code", 400)
			return
		}
		claims, _ := json.Marshal(map[string]any{
			"iss": "https://accounts.google.com", "aud": "google-id", "exp": time.Now().Add(time.Hour).Unix(),
			"email": g.email, "email_verified": true, "nonce": g.nonce,
		})
		writeJSON(w, 200, map[string]string{"id_token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
	}))
	t.Cleanup(google.Close)

	auth, err := NewAuth(AuthConfig{
		GoogleClientID: "google-id", GoogleClientSecret: "google-secret",
		AllowedEmails: []string{"mama@example.com"}, RedirectHosts: []string{"claude.ai", "localhost"},
		PublicURL: "http://placeholder", Secret: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	auth.googleToken = google.URL
	auth.googleAuth = "https://google.test/auth"
	srv, _ := newTestServerWith(t, API{token: "write", auth: auth}, SecurityConfig{BlockBots: true})
	auth.base = srv.URL
	return srv, g
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func send(t *testing.T, method, target, body string, header ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func readBody(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func cookieOf(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

// signIn follows a redirect to Google back through the callback.
func signIn(t *testing.T, srv *httptest.Server, g *fakeGoogle, toGoogle *http.Response) *http.Response {
	t.Helper()
	loc, _ := url.Parse(toGoogle.Header.Get("Location"))
	if toGoogle.StatusCode != http.StatusSeeOther || loc.Host != "google.test" || loc.Query().Get("redirect_uri") != srv.URL+callbackURL {
		t.Fatalf("expected redirect to Google: %d %s", toGoogle.StatusCode, loc)
	}
	g.nonce = loc.Query().Get("nonce")
	nonce := cookieOf(toGoogle, nonceCookie)
	if nonce == nil {
		t.Fatal("nonce cookie missing")
	}
	return send(t, "GET", srv.URL+callbackURL+"?code=google-code&state="+url.QueryEscape(loc.Query().Get("state")), "", "Cookie", nonce.String())
}

func registerClient(t *testing.T, srv *httptest.Server, body string) map[string]any {
	t.Helper()
	res := send(t, "POST", srv.URL+"/oauth/register", body, "Content-Type", "application/json")
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 201 {
		t.Fatalf("register: %d %v", res.StatusCode, out)
	}
	return out
}

func pkce() (verifier, challenge string) {
	verifier = strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func tokenRequest(t *testing.T, srv *httptest.Server, form url.Values) (int, map[string]any) {
	t.Helper()
	res := send(t, "POST", srv.URL+"/oauth/token", form.Encode(), "Content-Type", "application/x-www-form-urlencoded")
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

var consentToken = regexp.MustCompile(`name="consent" value="([^"]+)"`)

func TestOAuthDiscovery(t *testing.T) {
	srv, _ := newAuthServer(t)
	res := send(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	want := `Bearer resource_metadata="` + srv.URL + `/.well-known/oauth-protected-resource/mcp"`
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != want {
		t.Fatalf("unauthenticated MCP: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	var prm, as map[string]any
	json.NewDecoder(send(t, "GET", srv.URL+"/.well-known/oauth-protected-resource/mcp", "").Body).Decode(&prm)
	if prm["resource"] != srv.URL+"/mcp" || prm["authorization_servers"].([]any)[0] != srv.URL {
		t.Fatalf("resource metadata: %v", prm)
	}
	json.NewDecoder(send(t, "GET", srv.URL+"/.well-known/oauth-authorization-server", "").Body).Decode(&as)
	if as["issuer"] != srv.URL || as["token_endpoint"] != srv.URL+"/oauth/token" || as["registration_endpoint"] != srv.URL+"/oauth/register" {
		t.Fatalf("server metadata: %v", as)
	}
	// The static API token keeps working next to OAuth.
	if res := send(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Authorization", "Bearer write"); res.StatusCode != 200 {
		t.Fatalf("API token on MCP: %d", res.StatusCode)
	}
}

func TestOAuthConnectorFlow(t *testing.T) {
	srv, g := newAuthServer(t)
	client := registerClient(t, srv, `{"client_name":"Claude","redirect_uris":["`+claudeCallback+`"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"]}`)
	clientID := client["client_id"].(string)
	if _, hasSecret := client["client_secret"]; hasSecret {
		t.Fatal("public client must not get a secret")
	}
	verifier, challenge := pkce()
	authorize := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {claudeCallback},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"},
	}.Encode()

	callback := signIn(t, srv, g, send(t, "GET", authorize, ""))
	page := readBody(callback)
	m := consentToken.FindStringSubmatch(page)
	if callback.StatusCode != 200 || m == nil || !strings.Contains(page, "Claude") || !strings.Contains(page, "mama@example.com") {
		t.Fatalf("consent page: %d %s", callback.StatusCode, page)
	}
	if cookieOf(callback, loginCookie) == nil {
		t.Fatal("sign-in should also start a web session")
	}

	res := send(t, "POST", srv.URL+"/oauth/consent", "decision=allow&consent="+url.QueryEscape(m[1]), "Content-Type", "application/x-www-form-urlencoded", "Origin", srv.URL)
	back, _ := url.Parse(res.Header.Get("Location"))
	code := back.Query().Get("code")
	if res.StatusCode != 303 || !strings.HasPrefix(back.String(), claudeCallback) || back.Query().Get("state") != "xyz" || code == "" {
		t.Fatalf("consent redirect: %d %s", res.StatusCode, back)
	}

	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {claudeCallback}, "client_id": {clientID}}
	exchange.Set("code_verifier", strings.Repeat("w", 50))
	if status, out := tokenRequest(t, srv, exchange); status != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("wrong PKCE verifier: %d %v", status, out)
	}
	exchange.Set("code_verifier", verifier)
	status, tokens := tokenRequest(t, srv, exchange)
	if status != 200 || tokens["token_type"] != "Bearer" || tokens["refresh_token"] == nil {
		t.Fatalf("token: %d %v", status, tokens)
	}
	if status, _ := tokenRequest(t, srv, exchange); status != 400 {
		t.Fatalf("code reuse must fail, got %d", status)
	}

	bearer := "Bearer " + tokens["access_token"].(string)
	body := readBody(send(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_itinerary","arguments":`+sampleItinerary+`}}`, "Authorization", bearer))
	if !strings.Contains(body, "vysoke-tatry-s-detmi") || strings.Contains(body, `"isError":true`) {
		t.Fatalf("MCP write with OAuth token: %s", body)
	}

	status, refreshed := tokenRequest(t, srv, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)}, "client_id": {clientID}})
	if status != 200 || refreshed["access_token"] == nil {
		t.Fatalf("refresh: %d %v", status, refreshed)
	}
	if res := send(t, "GET", srv.URL+"/api/v1/itineraries", "", "Authorization", "Bearer "+tokens["refresh_token"].(string)); res.StatusCode != 401 {
		t.Fatalf("refresh token must not work as access token: %d", res.StatusCode)
	}
}

func TestOAuthDenyAndSignedInShortcut(t *testing.T) {
	srv, g := newAuthServer(t)
	clientID := registerClient(t, srv, `{"redirect_uris":["`+claudeCallback+`"],"token_endpoint_auth_method":"none"}`)["client_id"].(string)
	_, challenge := pkce()
	authorize := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"s"},
	}.Encode()
	session := cookieOf(signIn(t, srv, g, send(t, "GET", srv.URL+"/auth/login", "")), loginCookie)
	if session == nil {
		t.Fatal("no session after web sign-in")
	}
	// Already signed in: consent right away, no Google round trip.
	m := consentToken.FindStringSubmatch(readBody(send(t, "GET", authorize, "", "Cookie", session.String())))
	if m == nil {
		t.Fatal("signed-in authorize should show consent")
	}
	res := send(t, "POST", srv.URL+"/oauth/consent", "decision=deny&consent="+url.QueryEscape(m[1]), "Content-Type", "application/x-www-form-urlencoded")
	if back, _ := url.Parse(res.Header.Get("Location")); back.Query().Get("error") != "access_denied" || back.Query().Get("code") != "" {
		t.Fatalf("deny: %s", back)
	}
	if res := send(t, "POST", srv.URL+"/oauth/consent", "decision=allow&consent="+url.QueryEscape(m[1]), "Content-Type", "application/x-www-form-urlencoded", "Origin", "https://evil.test"); res.StatusCode != 403 {
		t.Fatalf("cross-origin consent: %d", res.StatusCode)
	}
	if res := send(t, "GET", strings.Replace(authorize, "S256", "plain", 1), "", "Cookie", session.String()); !strings.Contains(res.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("plain PKCE must be refused: %s", res.Header.Get("Location"))
	}
}

func TestOAuthRegistrationAndClientAuth(t *testing.T) {
	srv, _ := newAuthServer(t)
	for _, uri := range []string{"https://evil.test/cb", "http://claude.ai/cb", "https://claude.ai/cb#x"} {
		res := send(t, "POST", srv.URL+"/oauth/register", `{"redirect_uris":["`+uri+`"]}`)
		if res.StatusCode != 400 {
			t.Fatalf("redirect %s: want 400, got %d", uri, res.StatusCode)
		}
	}
	registerClient(t, srv, `{"redirect_uris":["http://localhost:33418/callback"],"token_endpoint_auth_method":"none"}`)

	confidential := registerClient(t, srv, `{"redirect_uris":["`+claudeCallback+`"]}`)
	if confidential["token_endpoint_auth_method"] != "client_secret_basic" || confidential["client_secret"] == nil {
		t.Fatalf("confidential client: %v", confidential)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {confidential["client_id"].(string)}}
	if status, out := tokenRequest(t, srv, form); status != 401 || out["error"] != "invalid_client" {
		t.Fatalf("missing secret: %d %v", status, out)
	}
	form.Set("client_secret", confidential["client_secret"].(string))
	if status, out := tokenRequest(t, srv, form); status != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("valid secret, bad refresh token: %d %v", status, out)
	}
	if res := send(t, "GET", srv.URL+"/oauth/authorize?client_id=forged.sig&response_type=code", ""); res.StatusCode != 400 || res.Header.Get("Location") != "" {
		t.Fatalf("forged client must not redirect: %d", res.StatusCode)
	}
}

func TestSignInGatesPage(t *testing.T) {
	srv, g := newAuthServer(t)
	res := send(t, "GET", srv.URL+"/", "")
	if page := readBody(res); res.StatusCode != 401 || strings.Contains(page, "slovensky-raj") || !strings.Contains(page, "/auth/login") {
		t.Fatalf("signed-out page leaks or lacks login: %d", res.StatusCode)
	}
	if res := send(t, "GET", srv.URL+"/api/v1/itineraries", ""); res.StatusCode != 401 {
		t.Fatalf("signed-out API read: %d", res.StatusCode)
	}

	g.email = "stranger@example.com"
	if res := signIn(t, srv, g, send(t, "GET", srv.URL+"/auth/login", "")); res.StatusCode != 403 || cookieOf(res, loginCookie) != nil {
		t.Fatalf("unlisted account: %d", res.StatusCode)
	}

	g.email = "mama@example.com"
	callback := signIn(t, srv, g, send(t, "GET", srv.URL+"/auth/login", ""))
	session := cookieOf(callback, loginCookie)
	if callback.StatusCode != 303 || callback.Header.Get("Location") != "/" || session == nil || !session.HttpOnly {
		t.Fatalf("web sign-in: %d %v", callback.StatusCode, session)
	}
	res = send(t, "GET", srv.URL+"/", "", "Cookie", session.String())
	if page := readBody(res); res.StatusCode != 200 || !strings.Contains(page, "slovensky-raj") || !strings.Contains(page, "mama@example.com") {
		t.Fatalf("signed-in page: %d", res.StatusCode)
	}
	if res := send(t, "GET", srv.URL+"/api/v1/itineraries", "", "Cookie", session.String()); res.StatusCode != 200 {
		t.Fatalf("signed-in API read: %d", res.StatusCode)
	}
	if res := send(t, "POST", srv.URL+"/api/v1/itineraries", sampleItinerary, "Cookie", session.String()); res.StatusCode != 401 {
		t.Fatalf("the web session must stay read-only: %d", res.StatusCode)
	}
	if res := send(t, "GET", srv.URL+callbackURL+"?code=google-code&state=forged", "", "Cookie", nonceCookie+"=x"); res.StatusCode != 400 {
		t.Fatalf("forged state: %d", res.StatusCode)
	}
}
