package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// consensusJSON builds a consensus-shaped CLI output with n all_studies entries
// (each carrying an abstract) plus top_supporting/top_refuting lists.
func consensusJSON(t *testing.T, n int) []byte {
	t.Helper()
	studies := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		studies = append(studies, map[string]any{
			"title":    fmt.Sprintf("Study %d", i),
			"year":     2020,
			"abstract": fmt.Sprintf("Abstract of study %d.", i),
		})
	}
	out, err := json.Marshal(map[string]any{
		"claim":           "vitamin D reduces respiratory infections",
		"verdict":         "supported",
		"consensus_score": 0.8,
		"top_supporting":  studies[:min(2, n)],
		"top_refuting":    []map[string]any{},
		"all_studies":     studies,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// decode unmarshals compacted output for assertions.
func decode(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("compacted output is not valid JSON: %v", err)
	}
	return obj
}

func studyCount(t *testing.T, obj map[string]json.RawMessage) int {
	t.Helper()
	var list []json.RawMessage
	if err := json.Unmarshal(obj["all_studies"], &list); err != nil {
		t.Fatalf("all_studies not an array: %v", err)
	}
	return len(list)
}

func TestCompactForLLMCapsAllStudiesAndDropsTopLists(t *testing.T) {
	got := decode(t, compactForLLM(consensusJSON(t, 40)))

	if n := studyCount(t, got); n != maxStudiesForLLM {
		t.Errorf("all_studies: got %d entries, want %d", n, maxStudiesForLLM)
	}
	for _, k := range []string{"top_supporting", "top_refuting"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be removed when all_studies is present", k)
		}
	}
	// The trim keeps the FIRST (most relevant) entries and their abstracts.
	var list []struct {
		Title    string `json:"title"`
		Abstract string `json:"abstract"`
	}
	if err := json.Unmarshal(got["all_studies"], &list); err != nil {
		t.Fatal(err)
	}
	if list[0].Title != "Study 0" || list[len(list)-1].Title != fmt.Sprintf("Study %d", maxStudiesForLLM-1) {
		t.Errorf("trim did not keep the first %d entries: first=%q last=%q", maxStudiesForLLM, list[0].Title, list[len(list)-1].Title)
	}
	if list[0].Abstract == "" {
		t.Error("abstract lost during compaction")
	}
	// Unrelated fields survive.
	if _, ok := got["consensus_score"]; !ok {
		t.Error("consensus_score dropped during compaction")
	}
}

func TestCompactForLLMShortListKeptButTopListsStillDropped(t *testing.T) {
	got := decode(t, compactForLLM(consensusJSON(t, 10)))
	if n := studyCount(t, got); n != 10 {
		t.Errorf("all_studies: got %d entries, want 10 (no trim below cap)", n)
	}
	if _, ok := got["top_supporting"]; ok {
		t.Error("top_supporting should be removed even when all_studies is under the cap")
	}
}

