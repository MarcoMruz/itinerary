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

// Owner and share keys live outside itineraries so public reads and exports cannot expose them.
func (s *Store) ownerKey(id string, create bool) (string, error) {
	return s.itineraryKey("owner-keys.json", id, create, false)
}

// shareKey guards the read-only link of one itinerary.
func (s *Store) shareKey(id string, create bool) (string, error) {
	return s.itineraryKey("share-keys.json", id, create, false)
}

// revokeShareKey removes the key: the link and cookies issued for it stop working.
func (s *Store) revokeShareKey(id string) error {
	_, err := s.itineraryKey("share-keys.json", id, false, true)
	return err
}

func (s *Store) itineraryKey(file, id string, create, remove bool) (string, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	_, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	path := filepath.Join(filepath.Dir(s.path), file)
	keys := map[string]string{}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &keys)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if keys == nil {
		return "", errors.New("invalid key file " + file)
	}
	switch {
	case remove:
		if keys[id] == "" {
			return "", nil
		}
		delete(keys, id)
	case keys[id] != "" || !create:
		return keys[id], nil
	default:
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		keys[id] = hex.EncodeToString(random[:])
	}
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
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 1024), &input); err != nil {
		writeErr(w, err)
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

// ownerSessionKey grants checklist edits to a signed-in account or a browser that opened the owner link.
func (a *API) ownerSessionKey(r *http.Request) string {
	if a.auth != nil && a.auth.sessionEmail(r) != "" {
		key, _ := a.store.ownerKey(r.PathValue("id"), true)
		return key
	}
	cookie, err := r.Cookie("itinerary_owner")
	if err != nil {
		return ""
	}
	key, err := a.store.ownerKey(r.PathValue("id"), false)
	if err != nil || key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(cookie.Value)) != 1 {
		return ""
	}
	return key
}

func (a *API) checklistAccess(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, schema{"canEdit": a.ownerSessionKey(r) != ""})
}

func (a *API) shareChecklist(w http.ResponseWriter, r *http.Request) {
	key := a.ownerSessionKey(r)
	if key == "" {
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
	if a.ownerSessionKey(r) == "" {
		writeError(w, 401, "owner link required")
		return
	}
	var input struct {
		Checklist *[]string `json:"checklist"`
	}
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, maxBodyBytes), &input); err != nil {
		writeErr(w, err)
		return
	}
	if input.Checklist == nil {
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
