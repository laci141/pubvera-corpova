package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// COR-007b: the index page carries a fresh nonce in every <script> tag and a
// matching, enforced Content-Security-Policy header. Like root_cache_test.go,
// these call handleRoot directly (see getRoot there).

// wantPolicy is written out here rather than taken from contentSecurityPolicy,
// so a change to any directive in main.go fails a test.
func wantPolicy(nonce string) string {
	return "script-src 'nonce-" + nonce + "' 'strict-dynamic' https: 'unsafe-inline'; script-src-attr 'none'; style-src 'nonce-" + nonce + "'; style-src-attr 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
}

// nonceMarkerText is the literal the server replaces. It is spelled out here
// so a renamed constant in main.go cannot silently agree with itself.
const nonceMarkerText = "__CSP_NONCE__"

// 16 random bytes in raw standard base64 are exactly 22 characters.
var cspNonceRE = regexp.MustCompile(`^script-src 'nonce-([A-Za-z0-9+/]{22})' `)

// scriptOpenRE matches every <script opening tag, with or without attributes.
var scriptOpenRE = regexp.MustCompile(`<script\b[^>]*>`)

// indexNonce fetches the index page and returns the response and the nonce
// read from the Content-Security-Policy header.
func indexNonce(t *testing.T) (string, string, http.Header) {
	t.Helper()
	rec := getRoot(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	m := cspNonceRE.FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("GET /: Content-Security-Policy = %q, want it to start with a 22-character nonce", csp)
	}
	return m[1], rec.Body.String(), rec.Header()
}

func TestCSPNonceIs16RandomBytes(t *testing.T) {
	nonce, _, _ := indexNonce(t)
	raw, err := base64.RawStdEncoding.DecodeString(nonce)
	if err != nil {
		t.Fatalf("nonce %q is not raw standard base64: %v", nonce, err)
	}
	if len(raw) != 16 {
		t.Errorf("nonce %q decodes to %d bytes, want 16", nonce, len(raw))
	}
}

func TestCSPNonceIsFreshPerResponse(t *testing.T) {
	a, _, _ := indexNonce(t)
	b, _, _ := indexNonce(t)
	if a == b {
		t.Errorf("two responses carried the same nonce %q", a)
	}
}

func TestCSPNonceInHeaderMatchesEveryScriptTag(t *testing.T) {
	nonce, body, _ := indexNonce(t)
	if strings.Contains(body, nonceMarkerText) {
		t.Errorf("GET /: body still contains %s", nonceMarkerText)
	}
	tags := scriptOpenRE.FindAllString(body, -1)
	if len(tags) != 4 {
		t.Errorf("GET /: %d <script> tags in the body, want 4", len(tags))
	}
	for _, tag := range tags {
		if !strings.Contains(tag, ` nonce="`+nonce+`"`) {
			t.Errorf("GET /: tag %s does not carry the header nonce %q", tag, nonce)
		}
	}
}

func TestCSPHeaderIsEnforcedWithThePolicy(t *testing.T) {
	nonce, _, h := indexNonce(t)
	if got, want := h.Get("Content-Security-Policy"), wantPolicy(nonce); got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}
	// S2: one enforced header; the style directives are in it, and no Report-Only copy is sent.
	if got := h.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("Content-Security-Policy-Report-Only = %q, want it absent", got)
	}
}

// A <script> tag added later without the marker would be blocked by the
// enforced policy, so the real file is checked, not a fixture.
func TestEveryScriptTagInIndexHTMLCarriesTheMarker(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(data)
	tags := scriptOpenRE.FindAllString(html, -1)
	if len(tags) == 0 {
		t.Fatal("index.html has no <script> tag - refusing to pass silently")
	}
	for _, tag := range tags {
		if !strings.Contains(tag, ` nonce="`+nonceMarkerText+`"`) {
			t.Errorf("index.html: %s has no nonce=%q", tag, nonceMarkerText)
		}
	}
	styles := regexp.MustCompile(`<style\b[^>]*>`).FindAllString(html, -1)
	if len(styles) != 1 || !strings.Contains(styles[0], ` nonce="`+nonceMarkerText+`"`) {
		t.Errorf("index.html: want exactly one <style> tag carrying the marker, got %v", styles)
	}
	if n := strings.Count(html, nonceMarkerText); n != len(tags)+len(styles) {
		t.Errorf("index.html: %s appears %d times, want %d (one per <script> and <style> tag)", nonceMarkerText, n, len(tags)+len(styles))
	}
}

func TestNoCSPHeaderOutsideTheIndexPage(t *testing.T) {
	check := func(t *testing.T, path string) {
		t.Helper()
		rec := getRoot(t, path)
		for _, name := range []string{"Content-Security-Policy-Report-Only", "Content-Security-Policy"} {
			if got := rec.Header().Get(name); got != "" {
				t.Errorf("GET %s: %s = %q, want it absent", path, name, got)
			}
		}
	}
	for _, path := range []string{"/healthz", "/version", "/config.json"} {
		t.Run(path, func(t *testing.T) { check(t, path) })
	}
	t.Run("root fallthrough", func(t *testing.T) {
		t.Chdir(t.TempDir()) // no index.html to read here
		check(t, "/")
	})
}

// Without a nonce there is no page: serving one with a missing or fixed nonce
// would break under the enforced policy, or make the nonce guessable.
func TestCSPNonceFailureFailsClosed(t *testing.T) {
	orig := nonceRandRead
	t.Cleanup(func() { nonceRandRead = orig })
	nonceRandRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }

	rec := getRoot(t, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /: status = %d, want 500", rec.Code)
	}
	for _, name := range []string{"Content-Security-Policy-Report-Only", "Content-Security-Policy"} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("GET /: %s = %q, want it absent", name, got)
		}
	}
	if body := rec.Body.String(); strings.Contains(body, "<script") || strings.Contains(body, "<html") {
		t.Errorf("GET /: body is the page (%d bytes), want only the error", len(body))
	}
}