func TestCompactForLLMCompareNested(t *testing.T) {
	cmp, err := json.Marshal(map[string]any{
		"claim_a":          json.RawMessage(consensusJSON(t, 30)),
		"claim_b":          json.RawMessage(consensusJSON(t, 30)),
		"stronger_support": "claim_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, compactForLLM(cmp))
	for _, k := range []string{"claim_a", "claim_b"} {
		sub := decode(t, got[k])
		if n := studyCount(t, sub); n != maxStudiesForCompare {
			t.Errorf("%s.all_studies: got %d entries, want %d (per-claim compare cap)", k, n, maxStudiesForCompare)
		}
		if _, ok := sub["top_supporting"]; ok {
			t.Errorf("%s.top_supporting should be removed", k)
		}
	}
	var stronger string
	if err := json.Unmarshal(got["stronger_support"], &stronger); err != nil || stronger != "claim_a" {
		t.Errorf("stronger_support corrupted: %s err=%v", got["stronger_support"], err)
	}
}

// consensusJSONWithAbstractLen mirrors consensusJSON but with worst-case
// abstract sizes (the CLI caps abstracts at ~1500 chars).
func consensusJSONWithAbstractLen(t *testing.T, n, abstractLen int) []byte {
	t.Helper()
	studies := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		studies = append(studies, map[string]any{
			"title":    fmt.Sprintf("Study %d with a realistically long title about the claim", i),
			"year":     2020,
			"abstract": strings.Repeat("a", abstractLen),
		})
	}
	out, err := json.Marshal(map[string]any{
		"claim":           "vitamin D reduces respiratory infections",
		"verdict":         "supported",
		"consensus_score": 0.8,
		"top_supporting":  studies[:min(2, n)],
		"top_refuting":    []map[string]any{},
		"all_studies":     studies,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCompareCompactionFitsByteCap encodes the point of the per-claim compare
// cap: two claims with worst-case 1500-char abstracts must fit under
// maxCLIJSONForPrompt after compaction, so the byte cap never cuts claim_b's
// JSON mid-array.
func TestCompareCompactionFitsByteCap(t *testing.T) {
	cmp, err := json.Marshal(map[string]any{
		"claim_a":          json.RawMessage(consensusJSONWithAbstractLen(t, 100, 1500)),
		"claim_b":          json.RawMessage(consensusJSONWithAbstractLen(t, 100, 1500)),
		"stronger_support": "claim_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := compactForLLM(cmp)
	if len(got) >= maxCLIJSONForPrompt {
		t.Errorf("compacted compare JSON is %d bytes, must stay under %d", len(got), maxCLIJSONForPrompt)
	}
	t.Logf("compacted compare JSON: %d bytes (cap %d)", len(got), maxCLIJSONForPrompt)
}

func TestCompactForLLMNoAllStudiesUnchanged(t *testing.T) {
	// evidence/gaps/controversies-shaped output (or an older CLI binary).
	raw := []byte(`{"claim":"x","designs":[{"design":"rct","count":3}],"note":"n"}`)
	if got := compactForLLM(raw); !bytes.Equal(got, raw) {
		t.Errorf("JSON without all_studies must be returned byte-identical\ngot:  %s\nwant: %s", got, raw)
	}
}

func TestCompactForLLMInvalidInputsUnchanged(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`not json at all`),
		[]byte(`[1,2,3]`),                  // top level not an object
		[]byte(`{"all_studies":"oops"}`),   // all_studies not an array
		[]byte(`{"claim_a":"not-object"}`), // compare key not an object
	} {
		if got := compactForLLM(raw); !bytes.Equal(got, raw) {
			t.Errorf("input %q must pass through unchanged, got %q", raw, got)
		}
	}
}

func TestSynthesisPromptUsesCompactedJSON(t *testing.T) {
	prompt := synthesisPrompt("consensus", []string{"claim"}, consensusJSON(t, 40))
	if strings.Contains(prompt, "top_supporting") {
		t.Error("prompt still contains top_supporting after compaction")
	}
	if !strings.Contains(prompt, "all_studies") {
		t.Error("prompt does not contain all_studies")
	}
	if !strings.Contains(prompt, fmt.Sprintf("Study %d", maxStudiesForLLM-1)) {
		t.Errorf("prompt missing study %d (last kept entry)", maxStudiesForLLM-1)
	}
	if strings.Contains(prompt, fmt.Sprintf(`"Study %d"`, maxStudiesForLLM)) {
		t.Errorf("prompt contains study %d, which should be trimmed", maxStudiesForLLM)
	}
}

// TestGeminiDefaultModelIsTheMeasuredWorkingOne pins the gemini default to the
// only gemini model measured to answer. On 2026-09-04 the live log showed
// gemini-3.7-flash returning invalid JSON and 3.6/3.8-flash returning HTTP 503,
// while gemini-3.5-flash succeeded twice; index.html recommends 3.5 in its
// datalist notes, and an empty model field falls through to this default, so a
// silent move here would make the UI recommend one model and run another.
// Re-measure before changing it.
func TestGeminiDefaultModelIsTheMeasuredWorkingOne(t *testing.T) {
	const want = "gemini-3.5-flash"
	if got := providers["gemini"].DefaultModel; got != want {
		t.Errorf("providers[gemini].DefaultModel = %q, want %q", got, want)
	}
	// An empty model override is what the UI actually sends.
	if got := resolveModel("gemini", ""); got != want {
		t.Errorf("resolveModel(gemini, \"\") = %q, want %q", got, want)
	}
}

// compareJSON builds a compare-shaped CLI output whose claims carry a and b
// all_studies entries.
func compareJSON(t *testing.T, a, b int) []byte {
	t.Helper()
	out, err := json.Marshal(map[string]any{
		"claim_a": json.RawMessage(consensusJSON(t, a)),
		"claim_b": json.RawMessage(consensusJSON(t, b)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLLMScopeFor(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want *llmScope
	}{
		{"single 40 is capped", consensusJSON(t, 40),
			&llmScope{Mode: "single", MaxPerClaim: maxStudiesForLLM, Available: 40, Reviewed: maxStudiesForLLM}},
		{"single 10 is fully reviewed", consensusJSON(t, 10),
			&llmScope{Mode: "single", MaxPerClaim: maxStudiesForLLM, Available: 10, Reviewed: 10}},
		{"compare a=30 b=5", compareJSON(t, 30, 5),
			&llmScope{Mode: "compare", MaxPerClaim: maxStudiesForCompare,
				ClaimA: &llmScopeClaim{Available: 30, Reviewed: maxStudiesForCompare},
				ClaimB: &llmScopeClaim{Available: 5, Reviewed: 5}}},
		{"compare with only claim_a", []byte(`{"claim_a":{"all_studies":[{},{}]}}`),
			&llmScope{Mode: "compare", MaxPerClaim: maxStudiesForCompare,
				ClaimA: &llmScopeClaim{Available: 2, Reviewed: 2},
				ClaimB: &llmScopeClaim{}}},
		{"invalid JSON", []byte(`{not json`), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := llmScopeFor(tc.raw)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if (got == nil) != (tc.want == nil) || !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("llmScopeFor = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestHandlerLLMScopeOnlyOnLLMPath: llm_scope describes what the LLM saw, so it
// must be on a successful synthesis and absent from a heuristic response.
func TestHandlerLLMScopeOnlyOnLLMPath(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}}]}`))
	}))
	defer llm.Close()
	useFakeProvider(t, "testprovider", llm.URL)
	buildSlowStubCLI(t, filepath.Join(t.TempDir(), "runs.txt"), 0)
	useCache(t, nil)

	scopeOf := func(body string) (string, json.RawMessage) {
		var m struct {
			StanceSource string          `json:"stance_source"`
			LLMScope     json.RawMessage `json:"llm_scope"`
		}
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		return m.StanceSource, m.LLMScope
	}

	status, body := postConsensusWithKey(t, "scope llm claim", "testprovider", "some-key")
	if status != http.StatusOK {
		t.Fatalf("llm path: status %d body %s", status, body)
	}
	src, scope := scopeOf(body)
	if src != "llm:testprovider" {
		t.Fatalf("stance_source = %q, want llm:testprovider", src)
	}
	if !strings.Contains(string(scope), `"mode":"single"`) || !strings.Contains(string(scope), fmt.Sprintf(`"max_per_claim":%d`, maxStudiesForLLM)) {
		t.Errorf("llm path: llm_scope = %s, want single mode with max_per_claim %d", scope, maxStudiesForLLM)
	}

	status, body = postConsensus(t, "scope heuristic claim")
	if status != http.StatusOK {
		t.Fatalf("heuristic path: status %d body %s", status, body)
	}
	src, scope = scopeOf(body)
	if src != "heuristic" {
		t.Fatalf("stance_source = %q, want heuristic", src)
	}
	if scope != nil {
		t.Errorf("heuristic path: llm_scope = %s, want absent", scope)
	}
}

// TestModelRegistryDefaults pins the 2026-10-08 registry refresh. Keep it in
// step with providerDefaults in index.html (the MR checks in
// export_wysiwyg_test.mjs assert the same four values).
func TestModelRegistryDefaults(t *testing.T) {
	want := map[string]string{
		"anthropic": "claude-haiku-5-5",
		"openai":    "gpt-6-luna",
		"deepseek":  "deepseek-flash",
		"xai":       "grok-4.7",
	}
	for provider, model := range want {
		if got := providers[provider].DefaultModel; got != model {
			t.Errorf("providers[%s].DefaultModel = %q, want %q", provider, got, model)
		}
	}
	// openrouter takes an OpenRouter slug, not a DeepSeek API id.
	if got := providers["openrouter"].DefaultModel; got != "deepseek/deepseek-chat" {
		t.Errorf("providers[openrouter].DefaultModel = %q, want unchanged", got)
	}
}

// TestThinkingDisabledOnlyForDeepseek captures the outgoing request body.
// DeepSeek defaults to thinking mode and the measured live calls ran into
// llmTimeout, so deepseek must send thinking disabled; no other provider
// (openrouter included, even with a deepseek/* slug) may carry the field.
func TestThinkingDisabledOnlyForDeepseek(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     bool
	}{
		{"deepseek", "", true},
		{"openai", "", false},
		{"openrouter", "deepseek/deepseek-chat", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			var got string
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got = string(b)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}}]}`))
			}))
			defer llm.Close()
			useFakeProvider(t, tc.provider, llm.URL)
			if _, err := llmSynthesize(context.Background(), tc.provider, "k", tc.model, "consensus", []string{"c"}, consensusJSON(t, 1)); err != nil {
				t.Fatalf("llmSynthesize: %v", err)
			}
			has := strings.Contains(got, `"thinking":{"type":"disabled"}`)
			if tc.want && !has {
				t.Errorf("%s body lacks thinking disabled: %s", tc.provider, got)
			}
			if !tc.want && strings.Contains(got, `"thinking"`) {
				t.Errorf("%s body must have no thinking key: %s", tc.provider, got)
			}
		})
	}
}

