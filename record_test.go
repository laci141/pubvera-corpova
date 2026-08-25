package main

// record_test.go — the usage-recording client (recordUsage in main.go).
//
// Everything here is hermetic: the auth service is an httptest server, the
// child CLI is the stub binary from cache_test.go, and the BYOK provider is a
// third httptest server. No test touches the network.
//
// The claims under test are, in order: the call is made exactly once with the
// right app/kind/user when it should be; it is not made at all in each of the
// three skip states; and a recording that fails — by status or by timeout —
// never changes or delays the analysis response the caller is owed.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testUserID stands in for the uuid Caddy's forward_auth will copy upstream in
// X-Pubvera-User once it is switched on.
const testUserID = "6f1b2c34-5d6e-4f70-8a91-b2c3d4e5f607"

const testInternalToken = "internal-shared-secret"

// recordCall is one request the mocked auth service saw, reduced to the fields
// the contract is about.
type recordCall struct {
	method string
	path   string
	app    string
	kind   string
	user   string
	token  string
	// model and the two token counts are what let the auth service price the
	// query. They are empty on a base or cached query, which is a valid state
	// and not a missing value: there is nothing to price.
	model  string
	inTok  string
	outTok string
}

// authStub is the mocked auth service. status is what it answers with; delay is
// applied AFTER the call is recorded, so a timed-out call is still observable
// as having arrived.
type authStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	callsN []recordCall
}

func newAuthStub(t *testing.T, status int, delay time.Duration) *authStub {
	t.Helper()
	s := &authStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.callsN = append(s.callsN, recordCall{
			method: r.Method,
			path:   r.URL.Path,
			app:    r.URL.Query().Get("app"),
			kind:   r.URL.Query().Get("kind"),
			user:   r.Header.Get("X-Pubvera-User"),
			token:  r.Header.Get("X-Pubvera-Internal-Token"),
			model:  r.URL.Query().Get("model"),
			inTok:  r.URL.Query().Get("in_tok"),
			outTok: r.URL.Query().Get("out_tok"),
		})
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *authStub) url() string { return s.srv.URL + "/auth/record" }

func (s *authStub) calls() []recordCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordCall(nil), s.callsN...)
}

// stubProvider points one BYOK provider at a local server returning a valid
// synthesis, so the deep path can be driven without a provider key or a
// network. The registry entry is restored when the test ends.
func stubProvider(t *testing.T, name string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.8,\"reasoning\":\"stub\",\"key_evidence\":[\"stub point\"]}"}}],"usage":{"prompt_tokens":1234,"completion_tokens":567}}`)
	}))
	t.Cleanup(srv.Close)
	prev := providers[name]
	spec := prev
	spec.BaseURL = srv.URL
	providers[name] = spec
	t.Cleanup(func() { providers[name] = prev })
}

// withRecordTimeout shortens the recording deadline for one test so the
// timeout case costs milliseconds instead of the production two seconds.
func withRecordTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := recordTimeout
	recordTimeout = d
	t.Cleanup(func() { recordTimeout = prev })
}

// recordingEnv sets (or clears) the two configuration variables.
func recordingEnv(t *testing.T, url, token string) {
	t.Helper()
	t.Setenv("AUTH_RECORD_URL", url)
	t.Setenv("INTERNAL_RECORD_TOKEN", token)
}

// analyze drives the real /api/consensus handler with the given headers and
// returns status and body.
func analyze(t *testing.T, claim string, headers map[string]string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"claim":%q,"limit":10}`, claim)
	req := httptest.NewRequest(http.MethodPost, "/api/consensus", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handleConsensus(rec, req)
	return rec.Code, rec.Body.String()
}

// synthesisTokens digs the provider's own token counts out of an analysis
// response. They are what the record call is expected to carry, so the test
// asserts against the figures the run actually produced rather than against
// numbers hardcoded here — a stub that changes its reply then cannot make this
// test pass by accident.
func synthesisTokens(t *testing.T, body string) (in, out int) {
	t.Helper()
	var resp struct {
		LLMSynthesis *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"llm_synthesis"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decoding analysis response: %v", err)
	}
	if resp.LLMSynthesis == nil {
		t.Fatal("no llm_synthesis in the response: the deep path did not run")
	}
	return resp.LLMSynthesis.InputTokens, resp.LLMSynthesis.OutputTokens
}

// recordingHarness gives a test the stub CLI and no cache, so every request
// takes the real exec path and none of them is served from a neighbour's
// leftovers.
func recordingHarness(t *testing.T) {
	t.Helper()
	buildStubCLI(t, filepath.Join(t.TempDir(), "runs.txt"))
	useCache(t, nil)
}

