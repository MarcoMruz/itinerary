package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// Owner keys live outside itineraries so public reads and exports cannot expose them.
func (s *Store) ownerKey(id string, create bool) (string, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, ok := s.Get(id); !ok {
		return "", ErrNotFound
	}
	path := filepath.Join(filepath.Dir(s.path), "owner-keys.json")
	keys := map[string]string{}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &keys)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if keys == nil {
		return "", errors.New("invalid owner key file")
	}
	if keys[id] != "" || !create {
		return keys[id], nil
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	keys[id] = hex.EncodeToString(random[:])
	data, err = json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomicMode(path, data, 0o600); err != nil {
		return "", err
	}
	return keys[id], nil
}

func (a *API) ownerLink(w http.ResponseWriter, r *http.Request) {
	key, err := a.store.ownerKey(r.PathValue("id"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, schema{"url": a.baseURL(r) + "/#" + r.PathValue("id") + "~" + key})
}

func (a *API) ownerSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Key string `json:"key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := dec.Decode(&input); err != nil {
		writeError(w, 400, "invalid key")
		return
	}
	key, err := a.store.ownerKey(r.PathValue("id"), false)
	if err != nil || key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(input.Key)) != 1 {
		writeError(w, 401, "invalid owner link")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "itinerary_owner", Value: key, Path: "/api/v1/itineraries/" + r.PathValue("id") + "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 60 * 60})
	writeJSON(w, 200, schema{"canEdit": true})
}

func (a *API) hasOwnerSession(r *http.Request) bool {
	cookie, err := r.Cookie("itinerary_owner")
	if err != nil {
		return false
	}
	key, err := a.store.ownerKey(r.PathValue("id"), false)
	return err == nil && key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(cookie.Value)) == 1
}

func (a *API) checklistAccess(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, schema{"canEdit": a.hasOwnerSession(r)})
}

func (a *API) shareChecklist(w http.ResponseWriter, r *http.Request) {
	if !a.hasOwnerSession(r) {
		writeError(w, http.StatusUnauthorized, "owner link required")
		return
	}
	key, err := a.store.ownerKey(r.PathValue("id"), false)
	if err != nil || key == "" {
		writeError(w, http.StatusUnauthorized, "owner link required")
		return
	}
	writeJSON(w, http.StatusOK, schema{"url": a.baseURL(r) + "/#" + r.PathValue("id") + "~" + key})
}

func (a *API) editChecklist(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeError(w, 403, "cross-origin edits are not allowed")
			return
		}
	}
	if !a.hasOwnerSession(r) {
		writeError(w, 401, "owner link required")
		return
	}
	var input struct {
		Checklist *[]string `json:"checklist"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeError(w, 400, "invalid checklist")
		return
	}
	if err := requireJSONEOF(dec); err != nil || input.Checklist == nil {
		writeError(w, 400, "checklist is required")
		return
	}
	value, _ := json.Marshal(*input.Checklist)
	it, err := a.updateItinerary(r.PathValue("id"), map[string]json.RawMessage{"checklist": value})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, it)
}
