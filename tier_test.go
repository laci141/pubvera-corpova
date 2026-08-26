package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTierAllowsModel(t *testing.T) {
	tests := []struct {
		name     string
		tier     string
		provider string
		key      string
		model    string
		endpoint string
		want     bool
	}{
		{"free with the one permitted model", "free", "deepseek", "k", "deepseek-chat", "consensus", true},
		{"free explicitly picking another model", "free", "anthropic", "k", "claude-sonnet-4-6", "consensus", false},
		// Empty model is not "no model": it resolves to the provider default,
		// which on anthropic is claude-haiku-4-5.
		{"free with empty model on anthropic", "free", "anthropic", "k", "", "consensus", false},
		{"free with empty model on deepseek", "free", "deepseek", "k", "", "consensus", true},
		// A deepseek-hosted model that is not deepseek-chat is still refused.
		{"free with another deepseek model", "free", "deepseek", "k", "deepseek-reasoner", "consensus", false},
		// The bypass shape: leave the permitted provider in place and type a
		// foreign model into the model field. The provider matches, so only
		// the resolved model can catch this.
		{"free putting a foreign model on deepseek", "free", "deepseek", "k", "claude-sonnet-4-6", "consensus", false},
		// gaps and evidence never reach the provider, so there is no model to
		// refuse even when the caller supplied a key and a locked model.
		{"free on gaps with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", "gaps", true},
		{"free on evidence with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", "evidence", true},
		// Same caller on an endpoint that does synthesize: refused.
		{"free on consensus with a locked model", "free", "anthropic", "k", "claude-sonnet-4-6", "consensus", false},
		// No key means no provider call, so nothing to refuse.
		{"free with no key on consensus", "free", "anthropic", "", "claude-sonnet-4-6", "consensus", true},
		{"pro is unaffected", "pro", "anthropic", "k", "claude-sonnet-4-6", "consensus", true},
		{"max is unaffected", "max", "openai", "k", "", "consensus", true},
		{"owner is unaffected", "owner", "anthropic", "k", "claude-sonnet-4-6", "consensus", true},
		// No header: local development, no Caddy in front.
		{"missing tier header", "", "anthropic", "k", "claude-sonnet-4-6", "consensus", true},
		// Keyless requests never reach an LLM, so there is nothing to refuse.
		{"free keyless request", "free", "", "", "", "consensus", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := byok{provider: tc.provider, key: tc.key, model: tc.model}
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

func TestEnforceTierGate(t *testing.T) {
	tests := []struct {
		name       string
		tierHeader string // "" means the header is not set at all
		b          byok
		endpoint   string
		wantOK     bool
	}{
		{"free with deepseek-chat", "free", byok{provider: "deepseek", key: "k", model: "deepseek-chat"}, "consensus", true},
		{"free with claude-sonnet-4-6", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", false},
		// The permitted provider carrying a foreign model must still produce a
		// 403 with the model_locked body, not just an internal false.
		{"free putting a foreign model on deepseek", "free", byok{provider: "deepseek", key: "k", model: "claude-sonnet-4-6"}, "consensus", false},
		{"free with empty model on anthropic", "free", byok{provider: "anthropic", key: "k"}, "consensus", false},
		{"free with empty model on deepseek", "free", byok{provider: "deepseek", key: "k"}, "consensus", true},
		// The endpoints that never synthesize stay open to free callers.
		{"free on gaps", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "gaps", true},
		{"free on evidence", "free", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "evidence", true},
		{"free with no key on consensus", "free", byok{provider: "anthropic", model: "claude-sonnet-4-6"}, "consensus", true},
		{"pro with claude-sonnet-4-6", "pro", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true},
		{"max is unaffected", "max", byok{provider: "openai", key: "k"}, "consensus", true},
		{"owner is unaffected", "owner", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true},
		{"no tier header", "", byok{provider: "anthropic", key: "k", model: "claude-sonnet-4-6"}, "consensus", true},
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
			want := "The free plan can only use deepseek-chat. Upgrade to Pro to use other models."
			if body["message"] != want {
				t.Errorf("message = %q, want %q", body["message"], want)
			}
		})
	}
}
