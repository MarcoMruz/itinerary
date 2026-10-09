package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	appName        = "family-itinerary"
	appVersion     = "1.0.0"
	maxBodyBytes   = 1 << 20
	maxIDSuffixTry = 100
)

// MCP protocol revisions this server speaks; the last one is preferred.
var mcpVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// API serves the REST and MCP endpoints on top of a Store.
type API struct {
	store     *Store
	token     string // optional bearer token guarding writes
	readToken string // optional bearer token guarding reads and MCP
	publicURL string // optional absolute base URL for the OpenAPI document
	geo       *Geo   // nil disables distances
	// startLimiter caps uncached start lookups per client, so one visitor
	// cannot occupy the shared, rate-limited geocoder.
	startLimiter *rateLimiter
	startGlobal  *rateLimiter // all clients together, so stops keep at least half of the ~55 calls/min
	ipHeader     string
}

// ---------- Shared use cases ----------

// addItinerary validates and stores it. When the client did not choose an id,
// the slug derived from the title gets a numeric suffix on collision.
func (a *API) addItinerary(it *Itinerary) error {
	derived := strings.TrimSpace(it.ID) == ""
	if err := it.Normalize(); err != nil {
		return err
	}
	base := it.ID
	for n := 2; ; n++ {
		err := a.store.Create(it)
		if err == nil && a.geo != nil {
			go a.geo.Warm(it)
		}
		if !derived || !errors.Is(err, ErrExists) || n > maxIDSuffixTry {
			return err
		}
		it.ID = fmt.Sprintf("%s-%d", base, n)
	}
}

func decodeItinerary(r io.Reader) (*Itinerary, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields() // surface agent typos ("name" vs "title") instead of dropping data
	var it Itinerary
	if err := dec.Decode(&it); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, err
		}
		return nil, invalid("invalid JSON: %v", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return nil, invalid("invalid JSON: %v", err)
	}
	return &it, nil
}

func (a *API) updateItinerary(id string, changes map[string]json.RawMessage) (*Itinerary, error) {
	if !idPattern.MatchString(id) {
		return nil, invalid("valid itinerary id is required")
	}
	it, err := a.store.Update(id, changes)
	if err == nil && a.geo != nil {
		if _, changed := changes["days"]; changed {
			go a.geo.Warm(it)
		}
	}
	return it, err
}