// TestRecordUsageIsMadeOnlyWhenItCan is the table: one row that records, and
// one row for each state in which recording is skipped. The three skip rows are
// the ones that protect the current deployment — no URL, no token, no header —
// where the correct number of outbound calls is zero.
func TestRecordUsageIsMadeOnlyWhenItCan(t *testing.T) {
	recordingHarness(t)

	cases := []struct {
		name      string
		configURL bool // set AUTH_RECORD_URL to the stub
		token     string
		user      string // X-Pubvera-User, empty means header absent
		wantCalls int
	}{
		{"header present and both env vars set", true, testInternalToken, testUserID, 1},
		{"no X-Pubvera-User header", true, testInternalToken, "", 0},
		{"AUTH_RECORD_URL empty", false, testInternalToken, testUserID, 0},
		{"INTERNAL_RECORD_TOKEN empty", true, "", testUserID, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAuthStub(t, http.StatusNoContent, 0)
			recordURL := ""
			if tc.configURL {
				recordURL = stub.url()
			}
			recordingEnv(t, recordURL, tc.token)

			headers := map[string]string{}
			if tc.user != "" {
				headers["X-Pubvera-User"] = tc.user
			}
			status, body := analyze(t, "vitamin D reduces respiratory infections", headers)
			if status != http.StatusOK {
				t.Fatalf("analysis failed: status %d, body %s", status, body)
			}

			calls := stub.calls()
			if len(calls) != tc.wantCalls {
				t.Fatalf("auth service saw %d calls, want %d: %+v", len(calls), tc.wantCalls, calls)
			}
			if tc.wantCalls == 0 {
				return
			}
			got := calls[0]
			want := recordCall{
				method: http.MethodPost,
				path:   "/auth/record",
				app:    recordApp,
				kind:   "base",
				user:   testUserID,
				token:  testInternalToken,
			}
			if got != want {
				t.Errorf("record call was\n  %+v\nwant\n  %+v", got, want)
			}
		})
	}
}

// TestRecordKindFollowsTheSynthesisDecision pins kind to the same condition
// that decides whether llmSynthesize is called: "deep" when a BYOK key drove a
// synthesis, "base" when none was used. The two cannot disagree because they
// read the same variable — this asserts that end to end.
func TestRecordKindFollowsTheSynthesisDecision(t *testing.T) {
	recordingHarness(t)
	stubProvider(t, "openai")

	cases := []struct {
		name       string
		headers    map[string]string
		wantKind   string
		wantSource string
	}{
		{
			name:       "no BYOK key: base",
			headers:    map[string]string{"X-Pubvera-User": testUserID},
			wantKind:   "base",
			wantSource: "heuristic",
		},
		{
			name: "BYOK key drove a synthesis: deep",
			headers: map[string]string{
				"X-Pubvera-User": testUserID,
				"X-LLM-Key":      "sk-test-not-a-real-key",
				"X-LLM-Provider": "openai",
			},
			wantKind:   "deep",
			wantSource: "llm:openai",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAuthStub(t, http.StatusNoContent, 0)
			recordingEnv(t, stub.url(), testInternalToken)

			status, body := analyze(t, "vitamin D reduces respiratory infections", tc.headers)
			if status != http.StatusOK {
				t.Fatalf("analysis failed: status %d, body %s", status, body)
			}
			// The response proves which path actually ran; the recorded kind must
			// agree with it, which is the whole point of deriving both from one
			// condition.
			var resp consensusResponse
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("response is not valid JSON: %v\n%s", err, body)
			}
			if resp.StanceSource != tc.wantSource {
				t.Errorf("stance_source %q, want %q (llm_error: %q)", resp.StanceSource, tc.wantSource, resp.LLMError)
			}
			calls := stub.calls()
			if len(calls) != 1 {
				t.Fatalf("auth service saw %d calls, want 1: %+v", len(calls), calls)
			}
			if calls[0].kind != tc.wantKind {
				t.Errorf("recorded kind %q, want %q", calls[0].kind, tc.wantKind)
			}
		})
	}
}

