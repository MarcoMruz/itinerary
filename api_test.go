package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleItinerary = `{"title":"Vysoké Tatry s deťmi","days":[{"activities":[{"time":"09:00","title":"Štrbské pleso","mapsUrl":"https://maps.google.com/?q=Strbske+pleso"}]}],"checklist":["Pršiplášť"]}`

func newTestServer(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	return newTestServerWith(t, API{token: token}, SecurityConfig{BlockBots: true})
}

func newTestServerWith(t *testing.T, api API, sec SecurityConfig) (*httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "itineraries.json")
	store, err := OpenStore(path, seedData)
	if err != nil {
		t.Fatal(err)
	}
	api.store = store
	h, err := newHandler(store, &api, sec)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, path
}

func do(t *testing.T, method, url, body string, header ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestRESTLifecycleAndPersistence(t *testing.T) {
	srv, path := newTestServer(t, "")
	api := srv.URL + "/api/v1/itineraries"

	if code, body := do(t, "GET", api, ""); code != 200 || !strings.Contains(string(body), "slovensky-raj-1yo") {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body := do(t, "POST", api, sampleItinerary)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var created Itinerary
	json.Unmarshal(body, &created)
	if created.ID != "vysoke-tatry-s-detmi" || created.Days[0].Label != "Deň 1" || created.Duration != "1 deň" {
		t.Fatalf("unexpected defaults: %+v", created)
	}
	if code, body = do(t, "POST", api, sampleItinerary); code != 201 || !strings.Contains(string(body), `"vysoke-tatry-s-detmi-2"`) {
		t.Fatalf("derived id collision: %d %s", code, body)
	}
	if code, _ = do(t, "POST", api, `{"id":"slovensky-raj-1yo","title":"x","days":[{"activities":[]}]}`); code != 409 {
		t.Fatalf("explicit duplicate: want 409, got %d", code)
	}

	reopened, err := OpenStore(path, nil)
	if err != nil || len(reopened.List()) != 3 {
		t.Fatalf("persisted store: %v %d", err, len(reopened.List()))
	}

	if code, _ = do(t, "DELETE", api+"/vysoke-tatry-s-detmi", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = do(t, "GET", api+"/vysoke-tatry-s-detmi", ""); code != 404 {
		t.Fatalf("get deleted: %d", code)
	}
	if code, _ = do(t, "DELETE", api+"/missing", ""); code != 404 {
		t.Fatalf("delete missing: %d", code)
	}
}

func TestValidation(t *testing.T) {
	srv, _ := newTestServer(t, "")
	api := srv.URL + "/api/v1/itineraries"
	for name, body := range map[string]string{
		"missing title":  `{"days":[{"activities":[]}]}`,
		"no days":        `{"title":"x","days":[]}`,
		"js url":         `{"title":"x","days":[{"activities":[{"time":"1","title":"a","mapsUrl":"javascript:alert(1)"}]}]}`,
		"bad id":         `{"id":"Bad ID","title":"x","days":[{"activities":[]}]}`,
		"unknown field":  `{"name":"x","days":[{"activities":[]}]}`,
		"malformed json": `{`,
	} {
		if code, b := do(t, "POST", api, body); code != 400 {
			t.Errorf("%s: want 400, got %d %s", name, code, b)
		}
	}
	if code, _ := do(t, "POST", api, `{"title":"`+strings.Repeat("a", maxBodyBytes)+`"}`); code != 413 {
		t.Errorf("oversized body: want 413, got %d", code)
	}
}

func TestWriteAuth(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret")
	api := srv.URL + "/api/v1/itineraries"
	if code, _ := do(t, "GET", api, ""); code != 401 {
		t.Fatalf("unauthenticated read: want 401, got %d", code)
	}
	if code, _ := do(t, "GET", api, "", "Authorization", "Bearer s3cret"); code != 200 {
		t.Fatalf("write token must also read, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary); code != 401 {
		t.Fatalf("unauthenticated create: want 401, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary, "Authorization", "Bearer s3cret"); code != 201 {
		t.Fatalf("authenticated create: want 201, got %d", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_itinerary","arguments":`+sampleItinerary+`}}`); code != 401 {
		t.Fatalf("unauthenticated MCP: want 401, got %d", code)
	}
}

func TestReadAuth(t *testing.T) {
	srv, _ := newTestServerWith(t, API{token: "write-secret", readToken: "read-secret"}, SecurityConfig{})
	api := srv.URL + "/api/v1/itineraries"
	if code, _ := do(t, "GET", api, "", "Authorization", "Bearer read-secret"); code != 200 {
		t.Fatalf("read token: want 200, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary, "Authorization", "Bearer read-secret"); code != 401 {
		t.Fatalf("read token must not write, got %d", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != 401 {
		t.Fatalf("unauthenticated MCP: want 401, got %d", code)
	}
	if code, b := do(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add_itinerary","arguments":`+sampleItinerary+`}}`, "Authorization", "Bearer read-secret"); code != 200 || !strings.Contains(string(b), `"isError":true`) {
		t.Fatalf("MCP add with read token must fail: %d %s", code, b)
	}
}

func TestReadTokenOnlyDisablesWrites(t *testing.T) {
	srv, _ := newTestServerWith(t, API{readToken: "read-secret"}, SecurityConfig{})
	api := srv.URL + "/api/v1/itineraries"
	for _, auth := range []string{"", "Bearer read-secret"} {
		if code, _ := do(t, "POST", api, sampleItinerary, "Authorization", auth); code != 401 {
			t.Fatalf("write with %q: want 401, got %d", auth, code)
		}
	}
}

func TestUISessionCookieGrantsReadOnly(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret")
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/" {
		t.Fatalf("session cookie: %+v", cookie)
	}
	api := srv.URL + "/api/v1/itineraries"
	if code, _ := do(t, "GET", api+"/slovensky-raj-1yo", "", "Cookie", cookie.String()); code != 200 {
		t.Fatalf("cookie read: want 200, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary, "Cookie", cookie.String()); code != 401 {
		t.Fatalf("cookie must not write, got %d", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Cookie", cookie.String()); code != 401 {
		t.Fatalf("cookie must not open MCP, got %d", code)
	}
	if code, _ := do(t, "GET", api, "", "Cookie", sessionCookie+"=forged"); code != 401 {
		t.Fatalf("forged cookie: want 401, got %d", code)
	}
}

func rpc(t *testing.T, url, body string) map[string]any {
	t.Helper()
	code, b := do(t, "POST", url, body, "Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	if code != 200 {
		t.Fatalf("rpc status %d: %s", code, b)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCP(t *testing.T) {
	srv, _ := newTestServer(t, "")
	mcp := srv.URL + "/mcp"

	init := rpc(t, mcp, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if v := init["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Fatalf("protocol negotiation: %v", v)
	}
	if code, _ := do(t, "POST", mcp, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != 202 {
		t.Fatalf("notification: want 202, got %d", code)
	}
	tools := rpc(t, mcp, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("want 4 tools, got %d", len(tools))
	}

	text := func(res map[string]any) (string, bool) {
		r := res["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"] == true
	}
	if s, isErr := text(rpc(t, mcp, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_itineraries","arguments":{}}}`)); isErr || !strings.Contains(s, "slovensky-raj-1yo") {
		t.Fatalf("list_itineraries: %s", s)
	}
	if s, isErr := text(rpc(t, mcp, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_itinerary_detail","arguments":{"id":"slovensky-raj-1yo"}}}`)); isErr || !strings.Contains(s, "Hrdlo Hornádu") {
		t.Fatalf("get_itinerary_detail: %s", s)
	}
	if _, isErr := text(rpc(t, mcp, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_itinerary_detail","arguments":{"id":"nope"}}}`)); !isErr {
		t.Fatal("missing itinerary must be a tool error")
	}
	if s, isErr := text(rpc(t, mcp, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"add_itinerary","arguments":`+sampleItinerary+`}}`)); isErr || !strings.Contains(s, "vysoke-tatry-s-detmi") {
		t.Fatalf("add_itinerary: %s", s)
	}

	for body, code := range map[string]float64{
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`:                                                                                                               rpcMethodNotFound,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"nope"}}`:                                                                                rpcInvalidParams,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"list_itineraries","_meta":{},"typo":true}}`:                                             rpcInvalidParams,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"get_itinerary_detail","_meta":{},"arguments":{"id":"slovensky-raj-1yo","typo":true}}}`: rpcInvalidParams,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"list_itineraries","_meta":"invalid"}}`:                                                 rpcInvalidParams,
		`{bad`: rpcParseError,
	} {
		if got := rpc(t, mcp, body)["error"].(map[string]any)["code"]; got != code {
			t.Errorf("%s: want code %v, got %v", body, code, got)
		}
	}
}

func TestMCPRequestMetadata(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
		want   string
	}{
		{"initialize", "initialize", `"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}`, "2025-03-26"},
		{"list", "tools/call", `"name":"list_itineraries","arguments":{}`, "slovensky-raj-1yo"},
		{"detail", "tools/call", `"name":"get_itinerary_detail","arguments":{"id":"slovensky-raj-1yo"}`, "Hrdlo Hornádu"},
		{"add", "tools/call", `"name":"add_itinerary","arguments":` + sampleItinerary, "vysoke-tatry-s-detmi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(t, "")
			body := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":{` + tc.params + `,"_meta":{"progressToken":1,"client/example":{"enabled":true}}}}`
			res := rpc(t, srv.URL+"/mcp", body)
			if res["error"] != nil {
				t.Fatalf("request with metadata failed: %v", res["error"])
			}
			result, _ := json.Marshal(res["result"])
			if !strings.Contains(string(result), tc.want) || strings.Contains(string(result), `"isError":true`) {
				t.Fatalf("want %q in successful result, got %s", tc.want, result)
			}
		})
	}
}

func TestIndexAndOpenAPI(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	code, body := do(t, "GET", srv.URL+"/", "")
	if code != 200 || !bytes.Contains(body, []byte(`"slovensky-raj-1yo"`)) || bytes.Contains(body, []byte("<script>alert")) {
		t.Fatalf("index: %d", code)
	}
	code, body = do(t, "GET", srv.URL+"/api/v1/openapi.json", "")
	var doc map[string]any
	if code != 200 || json.Unmarshal(body, &doc) != nil || doc["openapi"] != "3.1.0" || !strings.Contains(string(body), "bearerAuth") {
		t.Fatalf("openapi: %d %s", code, body)
	}
	if code, _ = do(t, "GET", srv.URL+"/nope", ""); code != 404 {
		t.Fatalf("unknown path: %d", code)
	}
}

func TestPlaceDerivesMapsURL(t *testing.T) {
	it := Itinerary{Title: "x", StartLocation: " Hotel Park ", Days: []Day{{Activities: []Activity{{Time: "9", Title: "a", Place: "Koliba Podlesok"}}}}}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	if got := it.Days[0].Activities[0].MapsURL; got != "https://www.google.com/maps/search/?api=1&query=Koliba+Podlesok" {
		t.Fatalf("derived mapsUrl: %s", got)
	}
	if it.StartLocation != "Hotel Park" {
		t.Fatalf("startLocation not trimmed: %q", it.StartLocation)
	}
}

func TestOpenStoreRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "itineraries.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := OpenStore(path, seedData); err == nil {
		t.Fatal("corrupt file must not be silently replaced")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	if _, err := decodeItinerary(strings.NewReader(sampleItinerary + " {}")); err == nil {
		t.Fatal("trailing JSON must be rejected")
	}
}

func TestOpenStoreRejectsNullItinerary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "itineraries.json")
	if err := os.WriteFile(path, []byte("[null]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path, seedData); err == nil {
		t.Fatal("null itinerary must be rejected")
	}
}

func TestStoreDoesNotExposeMutableItineraries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "itineraries.json")
	store, err := OpenStore(path, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	it := &Itinerary{ID: "trip", Title: "Trip", Days: []Day{{Activities: []Activity{}}}}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(it); err != nil {
		t.Fatal(err)
	}
	it.Title = "changed"
	got, ok := store.Get("trip")
	if !ok || got.Title != "Trip" {
		t.Fatalf("store exposed mutable input: %+v", got)
	}
	got.Title = "changed again"
	stored, ok := store.Get("trip")
	if !ok || stored.Title != "Trip" {
		t.Fatalf("store exposed mutable result: %+v", stored)
	}
}
