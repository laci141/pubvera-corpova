package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTierAllowsModel(t *testing.T) {
	tests := []struct {
		name      string
		tier      string
		provider  string
		key       string
		model     string
		serverKey bool
		endpoint  string
		want      bool
	}{
		{"free with the one permitted model", "free", "deepseek", "k", "deepseek-chat", false, "consensus", true},
		{"free explicitly picking another model", "free", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", false},
		// Empty model is not "no model": it resolves to the provider default,
		// which on anthropic is claude-haiku-4-5.
		{"free with empty model on anthropic", "free", "anthropic", "k", "", false, "consensus", false},
		{"free with empty model on deepseek", "free", "deepseek", "k", "", false, "consensus", true},
		// A deepseek-hosted model that is not deepseek-chat is still refused.
		{"free with another deepseek model", "free", "deepseek", "k", "deepseek-reasoner", false, "consensus", false},
		// The bypass shape: leave the permitted provider in place and type a
		// foreign model into the model field. The provider matches, so only
		// the resolved model can catch this.
		{"free putting a foreign model on deepseek", "free", "deepseek", "k", "claude-sonnet-4-6", false, "consensus", false},
		// gaps and evidence never reach the provider, so there is no model to
		// refuse even when the caller supplied a key and a locked model.
		{"free on gaps with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", false, "gaps", true},
		{"free on evidence with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", false, "evidence", true},
		// Same caller on an endpoint that does synthesize: refused.
		{"free on consensus with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", false},
		// No key means no provider call, so nothing to refuse.
		{"free with no key on consensus", "free", "anthropic", "", "claude-sonnet-4-6", false, "consensus", true},
		{"pro is unaffected", "pro", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", true},
		{"max is unaffected", "max", "openai", "k", "", false, "consensus", true},
		{"owner is unaffected", "owner", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", true},
		// No header: local development, no Caddy in front.
		{"missing tier header", "", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", true},
		// Keyless requests never reach an LLM, so there is nothing to refuse.
		{"free keyless request", "free", "", "", "", false, "consensus", true},

		// ---- the server's own key: the operator pays, so the pin applies ----
		// These are the cases the trial fallback creates. The tier is no longer
		// what decides; whose money it is decides.
		{"server key with the permitted model", "trial", "deepseek", "k", "deepseek-chat", true, "consensus", true},
		{"server key with an empty model", "trial", "deepseek", "k", "", true, "consensus", true},
		{"server key asked for sonnet", "trial", "deepseek", "k", "claude-sonnet-4-6", true, "consensus", false},
		{"server key asked for deepseek-reasoner", "trial", "deepseek", "k", "deepseek-reasoner", true, "consensus", false},
		// The free-tier branch would allow anything on a non-free tier, so the
		// serverKey check has to come first — this is the case that proves it.
		{"server key on pro tier still pinned", "pro", "deepseek", "k", "claude-sonnet-4-6", true, "consensus", false},
		{"server key on an absent tier still pinned", "", "deepseek", "k", "claude-sonnet-4-6", true, "consensus", false},
		// Endpoints that never synthesize cost nothing, so they stay open even
		// on the server's key.
		{"server key on gaps", "trial", "deepseek", "k", "claude-sonnet-4-6", true, "gaps", true},
		{"server key on evidence", "trial", "deepseek", "k", "claude-sonnet-4-6", true, "evidence", true},

		// ---- trial spending its OWN key: not pinned ----
		// The whole point of allowing BYOK on trial. Their money, their choice.
		{"trial with own key on sonnet", "trial", "anthropic", "k", "claude-sonnet-4-6", false, "consensus", true},
		{"trial with own key on deepseek", "trial", "deepseek", "k", "deepseek-chat", false, "consensus", true},
		{"trial with own key, empty model", "trial", "openai", "k", "", false, "consensus", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := byok{provider: tc.provider, key: tc.key, model: tc.model, serverKey: tc.serverKey}
			if got := tierAllowsModel(tc.tier, b, tc.endpoint); got != tc.want {
				t.Errorf("tierAllowsModel(%q, %+v, %q) = %v, want %v",
					tc.tier, b, tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestLLMWillRun(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		endpoint string
		want     bool
	}{
		{"key on consensus", "k", "consensus", true},
		{"key on compare", "k", "compare", true},
		{"key on controversies", "k", "controversies", true},
		{"key on gaps", "k", "gaps", false},
		{"key on evidence", "k", "evidence", false},
		{"no key on consensus", "", "consensus", false},
		{"no key on gaps", "", "gaps", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := byok{provider: "anthropic", key: tc.key}
			if got := llmWillRun(b, tc.endpoint); got != tc.want {
				t.Errorf("llmWillRun(%+v, %q) = %v, want %v", b, tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{"override wins", "anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"empty falls back to the provider default", "anthropic", "", "claude-haiku-4-5"},
		{"deepseek default", "deepseek", "", "deepseek-chat"},
		{"unknown provider has no default", "nope", "", ""},
		{"no provider at all", "", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveModel(tc.provider, tc.model); got != tc.want {
				t.Errorf("resolveModel(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

// The two refusal texts are not interchangeable. Telling a trial caller to
// "upgrade to Pro" would be wrong — pasting their own key is all they need —
// and telling a free caller to paste a key would hide the plan limit. A future
// edit that collapses them back into one string has to fail here.
const (
	freeRefusal      = "The free plan can only use deepseek-chat. Upgrade to Pro to use other models."
	serverKeyRefusal = "The included trial key only runs deepseek-chat. Enter your own API key to use other models."
)

func TestTierRefusalMessage(t *testing.T) {
	if got := tierRefusalMessage(byok{provider: "anthropic", key: "k"}); got != freeRefusal {
		t.Errorf("BYOK refusal = %q, want %q", got, freeRefusal)
	}
	if got := tierRefusalMessage(byok{provider: "deepseek", key: "k", serverKey: true}); got != serverKeyRefusal {
		t.Errorf("server-key refusal = %q, want %q", got, serverKeyRefusal)
	}
}

func TestEnforceTierGate(t *testing.T) {
	tests := []struct {
		name       string
		tierHeader string // "" means the header is not set at all
		b          byok
		endpoint   string
		wantOK     bool
		wantMsg    string // only checked when wantOK is false
	}{
		{"free with deepseek-chat", "free", byok{provider: "deepseek", key: "k", model: "deepseek-chat"}, "consensus", true, ""},
		{"free with claude-sonnet-4-6", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", false, freeRefusal},
		// The permitted provider carrying a foreign model must still produce a
		// 403 with the model_locked body, not just an internal false.
		{"free putting a foreign model on deepseek", "free", byok{provider: "deepseek", key: "k", model: "claude-sonnet-4-6"}, "consensus", false, freeRefusal},
		{"free with empty model on anthropic", "free", byok{provider: "anthropic", key: "k"}, "consensus", false, freeRefusal},
		{"free with empty model on deepseek", "free", byok{provider: "deepseek", key: "k"}, "consensus", true, ""},
		// The endpoints that never synthesize stay open to free callers.
		{"free on gaps", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "gaps", true, ""},
		{"free on evidence", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "evidence", true, ""},
		{"free with no key on consensus", "free", byok{provider: "anthropic", model: "claude-sonnet-4-6"}, "consensus", true, ""},
		{"pro with claude-sonnet-4-6", "pro", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true, ""},
		{"max is unaffected", "max", byok{provider: "openai", key: "k"}, "consensus", true, ""},
		{"owner is unaffected", "owner", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true, ""},
		{"no tier header", "", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true, ""},

		// The server's key refuses with its own wording.
		{"server key with deepseek-chat", "trial", byok{provider: "deepseek", key: "k", model: "deepseek-chat", serverKey: true}, "consensus", true, ""},
		{"server key asked for sonnet", "trial", byok{provider: "deepseek", key: "k", model: "claude-sonnet-4-6", serverKey: true}, "consensus", false, serverKeyRefusal},
		{"server key on gaps", "trial", byok{provider: "deepseek", key: "k", model: "claude-sonnet-4-6", serverKey: true}, "gaps", true, ""},
		// Trial paying for itself is not gated at all.
		{"trial with own key on sonnet", "trial", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/"+tc.endpoint, nil)
			if tc.tierHeader != "" {
				r.Header.Set("X-Pubvera-Tier", tc.tierHeader)
			}
			w := httptest.NewRecorder()

			if got := enforceTierGate(w, r, tc.b, tc.endpoint); got != tc.wantOK {
				t.Fatalf("enforceTierGate() = %v, want %v", got, tc.wantOK)
			}
			if tc.wantOK {
				if w.Code != http.StatusOK || w.Body.Len() != 0 {
					t.Fatalf("allowed request wrote a response: status %d, body %q", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, w.Body.String())
			}
			if body["error"] != "model_locked" {
				t.Errorf("error = %q, want %q", body["error"], "model_locked")
			}
			if body["message"] != tc.wantMsg {
				t.Errorf("message = %q, want %q", body["message"], tc.wantMsg)
			}
		})
	}
}

// TestExtractBYOKServerKey covers who gets handed the operator's key. It is the
// money question: every tier that reaches the fallback spends the operator's
// balance, so the allow-list has to be exactly one tier and nothing else.
func TestExtractBYOKServerKey(t *testing.T) {
	const serverSecret = "sk-server-side-secret"

	tests := []struct {
		name          string
		tierHeader    string // "" means the header is not set at all
		envKey        string // "" means the variable is not set
		wantKey       string
		wantProvider  string
		wantServerKey bool
	}{
		{"trial with the key configured", "trial", serverSecret, serverSecret, freeTierProvider, true},
		// No fallback configured: trial behaves exactly as it did before.
		{"trial with no key configured", "trial", "", "", "", false},
		// Every other tier is BYOK. A free signup must never reach the
		// operator's balance.
		{"free never gets the server key", "free", serverSecret, "", "", false},
		{"pro never gets the server key", "pro", serverSecret, "", "", false},
		{"max never gets the server key", "max", serverSecret, "", "", false},
		{"owner never gets the server key", "owner", serverSecret, "", "", false},
		// No header is local development (`go run .`, no Caddy). Handing out
		// the key here would arm the fallback for unauthenticated callers.
		{"absent tier header never gets the server key", "", serverSecret, "", "", false},
		// Near-misses must not match: the comparison is exact.
		{"trailing text is not the trial tier", "trial-extended", serverSecret, "", "", false},
		{"different case is not the trial tier", "Trial", serverSecret, "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(serverKeyEnvVar, tc.envKey)

			r := httptest.NewRequest(http.MethodPost, "/api/consensus", nil)
			if tc.tierHeader != "" {
				r.Header.Set("X-Pubvera-Tier", tc.tierHeader)
			}
			w := httptest.NewRecorder()

			b, ok := extractBYOK(w, r, "", "")
			if !ok {
				t.Fatalf("extractBYOK returned false: %q", w.Body.String())
			}
			if b.key != tc.wantKey {
				t.Errorf("key set = %v, want %v", b.key != "", tc.wantKey != "")
			}
			if b.provider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", b.provider, tc.wantProvider)
			}
			if b.serverKey != tc.wantServerKey {
				t.Errorf("serverKey = %v, want %v", b.serverKey, tc.wantServerKey)
			}
		})
	}
}

// A caller's own key always wins over the fallback, even on trial: the header
// is what they explicitly asked for, and silently spending the operator's
// balance instead would misreport whose money paid for the answer.
func TestExtractBYOKOwnKeyWinsOnTrial(t *testing.T) {
	t.Setenv(serverKeyEnvVar, "sk-server-side-secret")

	r := httptest.NewRequest(http.MethodPost, "/api/consensus", nil)
	r.Header.Set("X-Pubvera-Tier", trialTier)
	r.Header.Set("X-LLM-Key", "sk-callers-own")
	r.Header.Set("X-LLM-Provider", "anthropic")
	w := httptest.NewRecorder()

	b, ok := extractBYOK(w, r, "", "")
	if !ok {
		t.Fatalf("extractBYOK returned false: %q", w.Body.String())
	}
	if b.serverKey {
		t.Error("serverKey = true, want false — the caller supplied their own key")
	}
	if b.provider != "anthropic" {
		t.Errorf("provider = %q, want %q", b.provider, "anthropic")
	}
	if b.key != "sk-callers-own" {
		t.Error("the caller's own key was not used")
	}
}

// runEndpoint drives one real /api/<endpoint> handler through the hermetic
// stack recordingHarness sets up (stub CLI, no cache) and returns the decoded
// response. It exists because record_test.go's analyze covers /api/consensus
// only, and the claim under test here is precisely that a non-synthesizing
// endpoint behaves differently from a synthesizing one.
func runEndpoint(t *testing.T, endpoint string, headers map[string]string) consensusResponse {
	t.Helper()
	body := `{"claim":"vitamin D reduces respiratory infections","limit":10}`
	req := httptest.NewRequest(http.MethodPost, "/api/"+endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	switch endpoint {
	case "consensus":
		handleConsensus(rec, req)
	case "gaps":
		handleGaps(rec, req)
	case "controversies":
		handleControversies(rec, req)
	default:
		t.Fatalf("runEndpoint does not know endpoint %q", endpoint)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", endpoint, rec.Code, rec.Body.String())
	}
	var resp consensusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: response is not valid JSON: %v\n%s", endpoint, err, rec.Body.String())
	}
	return resp
}

// TestServerKeyReportedOnlyWhenAModelRan pins the field the UI reads to name
// the model on the result card.
//
// The defect it guards: a trial caller who leaves the key field empty is
// silently switched to the server's deepseek key (extractBYOK), so the model
// that answered is not the one the dropdown shows. server_key is what lets the
// card say so. Two halves of the condition, and both are load-bearing:
//
//   - b.serverKey — a caller spending their OWN key was not switched to
//     anything, so there is nothing to disclose.
//   - an LLM actually ran — gaps and evidence never reach a provider, so
//     reporting the server's key there would name a model that never ran.
//
// The value is read out of the JSON, not off the struct, because the UI reads
// JSON: omitempty means false must arrive as an ABSENT key, and a test against
// the struct field could not tell those apart.
func TestServerKeyReportedOnlyWhenAModelRan(t *testing.T) {
	const serverSecret = "sk-server-side-secret"

	tests := []struct {
		name     string
		tier     string
		ownKey   bool // the caller pasted their own key
		endpoint string
		want     bool
	}{
		// The live defect, 2026-09-01: trial, empty key field, consensus. The
		// answer came from the server's deepseek-chat and the UI never said so.
		{"trial on the server key synthesizes: disclosed", trialTier, false, "consensus", true},
		// Same caller, same key, an endpoint that runs no LLM at all. There is
		// no model to name, so the field must be absent.
		{"trial on the server key, gaps runs no LLM: silent", trialTier, false, "gaps", false},
		// Own key on trial: their money, their model, the dropdown told the
		// truth. Nothing was substituted, so nothing to disclose.
		{"trial with the caller's own key: silent", trialTier, true, "consensus", false},
		// Pro is never handed the server key in the first place.
		{"pro with its own key: silent", "pro", true, "consensus", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recordingHarness(t)
			// Both providers are stubbed: the server-key path is rewritten to
			// deepseek by extractBYOK, the own-key path stays on openai.
			stubProvider(t, freeTierProvider)
			stubProvider(t, "openai")
			t.Setenv(serverKeyEnvVar, serverSecret)

			headers := map[string]string{"X-Pubvera-Tier": tc.tier}
			if tc.ownKey {
				headers["X-LLM-Key"] = "sk-callers-own"
				headers["X-LLM-Provider"] = "openai"
			}

			resp := runEndpoint(t, tc.endpoint, headers)
			if resp.ServerKey != tc.want {
				t.Errorf("server_key = %v, want %v (stance_source %q, llm_error %q)",
					resp.ServerKey, tc.want, resp.StanceSource, resp.LLMError)
			}
			// The card names llm_synthesis.model, so a true server_key with no
			// model to read would render nothing — the disclosure would be lost.
			if tc.want && (resp.LLMSynthesis == nil || resp.LLMSynthesis.Model == "") {
				t.Error("server_key is true but the response names no model for the card to show")
			}
		})
	}
}

// TestServerKeyIsAbsentNotFalse guards the wire shape omitempty buys: existing
// BYOK clients must see no new key at all, rather than a new "server_key":false.
func TestServerKeyIsAbsentNotFalse(t *testing.T) {
	recordingHarness(t)
	stubProvider(t, "openai")

	req := httptest.NewRequest(http.MethodPost, "/api/consensus",
		strings.NewReader(`{"claim":"vitamin D reduces respiratory infections","limit":10}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Key", "sk-callers-own")
	req.Header.Set("X-LLM-Provider", "openai")
	rec := httptest.NewRecorder()
	handleConsensus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "server_key") {
		t.Errorf("a BYOK response carries a server_key key; it must be omitted entirely:\n%s", rec.Body.String())
	}
}
