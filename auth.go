package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Google sign-in for the web page, plus an OAuth 2.1 authorization server so
// MCP clients (claude.ai connectors, Claude Code) can connect without a static
// token. Google proves who the person is; only ALLOWED_EMAILS get in.
// Everything issued here (client ids, codes, tokens, cookies) is an
// HMAC-signed blob, so only the signing key and recently used codes are kept.

const (
	googleAuthEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint = "https://oauth2.googleapis.com/token"

	loginCookie = "itinerary_login"
	nonceCookie = "itinerary_oauth_nonce"
	callbackURL = "/auth/google/callback"
	oauthScope  = "itineraries"

	loginTTL   = 10 * time.Minute // Google round trip and consent screen
	codeTTL    = 5 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 60 * 24 * time.Hour
	sessionTTL = 30 * 24 * time.Hour
)

// AuthConfig enables Google sign-in when GoogleClientID is set.
type AuthConfig struct {
	GoogleClientID     string
	GoogleClientSecret string
	AllowedEmails      []string
	RedirectHosts      []string // hosts MCP clients may register redirect URIs on
	PublicURL          string
	Secret             []byte
}

type Auth struct {
	base          string
	clientID      string
	clientSecret  string
	allowed       map[string]bool
	redirectHosts []string
	key           []byte
	googleAuth    string
	googleToken   string
	http          *http.Client
	tmpl          *template.Template

	mu        sync.Mutex
	usedCodes map[string]time.Time
}

func NewAuth(cfg AuthConfig) (*Auth, error) {
	if cfg.GoogleClientSecret == "" || cfg.PublicURL == "" || len(cfg.AllowedEmails) == 0 || len(cfg.Secret) < 32 {
		return nil, errors.New("Google sign-in needs GOOGLE_CLIENT_SECRET, PUBLIC_URL, ALLOWED_EMAILS and a 32+ byte signing secret")
	}
	tmpl, err := template.ParseFS(templateFS, "templates/auth.html")
	if err != nil {
		return nil, err
	}
	a := &Auth{
		base:          strings.TrimRight(cfg.PublicURL, "/"),
		clientID:      cfg.GoogleClientID,
		clientSecret:  cfg.GoogleClientSecret,
		allowed:       map[string]bool{},
		redirectHosts: cfg.RedirectHosts,
		key:           cfg.Secret,
		googleAuth:    googleAuthEndpoint,
		googleToken:   googleTokenEndpoint,
		http:          &http.Client{Timeout: 10 * time.Second},
		tmpl:          tmpl,
		usedCodes:     map[string]time.Time{},
	}
	for _, e := range cfg.AllowedEmails {
		a.allowed[strings.ToLower(e)] = true
	}
	return a, nil
}

// loadAuthSecret returns AUTH_SECRET, or a random key kept beside the data so
// sessions and tokens survive restarts. Deleting the file signs everyone out.
func loadAuthSecret(dir string) ([]byte, error) {
	if s := os.Getenv("AUTH_SECRET"); s != "" {
		return []byte(s), nil
	}
	path := filepath.Join(dir, "auth-secret.key")
	data, err := os.ReadFile(path)
	if err == nil {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := []byte(randomToken())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return secret, writeAtomicMode(path, secret, 0o600)
}

func (a *Auth) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", a.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", a.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.serverMetadata)
	mux.HandleFunc("POST /oauth/register", a.register)
	mux.HandleFunc("GET /oauth/authorize", a.authorize)
	mux.HandleFunc("POST /oauth/consent", a.consent)
	mux.HandleFunc("POST /oauth/token", a.token)
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET "+callbackURL, a.googleCallback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

// ---------- Signed grants ----------

// grant is the payload of every signed value; Kind keeps one kind from being
// replayed as another (e.g. a refresh token used as an access token).
type grant struct {
	Kind         string   `json:"k"`
	Exp          int64    `json:"x,omitempty"` // unix seconds; 0 = never (client ids)
	ID           string   `json:"i,omitempty"`
	Email        string   `json:"e,omitempty"`
	Client       string   `json:"c,omitempty"`
	Name         string   `json:"n,omitempty"`
	RedirectURIs []string `json:"u,omitempty"`
	AuthMethod   string   `json:"m,omitempty"`
	RedirectURI  string   `json:"r,omitempty"`
	State        string   `json:"s,omitempty"`
	Challenge    string   `json:"p,omitempty"`
	Scope        string   `json:"sc,omitempty"`
	Nonce        string   `json:"o,omitempty"`
}

