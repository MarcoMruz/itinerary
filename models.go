package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ---------- Domain ----------

// Activity is one timeline entry within a day.
type Activity struct {
	Time     string `json:"time"`
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	Place    string `json:"place,omitempty"` // geocodable stop; activities with a place form the day route
	MapsURL  string `json:"mapsUrl,omitempty"`
	Badge    string `json:"badge,omitempty"`
}

// Day groups activities under a label such as "Piatok" or "Deň 1".
type Day struct {
	Label      string     `json:"label"`
	Activities []Activity `json:"activities"`
}

// RouteStops lists the day's route: activity places with consecutive
// duplicates merged. The UI numbers stops the same way (dayRoute).
func (d Day) RouteStops() []string {
	var stops []string
	for _, a := range d.Activities {
		if a.Place != "" && (len(stops) == 0 || stops[len(stops)-1] != a.Place) {
			stops = append(stops, a.Place)
		}
	}
	return stops
}

// Itinerary is the aggregate root. Once stored it is treated as immutable,
// so the store can hand out pointers to concurrent readers safely.
type Itinerary struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Subtitle       string   `json:"subtitle,omitempty"`
	Duration       string   `json:"duration,omitempty"`
	TargetAudience string   `json:"targetAudience,omitempty"`
	StartLocation  string   `json:"startLocation,omitempty"` // default origin of every day route, e.g. the hotel
	MapsURL        string   `json:"mapsUrl,omitempty"`
	Days           []Day    `json:"days"`
	Checklist      []string `json:"checklist"`
}

// Summary is the list projection of an itinerary.
type Summary struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle,omitempty"`
	Duration       string `json:"duration,omitempty"`
	TargetAudience string `json:"targetAudience,omitempty"`
}

func (it *Itinerary) Summary() Summary {
	return Summary{it.ID, it.Title, it.Subtitle, it.Duration, it.TargetAudience}
}

// ---------- Validation ----------

const (
	maxDays         = 60
	maxActivities   = 50
	maxChecklist    = 200
	maxTextRunes    = 500
	maxSlugBaseLen  = 48
	defaultSlugBase = "itinerar"
)

var idPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// ValidationError marks client-side input errors (HTTP 400 / MCP isError).
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

func invalid(format string, a ...any) error { return ValidationError(fmt.Sprintf(format, a...)) }

// Normalize trims and validates the itinerary in place, filling derived
// defaults (id from title, day labels, duration). Untrusted URLs are limited
// to http(s) so they are safe to render as links.
func (it *Itinerary) Normalize() error {
	for _, f := range []struct {
		name string
		v    *string
	}{{"id", &it.ID}, {"title", &it.Title}, {"subtitle", &it.Subtitle}, {"duration", &it.Duration},
		{"targetAudience", &it.TargetAudience}, {"startLocation", &it.StartLocation}, {"mapsUrl", &it.MapsURL}} {
		if err := cleanText(f.name, f.v); err != nil {
			return err
		}
	}
	if it.Title == "" {
		return invalid("title is required")
	}
	if it.ID == "" {
		it.ID = slugify(it.Title)
	}
	if !idPattern.MatchString(it.ID) {
		return invalid("id %q must be a lowercase slug (a-z, 0-9, '-'), max 64 chars", it.ID)
	}
	if err := checkURL("mapsUrl", it.MapsURL); err != nil {
		return err
	}
	if len(it.Days) == 0 || len(it.Days) > maxDays {
		return invalid("days must contain 1-%d items", maxDays)
	}
	for i := range it.Days {
		if err := it.Days[i].normalize(i); err != nil {
			return err
		}
	}
	if len(it.Checklist) > maxChecklist {
		return invalid("checklist may contain at most %d items", maxChecklist)
	}
	items := make([]string, 0, len(it.Checklist))
	for i := range it.Checklist {
		if err := cleanText(fmt.Sprintf("checklist[%d]", i), &it.Checklist[i]); err != nil {
			return err
		}
		if it.Checklist[i] != "" {
			items = append(items, it.Checklist[i])
		}
	}
	it.Checklist = items
	if it.Duration == "" {
		it.Duration = dayCount(len(it.Days))
	}
	return nil
}

func (d *Day) normalize(i int) error {
	field := fmt.Sprintf("days[%d]", i)
	if err := cleanText(field+".label", &d.Label); err != nil {
		return err
	}
	if d.Label == "" {
		d.Label = fmt.Sprintf("Deň %d", i+1)
	}
	if len(d.Activities) > maxActivities {
		return invalid("%s.activities may contain at most %d items", field, maxActivities)
	}
	if d.Activities == nil {
		d.Activities = []Activity{}
	}
	for j := range d.Activities {
		a, af := &d.Activities[j], fmt.Sprintf("%s.activities[%d]", field, j)
		for _, f := range []struct {
			name string
			v    *string
		}{{"time", &a.Time}, {"title", &a.Title}, {"location", &a.Location}, {"place", &a.Place}, {"mapsUrl", &a.MapsURL}, {"badge", &a.Badge}} {
			if err := cleanText(af+"."+f.name, f.v); err != nil {
				return err
			}
		}
		if a.Title == "" {
			return invalid("%s.title is required", af)
		}
		if err := checkURL(af+".mapsUrl", a.MapsURL); err != nil {
			return err
		}
		if a.MapsURL == "" && a.Place != "" {
			a.MapsURL = mapsSearchURL(a.Place)
		}
	}
	return nil
}

func mapsSearchURL(place string) string {
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(place)
}

