package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShareLinkOpensOneItineraryReadOnly(t *testing.T) {
	srv, g := newAuthServer(t)
	other := `{"action":"create","itinerary":{"title":"Tajný výlet","days":[{"activities":[]}]}}`
	if code, _ := do(t, "POST", srv.URL+"/api/v1/import", other, "Authorization", "Bearer write"); code != 201 {
		t.Fatalf("create second itinerary: %d", code)
	}
	link := srv.URL + "/api/v1/itineraries/slovensky-raj-1yo/share-link"
	if code, _ := do(t, "POST", link, ""); code != 401 {
		t.Fatalf("anonymous share: want 401, got %d", code)
	}
	session := cookieOf(signIn(t, srv, g, send(t, "GET", srv.URL+"/auth/login", "")), loginCookie)
	res := send(t, "POST", link, "", "Cookie", session.String(), "Origin", srv.URL)
	var out struct{ URL string }
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 || !strings.HasPrefix(out.URL, srv.URL+"/s/slovensky-raj-1yo/") {
		t.Fatalf("share link: %d %s", res.StatusCode, out.URL)
	}
	if res := send(t, "POST", link, "", "Cookie", session.String(), "Origin", srv.URL); readBody(res) == "" {
		t.Fatal("second request must return the same link")
	}

	// A stranger with the link, not signed in.
	page := send(t, "GET", out.URL, "")
	body := readBody(page)
	if page.StatusCode != 200 || !strings.Contains(body, "Hrdlo Hornádu") || strings.Contains(body, "Tajný výlet") || !strings.Contains(body, `"shared":true`) {
		t.Fatalf("shared page: %d leaks other itineraries or misses content", page.StatusCode)
	}
	share := cookieOf(page, shareCookie)
	if share == nil || share.Path != "/api/v1/itineraries/slovensky-raj-1yo/" {
		t.Fatalf("share cookie: %+v", share)
	}
	if res := send(t, "GET", srv.URL+"/api/v1/itineraries/slovensky-raj-1yo/export", "", "Cookie", share.String()); res.StatusCode != 200 {
		t.Fatalf("shared itinerary API read: %d", res.StatusCode)
	}
	for _, path := range []string{"/api/v1/itineraries", "/api/v1/itineraries/tajny-vylet/export", "/"} {
		if res := send(t, "GET", srv.URL+path, "", "Cookie", share.String()); res.StatusCode != 401 {
			t.Fatalf("share cookie must not open %s: %d", path, res.StatusCode)
		}
	}
	if res := send(t, "POST", srv.URL+"/api/v1/import", other, "Cookie", share.String(), "Origin", srv.URL); res.StatusCode != 401 {
		t.Fatalf("share cookie must not write: %d", res.StatusCode)
	}
	if res := send(t, "GET", srv.URL+"/s/slovensky-raj-1yo/wrong", ""); res.StatusCode != 404 {
		t.Fatalf("wrong key: %d", res.StatusCode)
	}

	// Revoking kills the link and the cookie it issued.
	if code, _ := do(t, "DELETE", link, "", "Authorization", "Bearer write"); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if res := send(t, "GET", out.URL, ""); res.StatusCode != 404 {
		t.Fatalf("revoked link: %d", res.StatusCode)
	}
	if res := send(t, "GET", srv.URL+"/api/v1/itineraries/slovensky-raj-1yo/export", "", "Cookie", share.String()); res.StatusCode != 401 {
		t.Fatalf("revoked cookie: %d", res.StatusCode)
	}
	code, b := do(t, "POST", link, "", "Authorization", "Bearer write")
	if code != 200 || strings.Contains(string(b), out.URL) {
		t.Fatalf("new link after revoke must differ: %d %s", code, b)
	}
}

func TestShareLinkMCPTool(t *testing.T) {
	srv, _ := newTestServer(t, "write")
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_share_link","arguments":{"id":"slovensky-raj-1yo"}}}`
	if _, b := do(t, "POST", srv.URL+"/mcp", call, "Authorization", "Bearer write"); !strings.Contains(string(b), "/s/slovensky-raj-1yo/") {
		t.Fatalf("MCP get_share_link: %s", b)
	}
}
