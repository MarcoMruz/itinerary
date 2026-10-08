package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, method, url string, header ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(60, 2)
	now := time.Unix(0, 0)
	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("1st request must pass")
	}
	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("burst request must pass")
	}
	ok, wait := l.allow("a", now)
	if ok || wait != time.Second {
		t.Fatalf("over burst: ok=%t wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b", now); !ok {
		t.Fatal("other clients have their own bucket")
	}
	if ok, _ := l.allow("a", now.Add(time.Second)); !ok {
		t.Fatal("bucket must refill")
	}
	l.allow("a", now.Add(time.Hour))
	if len(l.buckets) != 1 {
		t.Fatalf("idle buckets must be swept, have %d", len(l.buckets))
	}
}

func TestRateLimitResponds429(t *testing.T) {
	srv, _ := newTestServerWith(t, API{}, SecurityConfig{RatePerMinute: 60, RateBurst: 1})
	if res := request(t, "GET", srv.URL+"/api/v1/itineraries"); res.StatusCode != 200 {
		t.Fatalf("first: %d", res.StatusCode)
	}
	res := request(t, "GET", srv.URL+"/api/v1/itineraries")
	if res.StatusCode != 429 || res.Header.Get("Retry-After") != "1" {
		t.Fatalf("second: %d retry-after=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res := request(t, "GET", srv.URL+"/healthz"); res.StatusCode != 200 {
		t.Fatalf("healthz must bypass the limiter: %d", res.StatusCode)
	}
}

func TestCORS(t *testing.T) {
	srv, _ := newTestServerWith(t, API{}, SecurityConfig{CORSOrigins: []string{"https://app.example"}})
	api := srv.URL + "/api/v1/itineraries"

	res := request(t, "OPTIONS", api, "Origin", "https://app.example", "Access-Control-Request-Method", "POST")
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("allowed preflight: %d %v", res.StatusCode, res.Header)
	}
	if res := request(t, "OPTIONS", api, "Origin", "https://evil.example", "Access-Control-Request-Method", "POST"); res.StatusCode != 403 {
		t.Fatalf("disallowed preflight: want 403, got %d", res.StatusCode)
	}
	res = request(t, "GET", api, "Origin", "https://evil.example")
	if res.Header.Get("Access-Control-Allow-Origin") != "" || !slices.Contains(res.Header.Values("Vary"), "Origin") {
		t.Fatalf("disallowed origin got CORS headers: %v", res.Header)
	}
}

func TestBotBlocking(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for _, ua := range []string{"Mozilla/5.0 (compatible; GPTBot/1.0)", "sqlmap/1.7"} {
		if res := request(t, "GET", srv.URL+"/", "User-Agent", ua); res.StatusCode != 403 {
			t.Errorf("%s: want 403, got %d", ua, res.StatusCode)
		}
	}
	if res := request(t, "GET", srv.URL+"/robots.txt", "User-Agent", "GPTBot"); res.StatusCode != 200 {
		t.Errorf("robots.txt must stay reachable: %d", res.StatusCode)
	}
	if res := request(t, "GET", srv.URL+"/", "User-Agent", "Mozilla/5.0 Safari/605.1.15"); res.StatusCode != 200 {
		t.Errorf("browser blocked: %d", res.StatusCode)
	}
	if !isBadBot("") || isBadBot("claude-code/2.0") {
		t.Error("empty UA must be blocked, MCP clients allowed")
	}
}

func TestCacheHeaders(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for path, want := range map[string]string{
		"/":                          "private, no-cache",
		"/api/v1/itineraries":        "no-store",
		"/api/v1/openapi.json":       "public, max-age=300",
		"/robots.txt":                "public, max-age=86400",
		"/api/v1/itineraries/absent": "no-store",
	} {
		res := request(t, "GET", srv.URL+path)
		if got := res.Header.Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
		if res.Header.Get("X-Robots-Tag") == "" {
			t.Errorf("%s: missing X-Robots-Tag", path)
		}
	}
}

func TestClientIPAndOrigins(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientIP(r, ""); got != "10.0.0.1" {
		t.Errorf("untrusted header used: %s", got)
	}
	if got := clientIP(r, "CF-Connecting-IP"); got != "203.0.113.7" {
		t.Errorf("trusted header ignored: %s", got)
	}
	r.Header.Set("CF-Connecting-IP", strings.Repeat("x", 1000))
	if got := clientIP(r, "CF-Connecting-IP"); got != "10.0.0.1" {
		t.Errorf("non-IP header value must fall back to peer IP: %s", got)
	}
	if got := parseOrigins(" https://a.example/ ,,https://b.example"); !slices.Equal(got, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("parseOrigins: %v", got)
	}
}
