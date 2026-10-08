package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestUpdateItinerary(t *testing.T) {
	srv, path := newTestServer(t, "")
	url := srv.URL + "/api/v1/itineraries/slovensky-raj-1yo"
	_, before := do(t, "GET", url, "")
	var original Itinerary
	json.Unmarshal(before, &original)
	code, body := do(t, "PATCH", url, `{"checklist":[" Water "]}`)
	if code != 200 {
		t.Fatalf("update: %d %s", code, body)
	}
	var updated Itinerary
	json.Unmarshal(body, &updated)
	original.Checklist = []string{"Water"}
	if !reflect.DeepEqual(original, updated) {
		t.Fatalf("unexpected changes: %+v", updated)
	}
	reopened, err := OpenStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Get(original.ID)
	if !reflect.DeepEqual(saved, &updated) {
		t.Fatal("update not persisted")
	}
	for _, invalid := range []string{`null`, `[]`, `{}`, `{"id":"other"}`, `{"checklist":null}`, `{"title":""}`, `{"days":[]}`, `{"typo":true}`, `{"days":[{"activities":[{"time":"9","title":"x","typo":true}]}]}`, `{"checklist":[]} {}`} {
		if code, b := do(t, "PATCH", url, invalid); code != 400 {
			t.Errorf("%s: %d %s", invalid, code, b)
		}
	}
	_, after := do(t, "GET", url, "")
	if string(after) != string(body) {
		t.Fatal("invalid update changed itinerary")
	}
	if code, b := do(t, "PATCH", url, `{"checklist":[],"subtitle":""}`); code != 200 || !strings.Contains(string(b), `"checklist":[]`) {
		t.Fatalf("clear: %d %s", code, b)
	}
	if code, _ := do(t, "PATCH", srv.URL+"/api/v1/itineraries/missing", `{"title":"x"}`); code != 404 {
		t.Fatalf("missing: %d", code)
	}
}

func TestUpdateAuthorizationAndMCP(t *testing.T) {
	srv, _ := newTestServerWith(t, API{token: "write", readToken: "read"}, SecurityConfig{})
	url := srv.URL + "/api/v1/itineraries/slovensky-raj-1yo"
	for _, token := range []string{"", "read"} {
		if code, _ := do(t, "PATCH", url, `{"checklist":[]}`, "Authorization", "Bearer "+token); code != 401 {
			t.Fatalf("token %q: %d", token, code)
		}
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_itinerary","_meta":{"progressToken":1},"arguments":{"id":"slovensky-raj-1yo","changes":{"checklist":["Water"]}}}}`
	_, denied := do(t, "POST", srv.URL+"/mcp", body, "Authorization", "Bearer read")
	if !strings.Contains(string(denied), `"isError":true`) {
		t.Fatalf("read token allowed: %s", denied)
	}
	_, result := do(t, "POST", srv.URL+"/mcp", body, "Authorization", "Bearer write")
	if strings.Contains(string(result), `"isError":true`) || !strings.Contains(string(result), "Water") {
		t.Fatalf("MCP update: %s", result)
	}
	if code, b := do(t, "PATCH", url, `{"title":"Updated"}`, "Authorization", "Bearer write"); code != 200 {
		t.Fatalf("write token: %d %s", code, b)
	}
}

func TestUpdateRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "itineraries.json")
	s, err := OpenStore(path, seedData)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get("slovensky-raj-1yo")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(before.ID, map[string]json.RawMessage{"title": json.RawMessage(`"Changed"`)})
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	after, _ := s.Get(before.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed write changed memory")
	}
}

func TestConcurrentUpdatesPreserveOtherFields(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "itineraries.json"), seedData)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for key, value := range map[string]string{"title": `"New title"`, "subtitle": `"New subtitle"`, "checklist": `["Water"]`} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Update("slovensky-raj-1yo", map[string]json.RawMessage{key: json.RawMessage(value)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	it, _ := s.Get("slovensky-raj-1yo")
	if it.Title != "New title" || it.Subtitle != "New subtitle" || !reflect.DeepEqual(it.Checklist, []string{"Water"}) {
		t.Fatalf("lost update: %+v", it)
	}
}

func TestMCPUpdateErrors(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for _, args := range []string{
		`{"id":"missing","changes":{"title":"x"}}`,
		`{"id":"slovensky-raj-1yo","changes":{"days":[]}}`,
		`{"id":"slovensky-raj-1yo","changes":{}}`,
		`{"changes":{"title":"x"}}`,
	} {
		res := rpc(t, srv.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_itinerary","arguments":`+args+`}}`)
		result, ok := res["result"].(map[string]any)
		if !ok || result["isError"] != true {
			t.Errorf("%s: %v", args, res)
		}
	}
}

func TestUpdateDiscovery(t *testing.T) {
	srv, _ := newTestServerWith(t, API{readToken: "read"}, SecurityConfig{CORSOrigins: []string{"https://example.com"}})
	_, body := do(t, "GET", srv.URL+"/api/v1/openapi.json", "")
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc["paths"].(map[string]any)["/api/v1/itineraries/{id}"].(map[string]any)["patch"].(map[string]any)
	if op["security"] == nil || op["operationId"] != "updateItinerary" {
		t.Fatalf("patch discovery: %v", op)
	}
	req, err := http.NewRequest("OPTIONS", srv.URL+"/api/v1/itineraries/slovensky-raj-1yo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Fatal("PATCH missing from CORS methods")
	}
}
