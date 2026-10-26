package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /version reports the CalVer build version the release workflow stamps into
// the binary with -ldflags "-X main.appVersion=...". An unstamped build says
// "dev".
//
// main() builds its mux inline and registers handleRoot as the "/" catch-all,
// so, as in root_cache_test.go, handleRoot is the route table for /version. No
// CLI binary is needed and no Authorization header is sent.

func requestVersion(t *testing.T, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleRoot(rec, httptest.NewRequest(method, "/version", nil))
	return rec
}

func TestVersionDefaultDev(t *testing.T) {
	rec := requestVersion(t, http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no auth header sent)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body["version"] != "dev" || len(body) != 1 {
		t.Errorf("body = %v, want {\"version\":\"dev\"}", body)
	}
}

func TestVersionStamped(t *testing.T) {
	old := appVersion
	appVersion = "2026.10.26.1"
	t.Cleanup(func() { appVersion = old })
	rec := requestVersion(t, http.MethodGet)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body["version"] != "2026.10.26.1" {
		t.Errorf("version = %q, want 2026.10.26.1", body["version"])
	}
}

func TestVersionHead(t *testing.T) {
	rec := requestVersion(t, http.MethodHead)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("HEAD Cache-Control = %q, want no-store", cc)
	}
}

func TestVersionRejectsPost(t *testing.T) {
	rec := requestVersion(t, http.MethodPost)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want \"GET, HEAD\"", allow)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == nil || body["error"] == "" {
		t.Errorf("body %q is not a JSON error", rec.Body.String())
	}
}
