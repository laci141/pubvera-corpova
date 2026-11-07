package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// POST /csp-report, the collector for the enforcing connect-src policy. Every
// request goes through handleRoot, which main()'s "/" catch-all serves, so a
// missing route fails here too.

// captureCSPLog sends the standard logger to a buffer without timestamps and
// gives the collector a fresh flood gate on the given clock.
func captureCSPLog(t *testing.T, now func() time.Time) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags, prevGate := log.Writer(), log.Flags(), cspGate
	log.SetOutput(&buf)
	log.SetFlags(0)
	cspGate = newCSPLogGate(now)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		cspGate = prevGate
	})
	return &buf
}

func postCSPReport(contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handleRoot(rec, req)
	return rec
}

func fixedCSPClock() func() time.Time {
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

func cspLogLines(buf *bytes.Buffer) []string {
	s := strings.TrimSuffix(buf.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// report-to (application/reports+json): an array; only csp-violation reports
// are logged, with the origin of blockedURL and the path of documentURL.
const cspReportsJSONBody = `[
 {"type":"deprecation","url":"https://corpova.pubvera.com/","body":{"id":"x"}},
 {"type":"csp-violation","age":10,"url":"https://corpova.pubvera.com/?q=1","user_agent":"Mozilla/5.0 Full UA",
  "body":{"effectiveDirective":"connect-src","blockedURL":"https://evil.example/collect?code=SECRET&x=1#frag",
   "documentURL":"https://corpova.pubvera.com/index.html?claim=cancer#top","disposition":"report","statusCode":200,"lineNumber":3}}
]`

// report-uri (application/csp-report): one object.
const cspReportURIBody = `{"csp-report":{"document-uri":"https://corpova.pubvera.com/?token=SECRET","referrer":"",
 "violated-directive":"connect-src","effective-directive":"connect-src","original-policy":"connect-src 'self'",
 "disposition":"report","blocked-uri":"https://abc.supabase.co:8443/auth/v1/logout?scope=global","status-code":200}}`

func TestCSPReportAccepted(t *testing.T) {
	for _, tc := range []struct {
		name, ct, body, want string
	}{
		{"reports+json", "application/reports+json", cspReportsJSONBody,
			"csp-violation disposition=report directive=connect-src blocked=https://evil.example page=/index.html"},
		{"reports+json, mixed case with a parameter", "Application/Reports+JSON; charset=utf-8", cspReportsJSONBody,
			"csp-violation disposition=report directive=connect-src blocked=https://evil.example page=/index.html"},
		{"csp-report", "application/csp-report", cspReportURIBody,
			"csp-violation disposition=report directive=connect-src blocked=https://abc.supabase.co:8443 page=/"},
		{"keyword blocked value", "application/csp-report",
			`{"csp-report":{"document-uri":"https://corpova.pubvera.com/a/b","effective-directive":"script-src-elem","disposition":"enforce","blocked-uri":"inline"}}`,
			"csp-violation disposition=enforce directive=script-src-elem blocked=inline page=/a/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureCSPLog(t, fixedCSPClock())
			rec := postCSPReport(tc.ct, tc.body)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
			if got := cspLogLines(buf); len(got) != 1 || got[0] != tc.want {
				t.Errorf("log lines = %q, want [%q]", got, tc.want)
			}
			for _, bad := range []string{"SECRET", "code=", "token=", "Mozilla", "cancer", "#", "?"} {
				if strings.Contains(buf.String(), bad) {
					t.Errorf("log %q contains %q", buf.String(), bad)
				}
			}
		})
	}
}

// The collector answers only POST with a report media type and a small,
// well-formed body, and a rejected request logs nothing.
func TestCSPReportRejected(t *testing.T) {
	buf := captureCSPLog(t, fixedCSPClock())

	for _, m := range []string{http.MethodGet, http.MethodPut} {
		rec := httptest.NewRecorder()
		handleRoot(rec, httptest.NewRequest(m, "/csp-report", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s status = %d Allow = %q, want 405 and %q", m, rec.Code, rec.Header().Get("Allow"), "POST")
		}
	}

	for _, ct := range []string{"text/plain", "", "application/json", "application/reports+json;;"} {
		if rec := postCSPReport(ct, cspReportsJSONBody); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q status = %d, want 415", ct, rec.Code)
		}
	}

	for _, ct := range []string{"application/reports+json", "application/csp-report"} {
		if rec := postCSPReport(ct, `{"csp-report":`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s invalid JSON status = %d, want 400", ct, rec.Code)
		}
	}

	// Exactly 16 KiB is still read; one byte more is refused.
	const limit = 16 << 10
	if rec := postCSPReport("application/reports+json", "[]"+strings.Repeat(" ", limit-2)); rec.Code != http.StatusNoContent {
		t.Errorf("body of exactly %d bytes status = %d, want 204", limit, rec.Code)
	}
	if rec := postCSPReport("application/reports+json", "[]"+strings.Repeat(" ", limit-1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("body of %d bytes status = %d, want 413", limit+1, rec.Code)
	}

	if buf.Len() != 0 {
		t.Errorf("rejected requests logged %q, want nothing", buf.String())
	}
}

// Report values are attacker-controlled: a newline must not start a second,
// forged log line, and every value is capped at 200 bytes.
func TestCSPReportSanitizesValues(t *testing.T) {
	buf := captureCSPLog(t, fixedCSPClock())
	long := strings.Repeat("a", 500)
	body := `[{"type":"csp-violation","body":{"effectiveDirective":"connect-src\ncsp-violation disposition=forged\r\u0000\u001b[31m","blockedURL":"eval",` +
		`"documentURL":"https://corpova.pubvera.com/` + long + `","disposition":"` + long + `"}}]`
	if rec := postCSPReport("application/reports+json", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	got := cspLogLines(buf)
	if len(got) != 1 {
		t.Fatalf("log lines = %q, want exactly one", got)
	}
	want := "csp-violation disposition=" + long[:200] + " directive=connect-srccsp-violation disposition=forged[31m blocked=eval page=/" + long[:199]
	if got[0] != want {
		t.Errorf("log line = %q, want %q", got[0], want)
	}
}

// The cap cuts on a rune boundary: 199 ASCII bytes and then a 2-byte rune
// would end at byte 201, so the rune is dropped, not split.
func TestCSPReportCapIsOnARuneBoundary(t *testing.T) {
	buf := captureCSPLog(t, fixedCSPClock())
	v := strings.Repeat("a", 199) + "é" + "tail"
	body := `{"csp-report":{"document-uri":"https://b.example/","effective-directive":"connect-src","disposition":"` + v + `","blocked-uri":"eval"}}`
	if rec := postCSPReport("application/csp-report", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	want := "csp-violation disposition=" + strings.Repeat("a", 199) + " directive=connect-src blocked=eval page=/"
	if got := cspLogLines(buf); len(got) != 1 || got[0] != want {
		t.Errorf("log lines = %q, want [%q]", got, want)
	}
}

func TestCSPReportProcessesAtMost20(t *testing.T) {
	buf := captureCSPLog(t, fixedCSPClock())
	one := `{"type":"csp-violation","body":{"effectiveDirective":"connect-src","blockedURL":"https://x.example/","documentURL":"https://b.example/","disposition":"report"}}`
	body := "[" + strings.TrimSuffix(strings.Repeat(one+",", 25), ",") + "]"
	if rec := postCSPReport("application/reports+json", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(cspLogLines(buf)); n != 20 {
		t.Errorf("25 reports in one body logged %d lines, want 20", n)
	}
}

// 61 reports in one window: 60 lines, the 61st is only counted. The window
// that dropped lines says so once, when the next window begins.
func TestCSPReportFloodCap(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	buf := captureCSPLog(t, func() time.Time { return now })
	one := `{"csp-report":{"document-uri":"https://b.example/","effective-directive":"connect-src","disposition":"report","blocked-uri":"https://x.example/"}}`
	line := "csp-violation disposition=report directive=connect-src blocked=https://x.example page=/"
	for i := 0; i < 61; i++ {
		if rec := postCSPReport("application/csp-report", one); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d status = %d, want 204", i+1, rec.Code)
		}
		now = now.Add(500 * time.Millisecond) // all 61 inside one minute
	}
	got := cspLogLines(buf)
	if len(got) != 60 {
		t.Fatalf("61 reports in one window logged %d lines, want 60", len(got))
	}
	for _, l := range got {
		if l != line {
			t.Fatalf("log line = %q, want %q", l, line)
		}
	}

	now = now.Add(time.Minute)
	if rec := postCSPReport("application/csp-report", one); rec.Code != http.StatusNoContent {
		t.Fatalf("status after the window = %d, want 204", rec.Code)
	}
	got = cspLogLines(buf)
	if len(got) != 62 || got[60] != "csp-violation dropped=1" || got[61] != line {
		t.Errorf("after the window: %d lines, last two %q, want 62 lines ending [%q %q]", len(got), got[len(got)-2:], "csp-violation dropped=1", line)
	}
}

// The collector's own response carries no policy: the CSP headers stay on the
// index page only.
func TestCSPReportResponseHasNoPolicy(t *testing.T) {
	captureCSPLog(t, fixedCSPClock())
	rec := postCSPReport("application/csp-report", cspReportURIBody)
	for _, name := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Reporting-Endpoints"} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("POST /csp-report: %s = %q, want none", name, got)
		}
	}
}
