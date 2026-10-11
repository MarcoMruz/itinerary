package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExportImportRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t, "write")
	api := srv.URL + "/api/v1"

	code, body := do(t, "GET", api+"/itineraries/slovensky-raj-1yo/export", "", "Authorization", "Bearer write")
	var doc struct {
		Format       string          `json:"format"`
		Action       string          `json:"action"`
		Instructions string          `json:"instructions"`
		Schema       map[string]any  `json:"schema"`
		Itinerary    json.RawMessage `json:"itinerary"`
	}
	if code != 200 || json.Unmarshal(body, &doc) != nil || doc.Format != exportFormat || doc.Action != "update" || doc.Instructions == "" || doc.Schema == nil {
		t.Fatalf("export: %d %s", code, body)
	}

	// An assistant edits the title and drops the checklist, answering in a fenced block with prose around it.
	var it Itinerary
	json.Unmarshal(doc.Itinerary, &it)
	it.Title, it.Checklist = "Slovenský raj – upravené", []string{}
	edited, _ := json.Marshal(map[string]any{"format": exportFormat, "version": 1, "action": "update", "itinerary": it})
	answer := "Here is the updated itinerary:\n```json\n" + string(edited) + "\n```\nEnjoy!"

	if code, _ := do(t, "POST", api+"/import?dryRun=true", answer); code != 401 {
		t.Fatalf("import without write access: want 401, got %d", code)
	}
	code, body = do(t, "POST", api+"/import?dryRun=true", answer, "Authorization", "Bearer write")
	var preview ImportResult
	json.Unmarshal(body, &preview)
	if code != 200 || preview.Action != "update" || preview.Saved || preview.PreviousTitle == "" || strings.Join(preview.Changes, ",") != "title,checklist" {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if _, b := do(t, "GET", api+"/itineraries/slovensky-raj-1yo", "", "Authorization", "Bearer write"); strings.Contains(string(b), "upravené") {
		t.Fatal("dry run must not save")
	}
	if code, body = do(t, "POST", api+"/import", answer, "Authorization", "Bearer write"); code != 200 || !strings.Contains(string(body), `"saved":true`) {
		t.Fatalf("import update: %d %s", code, body)
	}
	_, b := do(t, "GET", api+"/itineraries/slovensky-raj-1yo", "", "Authorization", "Bearer write")
	var stored Itinerary
	json.Unmarshal(b, &stored)
	if stored.Title != "Slovenský raj – upravené" || len(stored.Checklist) != 0 || len(stored.Days) != len(it.Days) {
		t.Fatalf("stored after update: %+v", stored)
	}
}

func TestImportCreateFromTemplate(t *testing.T) {
	srv, _ := newTestServer(t, "")
	api := srv.URL + "/api/v1"
	code, body := do(t, "GET", api+"/import/template", "")
	if code != 200 || !strings.Contains(string(body), `"action": "create"`) {
		t.Fatalf("template: %d %s", code, body)
	}
	// The template itself imports as a new itinerary, twice: the derived id gets a suffix.
	for _, want := range []string{"nazov-vyletu", "nazov-vyletu-2"} {
		code, body = do(t, "POST", api+"/import", string(body))
		var res ImportResult
		json.Unmarshal(body, &res)
		if code != 201 || res.Action != "create" || res.ID != want {
			t.Fatalf("create %s: %d %s", want, code, body)
		}
		_, body = do(t, "GET", api+"/import/template", "")
	}
	// A bare itinerary without envelope works too.
	if code, body = do(t, "POST", api+"/import", sampleItinerary); code != 201 {
		t.Fatalf("bare itinerary: %d %s", code, body)
	}
}

func TestImportErrors(t *testing.T) {
	srv, _ := newTestServer(t, "")
	url := srv.URL + "/api/v1/import"
	cases := map[string]struct {
		body string
		code int
	}{
		"no json":            {"I could not do that.", 400},
		"wrong format":       {`{"format":"other","itinerary":{"title":"x","days":[{"activities":[]}]}}`, 400},
		"update missing":     {`{"action":"update","itinerary":{"id":"nope","title":"x","days":[{"activities":[]}]}}`, 400},
		"update without id":  {`{"action":"update","itinerary":{"title":"x","days":[{"activities":[]}]}}`, 400},
		"create existing id": {`{"action":"create","itinerary":{"id":"slovensky-raj-1yo","title":"x","days":[{"activities":[]}]}}`, 409},
		"unknown field":      {`{"action":"create","itinerary":{"title":"x","notes":"y","days":[{"activities":[]}]}}`, 400},
		"bad action":         {`{"action":"delete","itinerary":{"title":"x","days":[{"activities":[]}]}}`, 400},
		"invalid itinerary":  {`{"action":"create","itinerary":{"title":"x","days":[]}}`, 400},
	}
	for name, c := range cases {
		if code, body := do(t, "POST", url, c.body); code != c.code {
			t.Errorf("%s: want %d, got %d %s", name, c.code, code, body)
		}
	}
}
