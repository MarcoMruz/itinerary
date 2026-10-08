package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
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
	publicURL string // optional absolute base URL for the OpenAPI document
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
	return &it, nil
}

func (a *API) authorized(r *http.Request) bool {
	if a.token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+a.token)) == 1
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

	secured := func(op schema) schema { return op }
	components := schema{
		"schemas": schema{
			"Summary": schema{"type": "object", "properties": schema{
				"id": schema{"type": "string"}, "title": schema{"type": "string"}, "subtitle": schema{"type": "string"},
				"duration": schema{"type": "string"}, "targetAudience": schema{"type": "string"},
			}},
			"Itinerary": itinerarySchema,
			"Error":     schema{"type": "object", "properties": schema{"error": schema{"type": "string"}}},
		},
	}
	if a.token != "" {
		components["securitySchemes"] = schema{"bearerAuth": schema{"type": "http", "scheme": "bearer"}}
		secured = func(op schema) schema {
			op["security"] = []schema{{"bearerAuth": []string{}}}
			return op
		}
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
			"/api/v1/itineraries": schema{
				"get": schema{
					"operationId": "listItineraries", "summary": "List all itineraries",
					"responses": schema{"200": resp("Itinerary summaries", schema{"type": "array", "items": ref("Summary")})},
				},
				"post": secured(schema{
					"operationId": "createItinerary", "summary": "Create an itinerary",
					"requestBody": schema{"required": true, "content": jsonBody(ref("Itinerary"))},
					"responses": schema{
						"201": resp("Created", ref("Itinerary")), "400": errResp("Validation error"),
						"401": errResp("Unauthorized"), "409": errResp("Id already exists"),
					},
				}),
			},
			"/api/v1/itineraries/{id}": schema{
				"get": schema{
					"operationId": "getItinerary", "summary": "Get itinerary detail incl. days and checklist", "parameters": idParam,
					"responses": schema{"200": resp("Itinerary", ref("Itinerary")), "404": errResp("Not found")},
				},
				"delete": secured(schema{
					"operationId": "deleteItinerary", "summary": "Delete an itinerary", "parameters": idParam,
					"responses": schema{"204": resp("Deleted", nil), "401": errResp("Unauthorized"), "404": errResp("Not found")},
				}),
			},
		},
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *API) baseURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---------- MCP (JSON-RPC 2.0 over Streamable HTTP, JSON responses) ----------

func (a *API) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", Error: &rpcError{rpcParseError, "parse error: " + err.Error()}})
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
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpVersions[len(mcpVersions)-1]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return schema{
			"protocolVersion": version,
			"capabilities":    schema{"tools": schema{"listChanged": false}},
			"serverInfo":      schema{"name": appName, "version": appVersion},
			"instructions":    "Family travel itineraries. Use list_itineraries to discover ids, get_itinerary_detail for days/activities/checklist, add_itinerary to create one.",
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
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{rpcInvalidParams, "invalid params: " + err.Error()}
	}
	switch p.Name {
	case "list_itineraries":
		return toolJSON(a.store.List()), nil
	case "get_itinerary_detail":
		var args struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(p.Arguments, &args)
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