func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func bearerMatches(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	got, want := sha256.Sum256([]byte(r.Header.Get("Authorization"))), sha256.Sum256([]byte("Bearer "+token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// authorized fails closed: with only READ_API_TOKEN set, writes are disabled.
func (a *API) authorized(r *http.Request) bool {
	return !a.readsProtected() || bearerMatches(r, a.token)
}

// canRead accepts either token once any token is configured; withSession also accepts the UI cookie.
func (a *API) canRead(r *http.Request, withSession bool) bool {
	if !a.readsProtected() {
		return true
	}
	return bearerMatches(r, a.token) || bearerMatches(r, a.readToken) || (withSession && a.validSession(r))
}

func (a *API) readsProtected() bool { return a.token != "" || a.readToken != "" }

func (a *API) sessionValue() string { return sessionMAC(a.token + "\x00" + a.readToken) }

func (a *API) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && hmac.Equal([]byte(c.Value), []byte(a.sessionValue()))
}

// setSession gives the web UI read access to the API without exposing a token to JS.
func (a *API) setSession(w http.ResponseWriter, r *http.Request) {
	if !a.readsProtected() {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.sessionValue(),
		Path:     "/api/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (a *API) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}

func (a *API) requireReadToken(next http.HandlerFunc) http.HandlerFunc {
	return a.requireRead(false, next)
}

// requireUIRead also accepts the web UI session cookie.
func (a *API) requireUIRead(next http.HandlerFunc) http.HandlerFunc { return a.requireRead(true, next) }

func (a *API) requireRead(withSession bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.canRead(r, withSession) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}

// ---------- REST ----------

func (a *API) listItineraries(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.store.List())
}

func (a *API) getItinerary(w http.ResponseWriter, r *http.Request) {
	it, ok := a.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (a *API) createItinerary(w http.ResponseWriter, r *http.Request) {
	it, err := decodeItinerary(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err == nil {
		err = a.addItinerary(it)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/itineraries/"+it.ID)
	writeJSON(w, http.StatusCreated, it)
}

func (a *API) deleteItinerary(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) patchItinerary(w http.ResponseWriter, r *http.Request) {
	var changes map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	err := dec.Decode(&changes)
	if err == nil {
		err = requireJSONEOF(dec)
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if !errors.As(err, &tooBig) {
			err = invalid("invalid JSON: %v", err)
		}
		writeErr(w, err)
		return
	}
	it, err := a.updateItinerary(r.PathValue("id"), changes)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

const startLookupsGlobalPerMinute = 30

const legsTimeout = 20 * time.Second // below the server WriteTimeout; unfinished legs come back null with complete=false

// getDayLegs returns driving distances along one day route of a stored
// itinerary, from the optional ?start= (e.g. the hotel) through its stops.
// Stops come from the itinerary, so the only free text a visitor sends is the start.
func (a *API) getDayLegs(w http.ResponseWriter, r *http.Request) {
	if a.geo == nil {
		writeError(w, http.StatusNotFound, "distances are disabled")
		return
	}
	it, ok := a.store.Get(r.PathValue("id"))
	day, err := strconv.Atoi(r.PathValue("day"))
	if !ok || err != nil || day < 0 || day >= len(it.Days) {
		writeError(w, http.StatusNotFound, "itinerary day not found")
		return
	}
	start := r.URL.Query().Get("start")
	if err := cleanText("start", &start); err != nil {
		writeErr(w, err)
		return
	}
	stops := it.Days[day].RouteStops()
	if start != "" && len(stops) > 0 && a.startLimiter != nil {
		// Charge every uncached call the start causes, so a known start cannot fan out across days.
		ip := clientIP(r, a.ipHeader)
		for range a.geo.StartCost(start, stops[0]) {
			ok, wait := a.startLimiter.allow(ip, time.Now())
			if ok && a.startGlobal != nil {
				ok, wait = a.startGlobal.allow("*", time.Now())
			}
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				writeError(w, http.StatusTooManyRequests, "too many new start lookups, try again later")
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), legsTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, a.geo.Route(ctx, start, stops))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeErr(w http.ResponseWriter, err error) {
	var ve ValidationError
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		log.Printf("internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// ---------- OpenAPI ----------

func (a *API) openAPI(w http.ResponseWriter, r *http.Request) {
	ref := func(name string) schema { return schema{"$ref": "#/components/schemas/" + name} }
	jsonBody := func(s schema) schema { return schema{"application/json": schema{"schema": s}} }
	resp := func(desc string, s schema) schema {
		if s == nil {
			return schema{"description": desc}
		}
		return schema{"description": desc, "content": jsonBody(s)}
	}
	errResp := func(desc string) schema { return resp(desc, ref("Error")) }
	idParam := []schema{{"name": "id", "in": "path", "required": true, "schema": schema{"type": "string"}}}

	withBearer := func(op schema) schema {
		op["security"] = []schema{{"bearerAuth": []string{}}}
		op["responses"].(schema)["401"] = errResp("Unauthorized")
		return op
	}
	secured, readSecured := func(op schema) schema { return op }, func(op schema) schema { return op }
	components := schema{
		"schemas": schema{
			"Summary": schema{"type": "object", "properties": schema{
				"id": schema{"type": "string"}, "title": schema{"type": "string"}, "subtitle": schema{"type": "string"},
				"duration": schema{"type": "string"}, "targetAudience": schema{"type": "string"},
			}},
			"Itinerary":       itinerarySchema,
			"ItineraryUpdate": itineraryUpdateSchema,
			"Error":           schema{"type": "object", "properties": schema{"error": schema{"type": "string"}}},
		},
	}
	if a.readsProtected() {
		components["securitySchemes"] = schema{"bearerAuth": schema{"type": "http", "scheme": "bearer"}}
		readSecured = withBearer
	}
	if a.readsProtected() {
		secured = withBearer
	}

	doc := schema{
		"openapi": "3.1.0",
		"info": schema{
			"title":       "Family Itinerary API",
			"version":     appVersion,
			"description": "Manage family travel itineraries. An MCP (JSON-RPC 2.0, Streamable HTTP) endpoint is also available at POST /mcp.",
		},
		"servers":    []schema{{"url": a.baseURL(r)}},
		"components": components,
		"paths": schema{
			"/api/v1/itineraries/{id}/owner-link": schema{
				"post": secured(schema{
					"operationId": "getOwnerLink", "summary": "Get or create a private checklist edit link", "parameters": idParam,
					"responses": schema{"200": resp("Private owner link; keep it secret", schema{"type": "object", "properties": schema{"url": strProp("Private edit URL")}}), "404": errResp("Not found")},
				}),
			},
			"/api/v1/itineraries": schema{
				"get": readSecured(schema{
					"operationId": "listItineraries", "summary": "List all itineraries",
					"responses": schema{"200": resp("Itinerary summaries", schema{"type": "array", "items": ref("Summary")})},
				}),
				"post": secured(schema{
					"operationId": "createItinerary", "summary": "Create an itinerary",
					"requestBody": schema{"required": true, "content": jsonBody(ref("Itinerary"))},
					"responses": schema{
						"201": resp("Created", ref("Itinerary")), "400": errResp("Validation error"),
						"409": errResp("Id already exists"),
					},
				}),
			},
			"/api/v1/itineraries/{id}": schema{
				"patch": secured(schema{
					"operationId": "updateItinerary", "summary": "Edit supplied fields of an existing itinerary; arrays replace in full", "parameters": idParam,
					"requestBody": schema{"required": true, "content": jsonBody(ref("ItineraryUpdate"))},
					"responses":   schema{"200": resp("Updated itinerary", ref("Itinerary")), "400": errResp("Validation error"), "404": errResp("Not found"), "413": errResp("Request body too large")},
				}),
				"get": readSecured(schema{
					"operationId": "getItinerary", "summary": "Get itinerary detail incl. days and checklist", "parameters": idParam,
					"responses": schema{"200": resp("Itinerary", ref("Itinerary")), "404": errResp("Not found")},
				}),
				"delete": secured(schema{
					"operationId": "deleteItinerary", "summary": "Delete an itinerary", "parameters": idParam,
					"responses": schema{"204": resp("Deleted", nil), "404": errResp("Not found")},
				}),
			},
		},
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, doc)
}

func (a *API) baseURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------- MCP (JSON-RPC 2.0 over Streamable HTTP, JSON responses) ----------

func (a *API) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", Error: &rpcError{rpcParseError, "parse error: " + err.Error()}})
		return
	}
	if err := requireJSONEOF(dec); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{rpcInvalidRequest, "invalid JSON-RPC request: " + err.Error()}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{rpcInvalidRequest, "invalid JSON-RPC 2.0 request"}})
		return
	}
	if req.ID == nil { // notification (e.g. notifications/initialized): no response body
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := a.dispatch(r, req)
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (a *API) dispatch(r *http.Request, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    any                        `json:"capabilities"`
			ClientInfo      any                        `json:"clientInfo"`
			Meta            map[string]json.RawMessage `json:"_meta,omitempty"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, &rpcError{rpcInvalidParams, err.Error()}
		}
		version := mcpVersions[len(mcpVersions)-1]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return schema{
			"protocolVersion": version,
			"capabilities":    schema{"tools": schema{"listChanged": false}},
			"serverInfo":      schema{"name": appName, "version": appVersion},
			"instructions":    "Family travel itineraries. Use list_itineraries to discover ids, get_itinerary_detail for days/activities/checklist, add_itinerary to create one, and update_itinerary to edit supplied fields of an existing itinerary. Updates preserve omitted fields and replace supplied arrays in full. Give each activity place and the startLocation as an official map name or street address, so driving distances can be computed.",
		}, nil
	case "ping":
		return schema{}, nil
	case "tools/list":
		return schema{"tools": mcpTools}, nil
	case "tools/call":
		return a.callTool(r, req.Params)
	default:
		return nil, &rpcError{rpcMethodNotFound, "method not found: " + req.Method}
	}
}

func (a *API) callTool(r *http.Request, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string                     `json:"name"`
		Arguments json.RawMessage            `json:"arguments"`
		Meta      map[string]json.RawMessage `json:"_meta,omitempty"`
	}
	if err := decodeRaw(raw, &p); err != nil {
		return nil, &rpcError{rpcInvalidParams, "invalid params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{rpcInvalidParams, "tool name is required"}
	}
	switch p.Name {
	case "get_owner_link":
		if !a.authorized(r) {
			return toolError("unauthorized: get_owner_link requires write access"), nil
		}
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeRaw(p.Arguments, &args); err != nil {
			return nil, &rpcError{rpcInvalidParams, "invalid arguments: " + err.Error()}
		}
		key, err := a.store.ownerKey(args.ID, true)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolJSON(schema{"url": a.baseURL(r) + "/#" + args.ID + "~" + key}), nil
	case "update_itinerary":
		if !a.authorized(r) {
			return toolError("unauthorized: update_itinerary requires 'Authorization: Bearer <API_TOKEN>'"), nil
		}
		var args struct {
			ID      string                     `json:"id"`
			Changes map[string]json.RawMessage `json:"changes"`
		}
		if err := decodeRaw(p.Arguments, &args); err != nil {
			return nil, &rpcError{rpcInvalidParams, "invalid arguments: " + err.Error()}
		}
		it, err := a.updateItinerary(args.ID, args.Changes)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolJSON(it), nil
	case "list_itineraries":
		return toolJSON(a.store.List()), nil
	case "get_itinerary_detail":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeRaw(p.Arguments, &args); err != nil {
			return nil, &rpcError{rpcInvalidParams, "invalid arguments: " + err.Error()}
		}
		if args.ID == "" {
			return nil, &rpcError{rpcInvalidParams, "id is required"}
		}
		it, ok := a.store.Get(args.ID)
		if !ok {
			return toolError(fmt.Sprintf("itinerary %q not found; call list_itineraries for valid ids", args.ID)), nil
		}
		return toolJSON(it), nil
	case "add_itinerary":
		if !a.authorized(r) {
			return toolError("unauthorized: add_itinerary requires 'Authorization: Bearer <API_TOKEN>'"), nil
		}
		it, err := decodeItinerary(bytes.NewReader(p.Arguments))
		if err == nil {
			err = a.addItinerary(it)
		}
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolJSON(it), nil
	default:
		return nil, &rpcError{rpcInvalidParams, "unknown tool: " + p.Name}
	}
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return decodeRaw(raw, dst)
}

func decodeRaw(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return errors.New("object is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

func toolJSON(v any) mcpToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return toolError(err.Error())
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(b)}}}
}

func toolError(msg string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: msg}}, IsError: true}
}
