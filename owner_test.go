package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestOwnerChecklistAccess(t *testing.T) {
	srv, path := newTestServerWith(t, API{token: "write", readToken: "read"}, SecurityConfig{})
	base := srv.URL + "/api/v1/itineraries/slovensky-raj-1yo"
	for _, token := range []string{"", "read"} {
		if status, _ := do(t, "POST", base+"/owner-link", "", "Authorization", "Bearer "+token); status != 401 {
			t.Fatalf("owner link available with %q", token)
		}
	}
	status, body := do(t, "POST", base+"/owner-link", "", "Authorization", "Bearer write")
	if status != 200 {
		t.Fatalf("link: %d %s", status, body)
	}
	var link struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &link); err != nil {
		t.Fatal(err)
	}
	key := strings.Split(link.URL, "~")[1]
	if len(key) != 64 {
		t.Fatal("unexpected key length")
	}
	reopened, err := OpenStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.ownerKey("slovensky-raj-1yo", false)
	if err != nil || saved != key {
		t.Fatal("owner key not persisted")
	}
	_, public := do(t, "GET", srv.URL+"/", "")
	if strings.Contains(string(public), key) {
		t.Fatal("key exposed in public page")
	}
	if status, _ := do(t, "PATCH", base+"/checklist", `{"checklist":["Water"]}`); status != 401 {
		t.Fatal("anonymous edit allowed")
	}
	if status, _ := do(t, "POST", base+"/owner-session", `{"key":"invalid"}`); status != 401 {
		t.Fatal("invalid key accepted")
	}
	req, _ := http.NewRequest("POST", base+"/owner-session", strings.NewReader(`{"key":"`+key+`"}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) != 1 {
		t.Fatal("missing owner session")
	}
	cookie := res.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	for _, tc := range []struct {
		method, suffix, body string
		status               int
	}{
		{"GET", "/checklist-access", "", 200},
		{"PATCH", "/checklist", `{"checklist":["Water"]}`, 200},
		{"PATCH", "/checklist", `{"checklist":[]}`, 200},
		{"PATCH", "/checklist", `{"checklist":null}`, 400},
		{"PATCH", "/checklist", `{"checklist":[],"title":"changed"}`, 400},
		{"PATCH", "", `{"title":"changed"}`, 401},
	} {
		status, b := do(t, tc.method, base+tc.suffix, tc.body, "Cookie", cookie.String())
		if status != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.suffix, status, b)
		}
	}
	if status, _ := do(t, "PATCH", base+"/checklist", `{"checklist":[]}`, "Cookie", cookie.String(), "Origin", "https://other.example"); status != 403 {
		t.Fatal("cross origin edit allowed")
	}
	if status, _ := do(t, "PATCH", srv.URL+"/api/v1/itineraries/other/checklist", `{"checklist":[]}`, "Cookie", cookie.String()); status != 401 {
		t.Fatal("cross itinerary edit allowed")
	}
}
