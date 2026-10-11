package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Export files are made for AI assistants (ChatGPT, Gemini, Claude): the
// itinerary travels with instructions and its JSON Schema, the assistant
// edits or fills it in, and the answer is imported back here. The "action"
// says whether the file creates a new itinerary or replaces an existing one.

const exportFormat = "family-itinerary"

const exportInstructions = `This file is a family travel itinerary from the "Rodinné itineráre" app.
You are helping the user create or edit it. Follow these rules exactly:
1. Reply with the COMPLETE document in this same format: a single JSON object with "format", "version", "action" and "itinerary". Offer it as a downloadable .json file if you can, otherwise as one json code block. No text inside the JSON besides the data.
2. Keep "format" and "version" unchanged. Keep "action" unchanged: "update" edits the existing itinerary with the given "itinerary.id" (never change that id), "create" adds a new itinerary (leave "itinerary.id" out; it is derived from the title).
3. "itinerary" must follow "schema" (JSON Schema). Use only the fields it defines; required: "title" and "days", every activity needs "time" and "title".
4. The import replaces the whole itinerary: include every day, activity and checklist item that should remain, not just the changes.
5. Give every activity "place" and the "startLocation" as an official map name (as on Google Maps / OpenStreetMap) or a street address, so the app can compute driving distances. No descriptive extras like "parking" or "by the lake".
6. Write the content in the language of the existing itinerary (Slovak unless the user asks otherwise). Times like "09:00 - 11:30" or "15:00+". Day labels like "Piatok" or "Deň 1".
7. For a template, replace every example value with real content for the user's trip.
After the user uploads your file in the app (Import), the app shows a preview and saves it.`

var templateItinerary = schema{
	"title":          "Názov výletu",
	"subtitle":       "Krátky popis, napr. Predĺžený víkend s deťmi",
	"targetAudience": "Kto cestuje, napr. 2 dospelí + 2 deti (3 a 6 rokov)",
	"startLocation":  "Ubytovanie: oficiálny názov alebo adresa",
	"days": []schema{{
		"label": "Piatok",
		"activities": []schema{
			{"time": "09:00 - 11:30", "title": "Čo sa robí", "location": "Názov miesta pre ľudí", "place": "Oficiálny názov na mape", "badge": "Výlet"},
			{"time": "12:00", "title": "Obed", "location": "Reštaurácia", "place": "Ulica 1, Mesto"},
		},
	}},
	"checklist": []string{"Položka na zbalenie"},
}

func exportDocument(action string, itinerary any) schema {
	return schema{
		"format":       exportFormat,
		"version":      1,
		"action":       action,
		"instructions": exportInstructions,
		"itinerary":    itinerary,
		"schema":       itinerarySchema,
	}
}

func writeDownload(w http.ResponseWriter, filename string, doc schema) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write(append(data, '\n'))
}

func (a *API) exportItinerary(w http.ResponseWriter, r *http.Request) {
	it, ok := a.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	writeDownload(w, it.ID+".itinerary.json", exportDocument("update", it))
}

func (a *API) exportTemplate(w http.ResponseWriter, _ *http.Request) {
	writeDownload(w, "itinerary-template.json", exportDocument("create", templateItinerary))
}

// ImportResult describes what an import does (dry run) or did.
type ImportResult struct {
	Action        string     `json:"action"` // create or update
	ID            string     `json:"id"`
	PreviousTitle string     `json:"previousTitle,omitempty"`
	Changes       []string   `json:"changes,omitempty"` // top-level fields an update changes
	Itinerary     *Itinerary `json:"itinerary"`
	Saved         bool       `json:"saved"`
}