func cleanText(field string, s *string) error {
	*s = strings.TrimSpace(*s)
	if utf8.RuneCountInString(*s) > maxTextRunes {
		return invalid("%s exceeds %d characters", field, maxTextRunes)
	}
	return nil
}

func checkURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s must be an absolute http(s) URL", field)
	}
	return nil
}

var diacritics = strings.NewReplacer(
	"á", "a", "ä", "a", "č", "c", "ď", "d", "é", "e", "ě", "e", "í", "i", "ĺ", "l", "ľ", "l",
	"ň", "n", "ó", "o", "ô", "o", "ö", "o", "ŕ", "r", "ř", "r", "š", "s", "ť", "t", "ú", "u",
	"ů", "u", "ü", "u", "ý", "y", "ž", "z",
)

// slugify turns a (Slovak/Czech) title into a URL-safe id base.
func slugify(s string) string {
	s = diacritics.Replace(strings.ToLower(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	if len(out) > maxSlugBaseLen {
		out = out[:maxSlugBaseLen]
	}
	if out = strings.Trim(out, "-"); out == "" {
		return defaultSlugBase
	}
	return out
}

// dayCount renders a Slovak day count with correct plural form.
func dayCount(n int) string {
	switch {
	case n == 1:
		return "1 deň"
	case n >= 2 && n <= 4:
		return fmt.Sprintf("%d dni", n)
	default:
		return fmt.Sprintf("%d dní", n)
	}
}

// ---------- JSON Schema (shared by OpenAPI and MCP) ----------

type schema = map[string]any

func strProp(desc string) schema { return schema{"type": "string", "description": desc} }

var activitySchema = schema{
	"type":     "object",
	"required": []string{"time", "title"},
	"properties": schema{
		"time":     strProp("Time window, e.g. '09:00 - 11:30' or '15:00+'"),
		"title":    strProp("What happens"),
		"location": strProp("Human readable place name"),
		"place":    strProp("Place the stop is at, findable on both Google Maps and OpenStreetMap: the official name as shown on the map (add the town only if the name alone is ambiguous, e.g. 'Stratenský kaňon, Stratená') or a street address. No descriptive extras like 'parking' or 'by the lake'; unknown names get no driving distance. Activities with a place become numbered stops of the day route; mapsUrl is derived from it when omitted"),
		"mapsUrl":  strProp("Absolute http(s) Google Maps URL"),
		"badge":    strProp("Short tag shown as a badge, e.g. 'Relax'"),
	},
}

// itinerarySchema describes the input accepted by POST /api/v1/itineraries
// and the MCP add_itinerary tool.
var itinerarySchema = schema{
	"type":     "object",
	"required": []string{"title", "days"},
	"properties": schema{
		"id":             schema{"type": "string", "pattern": idPattern.String(), "description": "Optional slug; derived from title when omitted"},
		"title":          strProp("Itinerary title"),
		"subtitle":       strProp("Short tagline"),
		"duration":       strProp("Human readable length; derived from day count when omitted"),
		"targetAudience": strProp("Who the trip is for, e.g. '2 adults + 1-year-old'"),
		"startLocation":  strProp("Optional origin of every day route, e.g. the hotel you stay at, given as its official name or street address (same rules as activity place); empty = traveller's current location"),
		"mapsUrl":        strProp("Absolute http(s) Google Maps route link for the whole trip"),
		"days": schema{
			"type": "array", "minItems": 1, "maxItems": maxDays,
			"items": schema{
				"type":     "object",
				"required": []string{"activities"},
				"properties": schema{
					"label":      strProp("Day label, e.g. 'Piatok'; defaults to 'Deň N'"),
					"activities": schema{"type": "array", "maxItems": maxActivities, "items": activitySchema},
				},
			},
		},
		"checklist": schema{"type": "array", "maxItems": maxChecklist, "items": schema{"type": "string"}, "description": "Packing / preparation items"},
	},
}

var itineraryUpdateSchema = func() schema {
	properties := schema{}
	for key, value := range itinerarySchema["properties"].(schema) {
		if key != "id" {
			properties[key] = value
		}
	}
	return schema{
		"type": "object", "minProperties": 1, "additionalProperties": false,
		"description": "Fields to change. Omitted fields stay unchanged; arrays are replaced in full. Use an empty checklist to clear it, or empty strings to clear optional text. Null is not accepted.",
		"properties":  properties,
	}
}()

// ---------- MCP (JSON-RPC 2.0) ----------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema schema `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

var mcpTools = []mcpTool{
	{
		Name:        "update_itinerary",
		Description: "Edit an existing itinerary. Omitted fields stay unchanged; supplied arrays replace existing arrays. Returns the updated itinerary. Requires write access.",
		InputSchema: schema{"type": "object", "required": []string{"id", "changes"}, "additionalProperties": false, "properties": schema{
			"id":      strProp("Existing itinerary id from list_itineraries; cannot be changed"),
			"changes": itineraryUpdateSchema,
		}},
	},
	{
		Name:        "list_itineraries",
		Description: "List all family travel itineraries (id, title, subtitle, duration, targetAudience).",
		InputSchema: schema{"type": "object", "properties": schema{}},
	},
	{
		Name:        "get_itinerary_detail",
		Description: "Get one itinerary with all days, activities and its checklist.",
		InputSchema: schema{"type": "object", "required": []string{"id"}, "properties": schema{"id": strProp("Itinerary id from list_itineraries")}},
	},
	{
		Name:        "add_itinerary",
		Description: "Create a new itinerary. Returns the stored itinerary including its final id.",
		InputSchema: itinerarySchema,
	},
}
