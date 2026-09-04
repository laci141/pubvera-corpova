// providers.go — the in-process BYOK LLM layer.
//
// The vendored CLI is purely heuristic/keyless: it never reads a provider key
// and never calls an LLM. All LLM work therefore happens HERE, in the web
// layer, as a post-processing step: the CLI's JSON output plus the user's
// claim(s) are sent in ONE chat-completions call to the caller-selected
// provider, which returns a structured synthesis (stance, confidence,
// reasoning, key evidence points, and now an explicit list of studies it
// judged off-topic or methodologically too weak to count). The CLI result is
// always returned verbatim; an LLM failure degrades to the heuristic result
// plus a redacted llm_error.
//
// SECURITY MODEL (same rules as main.go, do not weaken):
//   - The key lives in memory for one request and goes into exactly one
//     outbound Authorization/x-api-key header over HTTPS. Never logged,
//     never persisted, never placed in any environment.
//   - Every error string that could contain a provider response body passes
//     through redact() (exact-key removal) plus control-byte stripping and
//     truncation before it reaches a client or a log line.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// llmTimeout bounds the single synthesis call; it must fit inside the 120s
// request budget in runCLIJSON with room for the CLI run that precedes it.
const llmTimeout = 60 * time.Second

// authStyle selects how the key is presented and which wire format is used.
type authStyle int

const (
	styleOpenAI    authStyle = iota // POST {base}/chat/completions, Authorization: Bearer
	styleAnthropic                  // POST {base}/messages, x-api-key + anthropic-version
)

// providerSpec describes one BYOK provider. BaseURL is the API root WITHOUT
// the /chat/completions (or /messages) suffix; DefaultModel is used when the
// caller sends no model override.
type providerSpec struct {
	BaseURL      string
	DefaultModel string
	Style        authStyle
	// JSONFormat requests JSON-object output via the OpenAI-wire
	// response_format parameter. Only meaningful for styleOpenAI providers;
	// enabled only where the endpoint is known to accept it (openrouter).
	JSONFormat bool
}

