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
	path := filepath.Join(t.TempDir(), "data", "itineraries.json")
	store, err := OpenStore(path, seedData)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHandler(store, &API{store: store, token: token})
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
	if code, _ := do(t, "GET", api, ""); code != 200 {
		t.Fatalf("reads must stay public, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary); code != 401 {
		t.Fatalf("unauthenticated create: want 401, got %d", code)
	}
	if code, _ := do(t, "POST", api, sampleItinerary, "Authorization", "Bearer s3cret"); code != 201 {
		t.Fatalf("authenticated create: want 201, got %d", code)
	}
	_, body := do(t, "POST", srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_itinerary","arguments":`+sampleItinerary+`}}`)
	if !strings.Contains(string(body), `"isError":true`) {
		t.Fatalf("unauthenticated MCP add must fail: %s", body)
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
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
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
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`:                                rpcMethodNotFound,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"nope"}}`: rpcInvalidParams,
		`{bad`: rpcParseError,
	} {
		if got := rpc(t, mcp, body)["error"].(map[string]any)["code"]; got != code {
			t.Errorf("%s: want code %v, got %v", body, code, got)
		}
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

func TestOpenStoreRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "itineraries.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := OpenStore(path, seedData); err == nil {
		t.Fatal("corrupt file must not be silently replaced")
	}
}
