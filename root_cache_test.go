package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The index page must be revalidated on every load, so a deploy is visible
// immediately instead of whenever a browser's heuristic cache expires. Without
// a Cache-Control header neither Caddy nor Cloudflare adds one.
//
// main() builds its mux inline and registers handleRoot as the "/" catch-all,
// so handleRoot is the route table for every path tested here.

func getRoot(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleRoot(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndexIsRevalidatedOnEveryLoad(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		rec := getRoot(t, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want %q", path, cc, "no-cache")
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: Content-Type = %q, want text/html", path, ct)
		}
	}
}

func TestConfigStaysNoStore(t *testing.T) {
	rec := getRoot(t, "/config.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /config.json: status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("GET /config.json: Cache-Control = %q, want %q", cc, "no-store")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	if rec := getRoot(t, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope: status = %d, want 404", rec.Code)
	}
}

// The health answer is not the page: neither /healthz nor the "/" fallthrough
// (index.html unreadable) may pick up the index page's no-cache.
func TestHealthzIsNotTheIndexPage(t *testing.T) {
	check := func(t *testing.T, path string) {
		t.Helper()
		rec := getRoot(t, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
		if got := rec.Body.String(); got != "ok" {
			t.Errorf("GET %s: body = %q, want %q", path, got, "ok")
		}
		if cc := rec.Header().Get("Cache-Control"); cc == "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want anything but %q", path, cc, "no-cache")
		}
	}
	t.Run("healthz", func(t *testing.T) { check(t, "/healthz") })
	t.Run("root fallthrough", func(t *testing.T) {
		t.Chdir(t.TempDir()) // no index.html to read here
		check(t, "/")
	})
}