// providers is the full BYOK registry. Everything except anthropic speaks the
// OpenAI chat-completions format; gemini via Google's OpenAI-compatibility
// endpoint, qwen via DashScope's international compatible-mode endpoint.
// openrouter is a meta-provider: its model string selects any hosted model
// (including :free ones), so the UI treats model as effectively required there.
var providers = map[string]providerSpec{
	"anthropic":  {"https://api.anthropic.com/v1", "claude-haiku-4-5", styleAnthropic, false},
	"openai":     {"https://api.openai.com/v1", "gpt-5-mini", styleOpenAI, false},
	"gemini":     {"https://generativelanguage.googleapis.com/v1beta/openai", "gemini-3.5-flash", styleOpenAI, false},
	"groq":       {"https://api.groq.com/openai/v1", "llama-3.3-70b-versatile", styleOpenAI, false},
	"mistral":    {"https://api.mistral.ai/v1", "mistral-small-latest", styleOpenAI, false},
	"deepseek":   {"https://api.deepseek.com", "deepseek-chat", styleOpenAI, false},
	"zai":        {"https://api.z.ai/api/paas/v4", "glm-5", styleOpenAI, false},
	"moonshot":   {"https://api.moonshot.ai/v1", "kimi-k2.6", styleOpenAI, false},
	"qwen":       {"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "qwen3-max", styleOpenAI, false},
	"minimax":    {"https://api.minimax.io/v1", "MiniMax-M2.7", styleOpenAI, false},
	"xai":        {"https://api.x.ai/v1", "grok-4-fast", styleOpenAI, false},
	"openrouter": {"https://openrouter.ai/api/v1", "deepseek/deepseek-chat", styleOpenAI, true},
}

// deterministicProviders are the providers known to accept temperature 0.
//
// The parameter is not sent to anything else, and that is the whole point of
// keeping this list rather than a field on providerSpec: OpenAI's reasoning
// models reject any temperature other than the default, so a value sent
// blindly would turn a working provider into a hard error. A provider absent
// here gets exactly the request bytes it got before this list existed.
//
// Temperature 0 reduces run-to-run variation; it does not remove it. Providers
// batch requests and sum floating point in whatever order the batch happens to
// take, so the same question can still come back with a different verdict.
// Measured on 2026-08-25: three identical deepseek-chat runs over the same 98
// studies returned "mixed" once and "refutes" twice, while the heuristic score
// was identical every time.
var deterministicProviders = map[string]bool{
	"anthropic": true,
	"deepseek":  true,
}

// llmFailureAdvice names what the user should do about a provider failure.
// The raw provider text follows it and is what makes a bug report useful,
// but on its own it never says whether to retry, switch model, or check a
// key. Measured against the live Gemini API: 503 is transient and common,
// and the correct advice there is simply to try again.
//
// An unrecognised status returns the empty string, leaving the message
// exactly as it was before this function existed.
func llmFailureAdvice(status int) string {
	switch status {
	case 401, 403:
		return "Check your API key. "
	case 404:
		return "That model is not available — pick another from the list. "
	case 429:
		return "Rate limit reached — wait a moment and try again. "
	case 503:
		return "Provider overloaded — try again in a few minutes. "
	}
	return ""
}

// supportedProviders is the sorted name list used in error messages.
var supportedProviders = func() string {
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}()

// excludedStudy is one study the LLM judged should not count toward the
// verdict, with a short human-readable reason (off-topic, wrong subject,
// animal/in-vitro only, etc.). Title mirrors the CLI study title so the UI can
// match it against the displayed cards.
type excludedStudy struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// llmSynthesis is the structured post-processing verdict returned to clients
// under "llm_synthesis". KeyEvidence holds 3-5 points referencing the CLI data.
// ExcludedStudies lists the studies the model set aside as irrelevant or too
// methodologically weak, so the filtering is transparent rather than silent.
type llmSynthesis struct {
	Stance          string          `json:"stance"` // supports | refutes | mixed | insufficient
	Confidence      float64         `json:"confidence"`
	Reasoning       string          `json:"reasoning"`
	KeyEvidence     []string        `json:"key_evidence"`
	ExcludedStudies []excludedStudy `json:"excluded_studies"`
	Model           string          `json:"model"`
	// InputTokens and OutputTokens are the provider's own counts for this
	// call, carried out so the caller can price the request against the
	// user's budget. Zero when the provider omitted a usage block.
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// maxCLIJSONForPrompt caps how much CLI output is embedded in the prompt so
// large study lists cannot blow the model's context window. Raised from 24KB
// to 56KB so the model sees more of the study list and can judge relevance on
// more titles; still well inside typical context limits.
const maxCLIJSONForPrompt = 56 * 1024

// maxStudiesForLLM caps how many all_studies entries the LLM sees — bump this
// one line to widen the LLM's view. The list is relevance-ordered, so the trim
// keeps the most relevant studies; with ≤1500-char abstracts, 25 entries stay
// comfortably inside maxCLIJSONForPrompt. Without the trim, the naive byte cap
// cut the all_studies array mid-JSON (it is the LAST field of the CLI output),
// silently dropping exactly the data the LLM needs most while keeping the
// duplicated top_supporting/top_refuting abstracts.
const maxStudiesForLLM = 25

// maxStudiesForCompare is the per-claim cap for compare output: compare packs
// 2 claims into one prompt, so use a smaller per-claim cap to stay under the
// byte cap (2 × ~12 studies with ≤1500-char abstracts ≈ ~45KB, inside the 56KB
// maxCLIJSONForPrompt; 2 × 25 would exceed it and get cut mid-JSON).
const maxStudiesForCompare = 12

// compactForLLM shrinks the CLI JSON before it is embedded in the prompt.
// Wherever an object carries an "all_studies" array (consensus output, and the
// claim_a/claim_b sub-objects of compare output), the array is trimmed and the
// top_supporting/top_refuting lists are dropped — all_studies supersedes them,
// and keeping both would send each top study's abstract twice. The trim cap
// depends on the shape: compare output (a top-level claim_a/claim_b key) uses
// the smaller maxStudiesForCompare per claim so both study lists fit under
// maxCLIJSONForPrompt together; everything else uses maxStudiesForLLM. Output
// without all_studies (evidence, gaps, controversies, or an older CLI binary)
// is returned unchanged, as is anything that fails to parse. Only the LLM's
// copy is compacted; the client always receives the CLI JSON verbatim.
func compactForLLM(raw []byte) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	maxStudies := maxStudiesForLLM
	if _, isCompare := obj["claim_a"]; isCompare {
		maxStudies = maxStudiesForCompare
	} else if _, isCompare := obj["claim_b"]; isCompare {
		maxStudies = maxStudiesForCompare
	}
	if !compactStudyObject(obj, maxStudies) {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// compactStudyObject applies the all_studies trim (to maxStudies entries) +
// top-list removal to one object and recurses into compare's claim_a/claim_b
// sub-objects with the same cap. Returns true when anything changed.
func compactStudyObject(obj map[string]json.RawMessage, maxStudies int) bool {
	changed := false
	if rawList, ok := obj["all_studies"]; ok {
		var list []json.RawMessage
		if err := json.Unmarshal(rawList, &list); err == nil {
			if len(list) > maxStudies {
				if trimmed, err := json.Marshal(list[:maxStudies]); err == nil {
					obj["all_studies"] = trimmed
				}
			}
			delete(obj, "top_supporting")
			delete(obj, "top_refuting")
			changed = true
		}
	}
	for _, k := range []string{"claim_a", "claim_b"} {
		sub, ok := obj[k]
		if !ok {
			continue
		}
		var subObj map[string]json.RawMessage
		if err := json.Unmarshal(sub, &subObj); err != nil {
			continue
		}
		if compactStudyObject(subObj, maxStudies) {
			if enc, err := json.Marshal(subObj); err == nil {
				obj[k] = enc
				changed = true
			}
		}
	}
	return changed
}

// llmStudyCount reports how many all_studies entries survive compaction, for
// the pre-call log line only: it recounts on compactForLLM's output rather
// than threading a count out of synthesisPrompt, so no signatures change.
// Returns -1 when the output carries no all_studies array at all (evidence,
// gaps, controversies, or an older CLI binary).
func llmStudyCount(raw []byte) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(compactForLLM(raw), &obj); err != nil {
		return -1
	}
	total, found := 0, false
	count := func(o map[string]json.RawMessage) {
		var list []json.RawMessage
		if rawList, ok := o["all_studies"]; ok && json.Unmarshal(rawList, &list) == nil {
			total += len(list)
			found = true
		}
	}
	count(obj)
	for _, k := range []string{"claim_a", "claim_b"} {
		var sub map[string]json.RawMessage
		if rawSub, ok := obj[k]; ok && json.Unmarshal(rawSub, &sub) == nil {
			count(sub)
		}
	}
	if !found {
		return -1
	}
	return total
}

// synthesisPrompt builds the single user message sent to the LLM. It now asks
// the model to act as a strict relevance/quality filter: examine each study by
// abstract content (title as fallback), set aside off-topic or methodologically
// weak entries, base the verdict only on what genuinely bears on the claim, and
// report what it excluded. The CLI JSON is compacted first (compactForLLM) so
// the LLM works from the relevance-ordered all_studies list when present.
func synthesisPrompt(endpoint string, claims []string, cliJSON []byte) string {
	cliJSON = compactForLLM(cliJSON)
	truncated := false
	if len(cliJSON) > maxCLIJSONForPrompt {
		cliJSON = cliJSON[:maxCLIJSONForPrompt]
		truncated = true
	}
	var b strings.Builder
	b.WriteString("You are a rigorous evidence-synthesis assistant. Below is the JSON output of a scientific-literature analysis tool (command: " + endpoint + ") for the claim(s):\n")
	for i, c := range claims {
		fmt.Fprintf(&b, "CLAIM %d: %s\n", i+1, c)
	}
	b.WriteString("\nTOOL OUTPUT (may be truncated):\n")
	b.Write(cliJSON)
	if truncated {
		b.WriteString("\n[NOTE: tool output was truncated; some studies may not be shown.]")
	}
	b.WriteString("\n\nIMPORTANT — the tool matched studies by keyword and did NOT verify that each study is actually about the claim. When an \"all_studies\" array is present, it is the complete analyzed study list: ordered by search relevance (NOT by citation count), capped to the most relevant entries, each with an \"abstract\" field when the source provides one. Before forming a verdict, act as a strict filter:\n" +
		"1. Examine each study by its abstract when present — judge relevance and study design from the abstract's content, not from the title alone (title only when the abstract is empty).\n" +
		"2. EXCLUDE a study when it is clearly off-topic (not about the claim's specific subject and outcome), when it studies a different substance, or when its design cannot support the claim about humans (e.g. animal-only or in-vitro/cell-culture studies used to assert a human body-weight or clinical effect).\n" +
		"3. The \"design\" field is a MACHINE-ASSIGNED label and is NOT authoritative. When the study's own title or abstract names a different design (e.g. the title contains \"case control\", \"cohort\", \"cross-sectional\", \"survey\", \"case series\", or \"observational\"), the title and abstract WIN over the \"design\" field. A study nested inside a randomized trial is not itself an RCT unless the abstract says the reported comparison was randomized. Never state or imply a study's design in reasoning or key_evidence unless the abstract or title supports that word.\n" +
		"4. Base your stance, confidence, and key_evidence ONLY on the studies that remain after exclusion. Prefer higher-tier human evidence (meta-analyses, systematic reviews, RCTs) over observational or mechanistic studies, and note reverse-causality limits for observational data where relevant.\n" +
		"5. If too few genuinely relevant studies remain, say so and use stance \"insufficient\".\n" +
		"6. When the tool output contains TWO claims (a comparison with claim_a and claim_b), apply steps 1-4 EQUALLY and INDEPENDENTLY to BOTH claims' study lists: examine every study under each claim with the same rigor, and put the studies you exclude from EITHER claim together into the single flat excluded_studies array. Do not neglect the second claim's studies.\n\n" +
		"Respond with ONLY a JSON object, no markdown fences, with exactly these fields:\n" +
		`{"stance":"supports|refutes|mixed|insufficient","confidence":0.0,"reasoning":"2-4 sentence synthesis based only on the studies you kept","key_evidence":["3-5 short points, each referencing specific numbers or studies you kept"],"excluded_studies":[{"title":"study title copied from the tool output","reason":"short reason, e.g. off-topic: about X not the claim / animal model only / in-vitro only / different substance"}]}` + "\n" +
		"stance is your overall verdict on claim 1 (for comparisons, weigh both claims and explain in reasoning). confidence is 0-1. Leave excluded_studies as [] only if every study is genuinely relevant. " +
		"Never attribute an outcome to a study that its abstract does not report measuring. " +
		"excluded_studies must contain ONLY studies you actually discarded and did NOT use for your stance, confidence, or key_evidence; a study you cite as evidence must never appear there, and lower evidence tier is never by itself a reason to exclude. " +
		"If you want to note a study's limitations while still using it, put that in reasoning, not in excluded_studies.")
	return b.String()
}

// openAIRequest / anthropicRequest are the minimal wire shapes. Temperature is
// sent only to the providers in deterministicProviders — some others reject
// any non-default value — and is a pointer so it disappears from the request
// for everyone else.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// responseFormat is the OpenAI-wire response_format parameter. Sent only when
// providerSpec.JSONFormat is set; the nil pointer keeps the wire bytes of every
// other provider's request identical to before the field existed.
type responseFormat struct {
	Type string `json:"type"`
}

type openAIRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// A pointer, so omitempty leaves the field out entirely for providers not
	// in deterministicProviders. A plain float64 would serialise as 0 for
	// everyone and send the very value some of them refuse.
	Temperature    *float64        `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type anthropicRequest struct {
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature *float64      `json:"temperature,omitempty"`
	Messages    []chatMessage `json:"messages"`
}

// llmSynthesize makes one chat call to the selected provider and parses the
// structured synthesis. Every returned error is already safe to expose: key
// redacted, control bytes stripped, body truncated.
func llmSynthesize(ctx context.Context, provider, key, model, endpoint string, claims []string, cliJSON []byte) (*llmSynthesis, error) {
	spec, ok := providers[provider]
	if !ok { // callers validate first; belt and braces
		return nil, errors.New("unknown provider")
	}
	if model == "" {
		model = spec.DefaultModel
	}
	prompt := synthesisPrompt(endpoint, claims, cliJSON)

	var url string
	var payload any

	// Nil for every provider outside deterministicProviders, so omitempty
	// leaves the field out and their request bytes stay exactly as before.
	// This sits ahead of the switch because Go allows only case clauses
	// directly inside one.
	var temp *float64
	if deterministicProviders[provider] {
		zero := 0.0
		temp = &zero
	}

	switch spec.Style {
	case styleAnthropic:
		url = spec.BaseURL + "/messages"
		// 1024 truncated the more verbose models mid-object: the response ended
		// with stop_reason "max_tokens", parseSynthesis found a stray closing
		// brace, and the whole synthesis was discarded as unparseable. This is a
		// ceiling, not a target — a short answer still costs only what it uses.
		payload = anthropicRequest{Model: model, MaxTokens: 4096, Temperature: temp, Messages: []chatMessage{{Role: "user", Content: prompt}}}
	default:
		url = spec.BaseURL + "/chat/completions"
		reqPayload := openAIRequest{Model: model, Temperature: temp, Messages: []chatMessage{{Role: "user", Content: prompt}}}
		if spec.JSONFormat {
			reqPayload.ResponseFormat = &responseFormat{Type: "json_object"}
		}
		payload = reqPayload
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build request: " + sanitizeLLMError(err.Error(), key))
	}
	req.Header.Set("Content-Type", "application/json")
	if spec.Style == styleAnthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// Log lines carry only provider/model names, sizes, counts, and
	// key-redacted error strings — never the key, a header, the prompt, the
	// payload, or claim text.
	log.Printf("llm: call provider=%s model=%s payload_bytes=%d studies=%d",
		provider, model, len(body), llmStudyCount(cliJSON))
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Transport errors can embed the URL but never the key (it travels in a
		// header); sanitize anyway.
		// A deadline is not an HTTP status: it arrives here as a transport
		// error. Detected with errors.Is, not by matching the error text.
		lead := ""
		if errors.Is(err, context.DeadlineExceeded) {
			lead = "The model did not answer in time — try again, or pick another model. "
		}
		msg := lead + "request failed: " + sanitizeLLMError(err.Error(), key)
		log.Printf("llm: fail provider=%s elapsed_ms=%d err=%s",
			provider, time.Since(start).Milliseconds(), truncate(msg, 300))
		return nil, errors.New(msg)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		msg := "read response: " + sanitizeLLMError(err.Error(), key)
		log.Printf("llm: fail provider=%s elapsed_ms=%d err=%s",
			provider, time.Since(start).Milliseconds(), truncate(msg, 300))
		return nil, errors.New(msg)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := llmFailureAdvice(resp.StatusCode) +
			fmt.Sprintf("provider returned HTTP %d: %s", resp.StatusCode, sanitizeLLMError(string(respBody), key))
		log.Printf("llm: fail provider=%s elapsed_ms=%d err=%s",
			provider, time.Since(start).Milliseconds(), truncate(msg, 300))
		return nil, errors.New(msg)
	}

	text, err := extractChatText(spec.Style, respBody)
	if err != nil {
		msg := sanitizeLLMError(err.Error(), key)
		log.Printf("llm: badshape provider=%s elapsed_ms=%d resp_bytes=%d err=%s",
			provider, time.Since(start).Milliseconds(), len(respBody), truncate(msg, 300))
		return nil, errors.New(msg)
	}
	syn, err := parseSynthesis(text)
	if err != nil {
		msg := "unparseable synthesis: " + sanitizeLLMError(err.Error(), key)
		log.Printf("llm: badshape provider=%s elapsed_ms=%d resp_bytes=%d err=%s",
			provider, time.Since(start).Milliseconds(), len(respBody), truncate(msg, 300))
		return nil, errors.New(msg)
	}
	syn.Model = model
	usage := extractUsage(spec.Style, respBody)
	syn.InputTokens = usage.InputTokens
	syn.OutputTokens = usage.OutputTokens
	log.Printf("llm: ok provider=%s model=%s elapsed_ms=%d resp_bytes=%d in_tok=%d out_tok=%d",
		provider, model, time.Since(start).Milliseconds(), len(respBody),
		usage.InputTokens, usage.OutputTokens)
	return syn, nil
}

// extractChatText pulls the assistant text out of the provider response.
func extractChatText(style authStyle, body []byte) (string, error) {
	if style == styleAnthropic {
		var r struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return "", errors.New("invalid provider response JSON")
		}
		for _, c := range r.Content {
			if c.Type == "text" && c.Text != "" {
				return c.Text, nil
			}
		}
		return "", errors.New("provider response contained no text content")
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", errors.New("invalid provider response JSON")
	}
	if len(r.Choices) == 0 || r.Choices[0].Message.Content == "" {
		return "", errors.New("provider response contained no choices")
	}
	return r.Choices[0].Message.Content, nil
}

// tokenUsage carries the provider-reported token counts for one call. Both
// API styles report them, under different names; extractUsage normalises to
// one shape so the caller does not branch on style a second time.
type tokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// extractUsage pulls the token counts out of a provider response. Failure is
// not an error: a missing or malformed usage block yields a zero-value
// tokenUsage, and a zero-cost log line is better than a dropped analysis.
func extractUsage(style authStyle, body []byte) tokenUsage {
	if style == styleAnthropic {
		var r struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return tokenUsage{}
		}
		return tokenUsage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	}
	var r struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return tokenUsage{}
	}
	return tokenUsage{InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens}
}

// parseSynthesis parses the model's JSON verdict, tolerating markdown fences
// and surrounding prose, then normalizes the fields.
func parseSynthesis(text string) (*llmSynthesis, error) {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return nil, errors.New("no JSON object in model output")
	}
	var syn llmSynthesis
	if err := json.Unmarshal([]byte(text[start:end+1]), &syn); err != nil {
		return nil, errors.New("model output is not valid JSON")
	}
	switch syn.Stance {
	case "supports", "refutes", "mixed", "insufficient":
	default:
		syn.Stance = "insufficient"
	}
	if syn.Confidence < 0 {
		syn.Confidence = 0
	} else if syn.Confidence > 1 {
		syn.Confidence = 1
	}
	if len(syn.KeyEvidence) > 5 {
		syn.KeyEvidence = syn.KeyEvidence[:5]
	}
	// Normalize excluded studies: drop entries with an empty title, trim fields,
	// and cap the list so a runaway model response can't bloat the payload.
	cleaned := make([]excludedStudy, 0, len(syn.ExcludedStudies))
	for _, e := range syn.ExcludedStudies {
		t := strings.TrimSpace(e.Title)
		if t == "" {
			continue
		}
		r := strings.TrimSpace(e.Reason)
		// A reason that says the study was kept is not an exclusion. The prompt
		// already forbids this ("a study you cite as evidence must never appear
		// there"), and the model still does it: on a statins comparison it filed
		// the West of Scotland trial — which its own key_evidence quotes for a
		// 31% risk reduction — with the reason "This is a key RCT for statin
		// claim, kept." The UI takes excluded_studies at its word and pulls the
		// work out of the supporting column, so the strongest evidence for the
		// claim disappeared from the screen while remaining in the summary above
		// it.
		//
		// Dropping the entry restores the study rather than removing it: it stays
		// in the columns, which is what the reason itself asked for. An
		// instruction the model ignores needs an answer that does not depend on
		// the model following it.
		if selfContradictingExclusion(r) {
			continue
		}
		if len(t) > 300 {
			t = t[:300]
		}
		if len(r) > 200 {
			r = r[:200]
		}
		cleaned = append(cleaned, excludedStudy{Title: t, Reason: r})
		if len(cleaned) >= 20 {
			break
		}
	}
	syn.ExcludedStudies = cleaned
	return &syn, nil
}

// selfContradictingExclusion reports whether an exclusion reason states that the
// study was kept. Matching is on the reason text because that is where the model
// contradicts itself; the alternative — cross-checking every excluded title
// against reasoning and key_evidence — depends on the model spelling the title
// the same way twice, which it does not.
//
// The vocabulary is deliberately narrow. Measured against the eleven reasons
// from the statins/fasting comparison that produced this bug, it flags the five
// that say kept and none of the six genuine exclusions, which use excluded,
// off-topic, animal model, or different substance. A wider pattern would start
// catching "not used", which is what a real exclusion says.
var keptAnyway = regexp.MustCompile(`(?i)\b(kept|keeping|retained|still used)\b`)

func selfContradictingExclusion(reason string) bool {
	return keptAnyway.MatchString(reason)
}

// sanitizeLLMError makes an upstream diagnostic safe for clients and logs:
// exact key redaction, control bytes stripped, hard length cap.
func sanitizeLLMError(s, key string) string {
	s = redact(s, key)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' {
			return ' '
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return strings.TrimSpace(s)
}

// validateModel enforces the opaque-token rules for the caller-supplied model
// override: trimmed, ≤128 chars, no whitespace or control characters. Returns
// the normalized model and "" on success, or an error message (which never
// echoes the value — the model field is treated as sensitive-adjacent input).
func validateModel(model string) (string, string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", ""
	}
	if len(model) > 128 {
		return "", "model must be at most 128 characters"
	}
	for _, r := range model {
		if r <= 0x20 || r == 0x7f {
			return "", "model must not contain whitespace or control characters"
		}
	}
	return model, ""
}