func (a *Auth) mac(data string) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (a *Auth) seal(g grant, ttl time.Duration) string {
	if ttl > 0 {
		g.Exp = time.Now().Add(ttl).Unix()
	}
	data, _ := json.Marshal(g)
	payload := base64.RawURLEncoding.EncodeToString(data)
	return payload + "." + base64.RawURLEncoding.EncodeToString(a.mac(payload))
}

func (a *Auth) open(kind, value string) (grant, bool) {
	var g grant
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return g, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, a.mac(payload)) {
		return g, false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &g) != nil || g.Kind != kind {
		return grant{}, false
	}
	if g.Exp != 0 && time.Now().Unix() > g.Exp {
		return grant{}, false
	}
	return g, true
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// useCode marks an authorization code as spent; codes are single use.
func (a *Auth) useCode(id string, exp int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, until := range a.usedCodes {
		if now.After(until) {
			delete(a.usedCodes, k)
		}
	}
	if _, spent := a.usedCodes[id]; spent {
		return false
	}
	a.usedCodes[id] = time.Unix(exp, 0).Add(time.Minute)
	return true
}

func (a *Auth) isAllowed(email string) bool { return email != "" && a.allowed[strings.ToLower(email)] }

// bearerEmail returns the person behind a valid OAuth access token.
func (a *Auth) bearerEmail(r *http.Request) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	g, ok := a.open("access", tok)
	if !ok || !a.isAllowed(g.Email) {
		return ""
	}
	return g.Email
}

// sessionEmail returns the person signed in to the web page.
func (a *Auth) sessionEmail(r *http.Request) string {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return ""
	}
	g, ok := a.open("session", c.Value)
	if !ok || !a.isAllowed(g.Email) {
		return ""
	}
	return g.Email
}

func (a *Auth) secure() bool { return strings.HasPrefix(a.base, "https://") }

func (a *Auth) resourceMetadataURL() string {
	return a.base + "/.well-known/oauth-protected-resource/mcp"
}

// ---------- Metadata (RFC 9728, RFC 8414) ----------

func (a *Auth) resourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, schema{
		"resource":                 a.base + "/mcp",
		"authorization_servers":    []string{a.base},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{oauthScope},
		"resource_name":            "Rodinné itineráre",
	})
}

func (a *Auth) serverMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, schema{
		"issuer":                                         a.base,
		"authorization_endpoint":                         a.base + "/oauth/authorize",
		"token_endpoint":                                 a.base + "/oauth/token",
		"registration_endpoint":                          a.base + "/oauth/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"scopes_supported":                               []string{oauthScope},
		"authorization_response_iss_parameter_supported": true,
	})
}

// ---------- Dynamic client registration (RFC 7591) ----------

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// redirectAllowed accepts https URIs on the configured hosts, and http only on loopback.
func (a *Auth) redirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2000 || u.Fragment != "" || u.User != nil || !slices.Contains(a.redirectHosts, u.Hostname()) {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	return u.Scheme == "https" || (u.Scheme == "http" && loopback)
}

func (a *Auth) clientSecretFor(clientID string) string {
	return hex.EncodeToString(a.mac("client-secret\x00" + clientID))
}