// TestRecordFailureStillSendsTheAnalysis is the failure policy: the caller
// asked a question and the answer is ready, so a broken ledger must not
// withhold it. Both failure modes are compared byte-for-byte against the same
// analysis run with recording switched off — "unchanged" means unchanged, not
// merely "still a 200".
func TestRecordFailureStillSendsTheAnalysis(t *testing.T) {
	recordingHarness(t)
	const claim = "vitamin D reduces respiratory infections"
	headers := map[string]string{"X-Pubvera-User": testUserID}

	// Baseline: recording disabled, which is the deployment state today.
	recordingEnv(t, "", "")
	baseStatus, baseBody := analyze(t, claim, headers)
	if baseStatus != http.StatusOK {
		t.Fatalf("baseline analysis failed: status %d, body %s", baseStatus, baseBody)
	}

	t.Run("auth service returns 500", func(t *testing.T) {
		stub := newAuthStub(t, http.StatusInternalServerError, 0)
		recordingEnv(t, stub.url(), testInternalToken)

		status, body := analyze(t, claim, headers)
		if status != baseStatus || body != baseBody {
			t.Errorf("a failed recording changed the response:\n got %d %s\nwant %d %s", status, body, baseStatus, baseBody)
		}
		if n := len(stub.calls()); n != 1 {
			t.Errorf("auth service saw %d calls, want 1", n)
		}
	})

	t.Run("auth service times out", func(t *testing.T) {
		// The stub hangs for far longer than the deadline; the deadline is
		// shortened so the test costs milliseconds rather than seconds.
		const hang = time.Second
		withRecordTimeout(t, 50*time.Millisecond)
		stub := newAuthStub(t, http.StatusNoContent, hang)
		recordingEnv(t, stub.url(), testInternalToken)

		start := time.Now()
		status, body := analyze(t, claim, headers)
		elapsed := time.Since(start)

		if status != baseStatus || body != baseBody {
			t.Errorf("a timed-out recording changed the response:\n got %d %s\nwant %d %s", status, body, baseStatus, baseBody)
		}
		// Returning before the stub would have answered is what proves the
		// deadline fired rather than the handler simply waiting it out.
		if elapsed >= hang {
			t.Errorf("handler took %s, i.e. it waited for the hung auth service instead of giving up at %s", elapsed, recordTimeout)
		}
		if n := len(stub.calls()); n != 1 {
			t.Errorf("auth service saw %d calls, want 1", n)
		}
	})
}

// A deep query is the only one worth pricing, so it is the only one that may
// carry a model and token counts. Without them the auth service books the row
// at zero and the money budget never moves — which is exactly the bug this
// whole change exists to fix.
func TestRecordCarriesModelAndTokensOnDeepQuery(t *testing.T) {
	recordingHarness(t)
	stubProvider(t, "deepseek")

	stub := newAuthStub(t, http.StatusNoContent, 0)
	recordingEnv(t, stub.url(), testInternalToken)

	status, body := analyze(t, "vitamin D reduces respiratory infections", map[string]string{
		"X-Pubvera-User": testUserID,
		"X-LLM-Key":      "test-key",
		"X-LLM-Provider": "deepseek",
	})
	if status != http.StatusOK {
		t.Fatalf("analysis failed: status %d, body %s", status, body)
	}

	calls := stub.calls()
	if len(calls) != 1 {
		t.Fatalf("auth service saw %d calls, want 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.kind != "deep" {
		t.Errorf("kind = %q, want %q: the LLM synthesis ran", got.kind, "deep")
	}
	// The model has to reach the ledger or the cost cannot be looked up, and an
	// unknown model is what shows up in the auth service's warning log.
	if got.model == "" {
		t.Error("model is empty on a deep query, want the model that was used")
	}
	wantIn, wantOut := synthesisTokens(t, body)
	if got.inTok != strconv.Itoa(wantIn) {
		t.Errorf("in_tok = %q, want %d (the provider's own count)", got.inTok, wantIn)
	}
	if got.outTok != strconv.Itoa(wantOut) {
		t.Errorf("out_tok = %q, want %d (the provider's own count)", got.outTok, wantOut)
	}
}

// A base query has no model and no tokens, and must not invent them: sending a
// zero-token count for a named model would tell the auth service to price a
// call that never happened.
func TestRecordOmitsModelAndTokensOnBaseQuery(t *testing.T) {
	recordingHarness(t)

	stub := newAuthStub(t, http.StatusNoContent, 0)
	recordingEnv(t, stub.url(), testInternalToken)

	status, body := analyze(t, "vitamin D reduces respiratory infections", map[string]string{
		"X-Pubvera-User": testUserID,
	})
	if status != http.StatusOK {
		t.Fatalf("analysis failed: status %d, body %s", status, body)
	}

	calls := stub.calls()
	if len(calls) != 1 {
		t.Fatalf("auth service saw %d calls, want 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.kind != "base" {
		t.Errorf("kind = %q, want %q", got.kind, "base")
	}
	if got.model != "" || got.inTok != "" || got.outTok != "" {
		t.Errorf("pricing parameters are present on a base query: model=%q in_tok=%q out_tok=%q",
			got.model, got.inTok, got.outTok)
	}
}