// TestReasoningEffortOnlyForGemini: Gemini 3.x cannot disable thinking on the
// OpenAI-compat endpoint, but reasoning_effort "low" is accepted. Only gemini
// may carry the key.
func TestReasoningEffortOnlyForGemini(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     bool
	}{
		{"gemini", "", true},
		{"deepseek", "", false},
		{"openai", "", false},
		{"openrouter", "deepseek/deepseek-chat", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			var got string
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got = string(b)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}}]}`))
			}))
			defer llm.Close()
			useFakeProvider(t, tc.provider, llm.URL)
			if _, err := llmSynthesize(context.Background(), tc.provider, "k", tc.model, "consensus", []string{"c"}, consensusJSON(t, 1)); err != nil {
				t.Fatalf("llmSynthesize: %v", err)
			}
			has := strings.Contains(got, `"reasoning_effort":"low"`)
			if tc.want && !has {
				t.Errorf("%s body lacks reasoning_effort low: %s", tc.provider, got)
			}
			if !tc.want && strings.Contains(got, `"reasoning_effort"`) {
				t.Errorf("%s body must have no reasoning_effort key: %s", tc.provider, got)
			}
			if strings.Contains(got, "thinking_config") {
				t.Errorf("%s body must not carry thinking_config: %s", tc.provider, got)
			}
		})
	}
}

// TestAnthropicTemperatureOmittedForClaude55: the Claude 5.5 family answers
// HTTP 400 "`temperature` is deprecated for this model", so the key must be
// absent from the body (not 0). Older Anthropic models and other providers
// keep sending it.
func TestAnthropicTemperatureOmittedForClaude55(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     bool
	}{
		{"anthropic", "claude-haiku-5-5", false},
		{"anthropic", "claude-sonnet-5-5", false},
		{"anthropic", "claude-opus-5-5", false},
		{"anthropic", "claude-haiku-4-5", true},
		{"deepseek", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			var got string
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got = string(b)
				w.Header().Set("Content-Type", "application/json")
				if tc.provider == "anthropic" {
					_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}}]}`))
			}))
			defer llm.Close()
			if tc.provider == "anthropic" {
				prev := providers["anthropic"]
				providers["anthropic"] = providerSpec{BaseURL: llm.URL, DefaultModel: prev.DefaultModel, Style: styleAnthropic}
				t.Cleanup(func() { providers["anthropic"] = prev })
			} else {
				useFakeProvider(t, tc.provider, llm.URL)
			}
			if _, err := llmSynthesize(context.Background(), tc.provider, "k", tc.model, "consensus", []string{"c"}, consensusJSON(t, 1)); err != nil {
				t.Fatalf("llmSynthesize: %v", err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal([]byte(got), &body); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, got)
			}
			raw, has := body["temperature"]
			if has != tc.want {
				t.Fatalf("%s/%s: temperature present=%v, want %v: %s", tc.provider, tc.model, has, tc.want, got)
			}
			if tc.want && string(raw) != "0" {
				t.Errorf("temperature = %s, want 0", raw)
			}
		})
	}
}