func (a *Auth) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 5 redirect_uris are required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !a.redirectAllowed(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI not allowed: "+u)
			return
		}
	}
	method := req.AuthMethod
	if method == "" {
		method = "client_secret_basic" // RFC 7591 default
	}
	if !slices.Contains([]string{"none", "client_secret_basic", "client_secret_post"}, method) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" || len(name) > 100 {
		name = "MCP klient"
	}
	id := a.seal(grant{Kind: "client", Name: name, RedirectURIs: req.RedirectURIs, AuthMethod: method}, 0)
	resp := schema{
		"client_id":                  id,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": method,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if method != "none" {
		resp["client_secret"] = a.clientSecretFor(id)
		resp["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ---------- Authorization ----------

type authPage struct {
	Kind    string // login, consent or message
	Title   string
	Message string
	Client  string
	Host    string
	Email   string
	Token   string
}

func (a *Auth) render(w http.ResponseWriter, status int, p authPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := a.tmpl.ExecuteTemplate(w, "page", p); err != nil {
		log.Printf("render auth page: %v", err)
	}
}

func (a *Auth) message(w http.ResponseWriter, status int, title, msg string) {
	a.render(w, status, authPage{Kind: "message", Title: title, Message: msg})
}

// renderLogin is what visitors without a session see instead of the itineraries.
func (a *Auth) renderLogin(w http.ResponseWriter) {
	a.render(w, http.StatusUnauthorized, authPage{Kind: "login", Title: "Prihlásenie"})
}

func redirectWith(w http.ResponseWriter, r *http.Request, target string, params url.Values) {
	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (a *Auth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, ok := a.open("client", q.Get("client_id"))
	if !ok {
		a.message(w, http.StatusBadRequest, "Neznámy klient", "Aplikácia nie je zaregistrovaná. Pridajte konektor znova.")
		return
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" && len(client.RedirectURIs) == 1 {
		redirectURI = client.RedirectURIs[0]
	}
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		a.message(w, http.StatusBadRequest, "Neplatné presmerovanie", "Adresa presmerovania nepatrí tejto aplikácii.")
		return
	}
	fail := func(code, desc string) {
		redirectWith(w, r, redirectURI, url.Values{"error": {code}, "error_description": {desc}, "state": {q.Get("state")}, "iss": {a.base}})
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = oauthScope
	}
	pending := grant{
		Client: q.Get("client_id"), Name: client.Name, RedirectURI: redirectURI,
		State: q.Get("state"), Challenge: q.Get("code_challenge"), Scope: scope,
	}
	if email := a.sessionEmail(r); email != "" {
		pending.Email = email
		a.renderConsent(w, pending)
		return
	}
	a.startGoogle(w, r, pending)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if a.sessionEmail(r) != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.startGoogle(w, r, grant{})
}

// startGoogle sends the browser to Google. The pending request rides in the
// signed state; the nonce cookie ties the callback to this browser.
func (a *Auth) startGoogle(w http.ResponseWriter, r *http.Request, pending grant) {
	pending.Kind = "login"
	pending.Nonce = randomToken()
	http.SetCookie(w, &http.Cookie{
		Name: nonceCookie, Value: pending.Nonce, Path: callbackURL, MaxAge: int(loginTTL.Seconds()),
		HttpOnly: true, Secure: a.secure(), SameSite: http.SameSiteLaxMode,
	})
	redirectWith(w, r, a.googleAuth, url.Values{
		"client_id":     {a.clientID},
		"redirect_uri":  {a.base + callbackURL},
		"response_type": {"code"},
		"scope":         {"openid email"},
		"state":         {a.seal(pending, loginTTL)},
		"nonce":         {pending.Nonce},
		"prompt":        {"select_account"},
	})
}

func (a *Auth) googleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" {
		a.message(w, http.StatusForbidden, "Prihlásenie zrušené", "Google prihlásenie nebolo dokončené.")
		return
	}
	pending, ok := a.open("login", q.Get("state"))
	c, err := r.Cookie(nonceCookie)
	if !ok || err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(pending.Nonce)) != 1 {
		a.message(w, http.StatusBadRequest, "Prihlásenie vypršalo", "Skúste sa prihlásiť znova.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: nonceCookie, Path: callbackURL, MaxAge: -1, HttpOnly: true, Secure: a.secure()})
	email, err := a.exchange(r.Context(), q.Get("code"), pending.Nonce)
	if err != nil {
		log.Printf("google sign-in: %v", err)
		a.message(w, http.StatusBadGateway, "Prihlásenie zlyhalo", "Google prihlásenie sa nepodarilo overiť. Skúste znova.")
		return
	}
	if !a.isAllowed(email) {
		log.Printf("google sign-in refused for %s", email)
		a.message(w, http.StatusForbidden, "Bez prístupu", "Účet "+email+" nemá prístup k týmto itinerárom.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: a.seal(grant{Kind: "session", Email: email}, sessionTTL), Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: a.secure(), SameSite: http.SameSiteLaxMode,
	})
	if pending.Client == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	pending.Email = email
	a.renderConsent(w, pending)
}

// exchange trades Google's code for the verified email. The ID token comes
// straight from Google's token endpoint over TLS, which OpenID Connect
// accepts in place of checking its signature (Core 3.1.3.7).
func (a *Auth) exchange(ctx context.Context, code, nonce string) (string, error) {
	form := url.Values{
		"code": {code}, "client_id": {a.clientID}, "client_secret": {a.clientSecret},
		"redirect_uri": {a.base + callbackURL}, "grant_type": {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.googleToken, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint: %s", res.Status)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	parts := strings.Split(tok.IDToken, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", err
	}
	var claims struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Exp           int64  `json:"exp"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Nonce         string `json:"nonce"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	switch {
	case claims.Iss != "https://accounts.google.com" && claims.Iss != "accounts.google.com":
		return "", errors.New("unexpected issuer")
	case claims.Aud != a.clientID:
		return "", errors.New("unexpected audience")
	case time.Now().Unix() > claims.Exp:
		return "", errors.New("expired id_token")
	case claims.Nonce != nonce:
		return "", errors.New("nonce mismatch")
	case claims.EmailVerified != true && claims.EmailVerified != "true":
		return "", errors.New("email not verified")
	}
	return strings.ToLower(claims.Email), nil
}

// renderConsent asks before handing a client access: someone could send a
// signed-in person an authorize link for a client they never added.
func (a *Auth) renderConsent(w http.ResponseWriter, pending grant) {
	pending.Kind = "consent"
	host := pending.RedirectURI
	if u, err := url.Parse(pending.RedirectURI); err == nil {
		host = u.Host
	}
	a.render(w, http.StatusOK, authPage{
		Kind: "consent", Title: "Povoliť prístup", Client: pending.Name, Host: host,
		Email: pending.Email, Token: a.seal(pending, loginTTL),
	})
}

func (a *Auth) consent(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != a.base {
		a.message(w, http.StatusForbidden, "Neplatná požiadavka", "Súhlas musí prísť z tejto stránky.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		a.message(w, http.StatusBadRequest, "Neplatná požiadavka", "Skúste to znova.")
		return
	}
	g, ok := a.open("consent", r.PostFormValue("consent"))
	if !ok || !a.isAllowed(g.Email) {
		a.message(w, http.StatusBadRequest, "Žiadosť vypršala", "Pridajte konektor znova.")
		return
	}
	if r.PostFormValue("decision") != "allow" {
		redirectWith(w, r, g.RedirectURI, url.Values{"error": {"access_denied"}, "state": {g.State}, "iss": {a.base}})
		return
	}
	code := a.seal(grant{
		Kind: "code", ID: randomToken(), Email: g.Email, Client: g.Client,
		RedirectURI: g.RedirectURI, Challenge: g.Challenge, Scope: g.Scope,
	}, codeTTL)
	redirectWith(w, r, g.RedirectURI, url.Values{"code": {code}, "state": {g.State}, "iss": {a.base}})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure()})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- Token endpoint ----------

func (a *Auth) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Pragma", "no-cache")
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	clientID, secret, basic := r.BasicAuth()
	if basic {
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
	} else {
		clientID, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	client, ok := a.open("client", clientID)
	if ok && client.AuthMethod != "none" {
		ok = hmac.Equal([]byte(secret), []byte(a.clientSecretFor(clientID)))
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="oauth"`)
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client or bad credentials")
		return
	}

	var g grant
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		g, ok = a.open("code", r.PostFormValue("code"))
		if !ok || g.Client != clientID || g.RedirectURI != r.PostFormValue("redirect_uri") || !pkceMatches(r.PostFormValue("code_verifier"), g.Challenge) || !a.useCode(g.ID, g.Exp) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired or reused authorization code")
			return
		}
	case "refresh_token":
		g, ok = a.open("refresh", r.PostFormValue("refresh_token"))
		if !ok || g.Client != clientID {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
			return
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	if !a.isAllowed(g.Email) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "account no longer has access")
		return
	}
	issued := grant{Email: g.Email, Client: clientID, Scope: g.Scope}
	issued.Kind = "access"
	access := a.seal(issued, accessTTL)
	issued.Kind = "refresh"
	writeJSON(w, http.StatusOK, schema{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": a.seal(issued, refreshTTL),
		"scope":         g.Scope,
	})
}

func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}
