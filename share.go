package main

import (
	"crypto/subtle"
	"net/http"
)

// Read-only share links open one itinerary without signing in, e.g. for
// grandparents who are not on ALLOWED_EMAILS. The link carries a random key
// kept in share-keys.json; revoking it (or making a new one) kills old links.

const shareCookie = "itinerary_share"

func (a *API) shareURL(r *http.Request, id, key string) string {
	return a.baseURL(r) + "/s/" + id + "/" + key
}

func (a *API) validShareKey(id, got string) bool {
	key, err := a.store.shareKey(id, false)
	return err == nil && key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(got)) == 1
}

// hasShareAccess lets a shared page read its own itinerary's API (day legs,
// export), never the list or other itineraries.
func (a *API) hasShareAccess(r *http.Request) bool {
	c, err := r.Cookie(shareCookie)
	return err == nil && r.PathValue("id") != "" && a.validShareKey(r.PathValue("id"), c.Value)
}

func (a *API) createShareLink(w http.ResponseWriter, r *http.Request) {
	if !a.canWriteFromPage(r) {
		a.challenge(w)
		writeError(w, http.StatusUnauthorized, "sign in or send 'Authorization: Bearer <API_TOKEN>' to share")
		return
	}
	key, err := a.store.shareKey(r.PathValue("id"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, schema{"url": a.shareURL(r, r.PathValue("id"), key)})
}

func (a *API) revokeShareLink(w http.ResponseWriter, r *http.Request) {
	if !a.canWriteFromPage(r) {
		a.challenge(w)
		writeError(w, http.StatusUnauthorized, "sign in or send 'Authorization: Bearer <API_TOKEN>' to revoke")
		return
	}
	if err := a.store.revokeShareKey(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setShareCookie keeps the shared page's API calls working after the URL is opened.
func (a *API) setShareCookie(w http.ResponseWriter, r *http.Request, id, key string) {
	http.SetCookie(w, &http.Cookie{
		Name: shareCookie, Value: key, Path: "/api/v1/itineraries/" + id + "/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 60 * 60,
	})
}
