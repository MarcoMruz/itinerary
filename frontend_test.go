package main

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

func TestFrontendModulesAreServedWithoutReadCredentials(t *testing.T) {
	srv, _ := newTestServer(t, "write")
	err := fs.WalkDir(staticFS, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		response := send(t, "GET", srv.URL+"/"+path, "")
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "javascript") {
			t.Errorf("module %s: status %d, type %q", path, response.StatusCode, response.Header.Get("Content-Type"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := do(t, "GET", srv.URL+"/static/missing.mjs", ""); status != http.StatusNotFound {
		t.Fatalf("missing module: %d", status)
	}
	if status, _ := do(t, "GET", srv.URL+"/static/data/itineraries.json", ""); status != http.StatusNotFound {
		t.Fatalf("static handler exposed private data: %d", status)
	}
}

func TestPageIncludesComponentsAndModuleEntry(t *testing.T) {
	srv, _ := newTestServer(t, "")
	status, body := do(t, "GET", srv.URL+"/", "")
	if status != http.StatusOK {
		t.Fatalf("page: %d %s", status, body)
	}
	for _, marker := range []string{`type="module" src="/static/bootstrap.mjs"`, `aria-label="Program"`, `aria-label="Checklist"`, `x-ref="nav"`, `x-ref="drawer"`, `x-ref="share"`, `x-ref="io"`, `aria-label="Rady od miestnych"`, `x-model="io.insider"`} {
		if !strings.Contains(string(body), marker) {
			t.Errorf("page missing %s", marker)
		}
	}
}
