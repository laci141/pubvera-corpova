package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// claimLengthSetup points CLI_BIN at a missing binary and disables the cache,
// so a claim that passes the length check reaches the CLI and gets 502
// "CLI failed", while a claim that fails it gets 400 before any CLI run.
func claimLengthSetup(t *testing.T) {
	t.Helper()
	t.Setenv("CLI_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	useCache(t, nil)
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body map[string]any) (int, string) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("X-LLM-Key")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.String()
}

func postClaim(t *testing.T, claim string) (int, string) {
	t.Helper()
	return postJSON(t, handleConsensus, "/api/consensus", map[string]any{"claim": claim, "limit": 10})
}

func postCompare(t *testing.T, c1, c2 string) (int, string) {
	t.Helper()
	return postJSON(t, handleCompare, "/api/compare", map[string]any{"claim1": c1, "claim2": c2, "limit": 10})
}

func wantNot400(t *testing.T, status int, body string) {
	t.Helper()
	if status == http.StatusBadRequest {
		t.Errorf("status = 400, want not 400 (claim is within the limit); body %s", body)
	}
}

func wantTooLong(t *testing.T, status int, body, name string) {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %.200s", status, body)
	}
	if !strings.Contains(body, name+" is too long") || !strings.Contains(body, "500") {
		t.Errorf("body = %.200s, want %q and \"500\"", body, name+" is too long")
	}
}

func TestClaimLengthConsensus500ASCIIAllowed(t *testing.T) {
	claimLengthSetup(t)
	status, body := postClaim(t, strings.Repeat("a", 500))
	wantNot400(t, status, body)
}

func TestClaimLengthConsensus501ASCIIRejected(t *testing.T) {
	claimLengthSetup(t)
	status, body := postClaim(t, strings.Repeat("a", 501))
	wantTooLong(t, status, body, "claim")
}

func TestClaimLengthConsensus500RunesAllowed(t *testing.T) {
	claimLengthSetup(t)
	s := strings.Repeat("á", 500)
	if len(s) != 1000 {
		t.Fatalf("setup: len = %d bytes, want 1000", len(s))
	}
	status, body := postClaim(t, s)
	wantNot400(t, status, body)
}

func TestClaimLengthConsensus501RunesRejected(t *testing.T) {
	claimLengthSetup(t)
	status, body := postClaim(t, strings.Repeat("á", 501))
	wantTooLong(t, status, body, "claim")
}

// The limit applies to the normalized claim: whitespace runs collapse first.
func TestClaimLengthCountsAfterNormalize(t *testing.T) {
	claimLengthSetup(t)
	words := make([]string, 100)
	for i := range words {
		words[i] = "abcd"
	}
	s := strings.Join(words, "     ")
	raw, norm := utf8.RuneCountInString(s), utf8.RuneCountInString(normalizeClaim(s))
	if raw <= 500 || norm > 500 {
		t.Fatalf("setup: raw = %d (want > 500), normalized = %d (want <= 500)", raw, norm)
	}
	status, body := postClaim(t, s)
	wantNot400(t, status, body)
}

func TestClaimLengthCompareClaim2Rejected(t *testing.T) {
	claimLengthSetup(t)
	status, body := postCompare(t, "coffee reduces mortality", strings.Repeat("a", 501))
	wantTooLong(t, status, body, "claim2")
}

func TestClaimLengthCompareClaim1Rejected(t *testing.T) {
	claimLengthSetup(t)
	status, body := postCompare(t, strings.Repeat("a", 501), "coffee reduces mortality")
	wantTooLong(t, status, body, "claim1")
}

// A claim over the Linux single-argv limit (128 KiB) must be refused with 400,
// never reach exec and leak the server's binary path in a 502.
func TestClaimLengthHugeClaimNever502(t *testing.T) {
	claimLengthSetup(t)
	status, body := postClaim(t, strings.Repeat("a", 200000))
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %.200s", status, body)
	}
	for _, leak := range []string{"fork/exec", "argument list too long"} {
		if strings.Contains(body, leak) {
			t.Errorf("body contains %q: %.200s", leak, body)
		}
	}
}