// extractJSON finds the JSON object in what an assistant answered: a bare
// file, a ```json fenced block, or an object surrounded by prose.
func extractJSON(text string) (string, error) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "\ufeff")
	if i := strings.Index(text, "```"); i >= 0 && !strings.HasPrefix(text, "{") {
		block := text[i+3:]
		if nl := strings.IndexByte(block, '\n'); nl >= 0 {
			block = block[nl+1:] // drop the language tag
		}
		if end := strings.Index(block, "```"); end >= 0 {
			text = block[:end]
		}
	}
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return "", invalid("no JSON object found; paste the whole file the assistant returned")
	}
	return text[start : end+1], nil
}

// importItinerary creates or fully replaces an itinerary from an export file
// or a bare itinerary object. Without an action, an existing id means update.
func (a *API) importItinerary(text string, dryRun bool) (*ImportResult, error) {
	raw, err := extractJSON(text)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil, invalid("invalid JSON: %v", err)
	}
	action, body := "", json.RawMessage(raw)
	if inner, ok := top["itinerary"]; ok {
		var envelope struct {
			Format string `json:"format"`
			Action string `json:"action"`
		}
		json.Unmarshal([]byte(raw), &envelope)
		if envelope.Format != "" && envelope.Format != exportFormat {
			return nil, invalid("unknown format %q", envelope.Format)
		}
		action, body = envelope.Action, inner
	}
	it, err := decodeItinerary(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	it.ID = strings.TrimSpace(it.ID)
	existing, exists := (*Itinerary)(nil), false
	if it.ID != "" {
		existing, exists = a.store.Get(it.ID)
	}
	switch action {
	case "":
		action = "create"
		if exists {
			action = "update"
		}
	case "update":
		if it.ID == "" {
			return nil, invalid("update needs itinerary.id of an existing itinerary")
		}
		if !exists {
			return nil, invalid("itinerary %q does not exist; use action \"create\" to add it", it.ID)
		}
	case "create":
		if exists {
			return nil, fmt.Errorf("%w: %q; remove itinerary.id or use action \"update\"", ErrExists, it.ID)
		}
	default:
		return nil, invalid("action must be \"create\" or \"update\"")
	}

	res := &ImportResult{Action: action}
	if action == "update" {
		if err := it.Normalize(); err != nil {
			return nil, err
		}
		res.PreviousTitle, res.Changes = existing.Title, changedFields(existing, it)
		if !dryRun {
			if err := a.store.Replace(it); err != nil {
				return nil, err
			}
			if a.geo != nil {
				a.geo.Warm(it)
			}
		}
	} else if dryRun {
		if err := it.Normalize(); err != nil {
			return nil, err
		}
	} else if err := a.addItinerary(it); err != nil {
		return nil, err
	}
	res.ID, res.Itinerary, res.Saved = it.ID, it, !dryRun
	return res, nil
}

var itineraryFields = []string{"title", "subtitle", "duration", "targetAudience", "startLocation", "mapsUrl", "days", "checklist"}

func changedFields(before, after *Itinerary) []string {
	var b, c map[string]json.RawMessage
	x, _ := json.Marshal(before)
	y, _ := json.Marshal(after)
	json.Unmarshal(x, &b)
	json.Unmarshal(y, &c)
	changes := []string{}
	for _, f := range itineraryFields {
		if !bytes.Equal(b[f], c[f]) {
			changes = append(changes, f)
		}
	}
	return changes
}

// canWriteFromPage also lets a signed-in person import and share from the
// page; the Origin check keeps other sites from using their cookie.
func (a *API) canWriteFromPage(r *http.Request) bool {
	if a.authorized(r) {
		return true
	}
	if a.auth == nil || a.auth.sessionEmail(r) == "" {
		return false
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && origin.Host == r.Host
}

func (a *API) importHandler(w http.ResponseWriter, r *http.Request) {
	if !a.canWriteFromPage(r) {
		a.challenge(w)
		writeError(w, http.StatusUnauthorized, "sign in or send 'Authorization: Bearer <API_TOKEN>' to import")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, err)
		return
	}
	dryRun := r.URL.Query().Get("dryRun") == "true"
	res, err := a.importItinerary(string(data), dryRun)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if res.Saved && res.Action == "create" {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}
