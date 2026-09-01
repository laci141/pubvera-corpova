// Command scientific-consensus-web is a thin standalone HTTP wrapper around the
// scientific-consensus CLI. Each /api endpoint runs the compiled CLI (always
// keyless/heuristic — the CLI never uses an LLM) for one cold key at a time:
// a cache hit skips the run (cache.go) and concurrent callers of the same key
// share one (singleflight.go). Then, when a key is available for the request,
// makes ONE in-process chat-completions call (providers.go) to synthesize the
// CLI output into a structured verdict.
//
// Two key sources, never mixed:
//   - BYOK: the caller's own key, from the X-LLM-Key header. This is the only
//     source for free, pro and max, and remains available to everyone.
//   - The server's own DEEPSEEK_API_KEY, handed ONLY to trial-tier callers who
//     supplied no key of their own. The operator pays for those calls, so
//     tier.go pins them to one model.
//
// SECURITY MODEL (enforced below and in providers.go, do not weaken):
//   - A key lives in memory only for the duration of one request and one
//     outbound HTTPS call.
//   - The key is NEVER logged, printed, persisted, or passed to the child CLI.
//     buildChildEnv() strips every known provider key out of os.Environ() —
//     INCLUDING the server's own DEEPSEEK_API_KEY, which is why that variable
//     must stay listed in allProviderEnvVars.
//   - Any CLI stderr or LLM provider diagnostic surfaced to the client passes
//     through redact()/sanitizeLLMError(), which remove the key substring so a
//     key echoed in an error can never escape. redact() is given b.key, so a
//     server key is redacted on exactly the same path as a caller's key.
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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cliBinary is the compiled scientific-consensus CLI this server shells out to.
// Overridable with CLI_BIN. Otherwise it defaults to bin/scientific-consensus-pp-cli
// (plus a .exe suffix on Windows), so the same code runs against a Windows-built
// binary locally and a Linux-built binary inside the Docker/Render container.
func cliBinaryPath() string {
	if p := strings.TrimSpace(os.Getenv("CLI_BIN")); p != "" {
		return p
	}
	name := "scientific-consensus-pp-cli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join("bin", name)
}

// serverKeyEnvVar holds the operator's own DeepSeek key. It is the ONE key this
// process may spend on a caller's behalf, and only for the trial tier. Empty or
// unset simply means the fallback does not exist — every caller is then BYOK,
// which is the pre-trial behaviour.
const serverKeyEnvVar = "DEEPSEEK_API_KEY"

// allProviderEnvVars is every provider key env var a CLI might conceivably
// read. buildChildEnv strips ALL of them from the inherited environment so the
// child never sees any provider key — the child is always keyless; LLM calls
// happen in-process (providers.go).
//
// serverKeyEnvVar is on this list deliberately. It is the one variable this
// process is EXPECTED to have set, which makes it the most likely to leak into
// a child; being present in the server's own environment is precisely why it
// must be stripped from the child's.
var allProviderEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GEMINI_API_KEY",
	"GROQ_API_KEY",
	"MISTRAL_API_KEY",
	serverKeyEnvVar,
}

// consensusResponse wraps the CLI's raw JSON verbatim. stance_source is
// "llm:<provider>" when an LLM synthesis succeeded, otherwise "heuristic"
// (no key supplied, or the LLM call failed — then llm_error says why, already
// redacted). llm_synthesis carries the structured verdict on success. With no
// key the response is byte-identical in shape to the pre-LLM version:
// {"stance_source":"heuristic","result":...}.
type consensusResponse struct {
	StanceSource string        `json:"stance_source"`
	LLMSynthesis *llmSynthesis `json:"llm_synthesis,omitempty"`
	LLMError     string        `json:"llm_error,omitempty"`
	// ServerKey reports that this answer was produced with the SERVER's own
	// provider key (the trial fallback in extractBYOK), not with a key the
	// caller supplied. It exists so the UI can name the model that actually
	// ran: the fallback rewrites the provider to deepseek, so a trial caller
	// who picked something else in the dropdown was answered by a different
	// model than the one on screen. Set inside the LLMSynthesis block below,
	// so it is true only when an LLM really ran — /api/gaps and /api/evidence
	// call no provider at all and must not report a model they never used.
	// omitempty keeps it off every BYOK response: absent, never false.
	ServerKey bool `json:"server_key,omitempty"`
	// Divergence reports that the keyless heuristic verdict and the LLM
	// synthesis reached different conclusions, so the two blocks the UI shows
	// cannot both be right. It is false whenever they agree and false whenever
	// there is no synthesis to compare against. DivergenceReason names which
	// axis fired; it is empty when Divergence is false.
	Divergence       bool            `json:"divergence"`
	DivergenceReason string          `json:"divergence_reason,omitempty"`
	Result           json.RawMessage `json:"result"`
}

// consensusFacts is the minimal slice of the CLI's consensus JSON the web layer
// needs in order to compare the heuristic result against the LLM synthesis.
// Decoding only these fields keeps the CLI output opaque and forward-compatible
// — the full result is still passed through verbatim.
type consensusFacts struct {
	Verdict  string `json:"verdict"`
	Refuting int    `json:"refuting"`
	Mixed    int    `json:"mixed"`
}

// verdictDirection maps the CLI's heuristic verdict onto a direction:
// +1 supports, -1 refutes, 0 for every non-directional verdict (mixed,
// inconclusive, insufficient-evidence).
func verdictDirection(v string) int {
	switch v {
	case "evidence-supports":
		return 1
	case "evidence-refutes":
		return -1
	default:
		return 0
	}
}

// stanceDirection maps the LLM synthesis stance onto the same scale as
// verdictDirection, so the two can be compared directly. "mixed" and
// "insufficient" are non-directional and map to 0.
func stanceDirection(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "supports":
		return 1
	case "refutes":
		return -1
	default:
		return 0
	}
}

// divergenceFlag reports whether the keyless heuristic and the LLM synthesis
// disagree, on either of two axes, and names the axis that fired.
//
//   - DIRECTION: the heuristic verdict and the LLM stance point different ways.
//     Includes the case where one is directional and the other is not (the
//     heuristic says "supports" while the LLM says "insufficient").
//   - CERTAINTY: the LLM reports conflict or insufficiency ("mixed" /
//     "insufficient") while the heuristic saw NO dissent at all — zero refuting
//     and zero mixed studies. The heuristic's apparent unanimity is then an
//     artifact of its lexical classifier, not a property of the literature.
//
// It returns false when the two agree (0.95 vs 1.00 in the same direction is
// agreement, not divergence) and false when there is no synthesis to compare
// against — no key, or the LLM call failed. Unparseable CLI JSON is treated as
// "cannot compare", never as divergence.
func divergenceFlag(cliJSON []byte, syn *llmSynthesis) (bool, string) {
	if syn == nil {
		return false, ""
	}
	var f consensusFacts
	if err := json.Unmarshal(cliJSON, &f); err != nil {
		return false, ""
	}
	if verdictDirection(f.Verdict) != stanceDirection(syn.Stance) {
		return true, fmt.Sprintf("direction: heuristic verdict %q vs AI stance %q", f.Verdict, syn.Stance)
	}
	switch strings.ToLower(strings.TrimSpace(syn.Stance)) {
	case "mixed", "insufficient":
		if f.Refuting == 0 && f.Mixed == 0 {
			return true, fmt.Sprintf("certainty: AI reports %q but the heuristic found no refuting and no mixed studies", syn.Stance)
		}
	}
	return false, ""
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/api/consensus", handleConsensus)
	mux.HandleFunc("/api/evidence", handleEvidence)
	mux.HandleFunc("/api/compare", handleCompare)
	mux.HandleFunc("/api/gaps", handleGaps)
	mux.HandleFunc("/api/controversies", handleControversies)

	// Address resolution, in priority order:
	//   1. $ADDR  — explicit override (host:port), used locally.
	//   2. $PORT  — Render/Heroku convention; bind 0.0.0.0 so the platform can
	//      route external traffic to the container.
	//   3. default 127.0.0.1:8090 for local development.
	addr := "127.0.0.1:8090"
	if a := strings.TrimSpace(os.Getenv("ADDR")); a != "" {
		addr = a
	} else if p := strings.TrimSpace(os.Getenv("PORT")); p != "" {
		addr = "0.0.0.0:" + p
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// The response cache is a SOFT dependency. newRedisCacheFromEnv only reads
	// the environment — it never dials — and probeAsync reports reachability from
	// a goroutine, so a missing, refused or hung Redis can neither delay nor fail
	// startup. A nil cacheStore (no REDIS_ADDR, or CACHE_DISABLED) makes every
	// cache call a no-op, which is exactly the pre-cache behaviour.
	//
	// initCLIEngineHash runs FIRST and synchronously: it stamps the CLI binary's
	// identity into every cache key, so it has to be in place before any key is
	// built. It is a ~50ms local file read that cannot fail the process (an
	// unreadable binary logs and falls back to the manual version), unlike
	// probeAsync it is not a network call, and running it here rather than per
	// request is what keeps a cache hit at ~3ms.
	initCLIEngineHash()
	cacheStore = newRedisCacheFromEnv()
	cacheStore.probeAsync()

	// Presence only — never the value, never a prefix, never a length that could
	// narrow a guess. An operator needs to know whether the trial fallback is
	// armed; nobody reading a log needs anything else about it.
	log.Printf("corpova listening on %s (CLI: %s, trial key: %v)",
		addr, cliBinaryPath(), serverProviderKey() != "")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

// setCORS adds the CORS headers every /api response needs. Browsers refuse
// fetch() responses without these ("Failed to fetch") even same-origin in some
// embed/proxy setups, and error responses need them too or the browser hides
// the JSON error body.
//
// Authorization is in the allowed list because the page now sends a Supabase
// bearer token on /api/* requests (index.html); without it a cross-origin
// preflight would reject the request before it ever reached the handler.
func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-LLM-Key, X-LLM-Provider")
}

// preflight handles the CORS preflight OPTIONS request. Returns true when the
// request was a preflight and has been fully answered.
func preflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	w.WriteHeader(http.StatusOK)
	return true
}

// browserConfig is the bootstrap payload /config.json hands to the page so it
// can build its Supabase client. SupabaseAnonKey is the PUBLISHABLE
// (browser-side) key, never the secret one: it is designed to be visible in a
// browser and Row Level Security is what protects the data. It is still never
// logged — the security model at the top of this file applies to it too.
type browserConfig struct {
	SupabaseURL     string `json:"supabase_url"`
	SupabaseAnonKey string `json:"supabase_anon_key"`
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/config.json":
		// Deliberately NOT under /api/*: Caddy protects /api/* with
		// forward_auth, and the page needs this config BEFORE it can sign
		// anyone in. Serving it from /api/ would make the requirement circular
		// and force a special-case exception into the Caddy matcher.
		//
		// A missing variable is not an error. An empty pair with status 200 is
		// a valid answer that puts the page into unauthenticated mode, which is
		// what keeps local development and the current deployment working until
		// the environment is set.
		supaURL := strings.TrimSpace(os.Getenv("SUPABASE_URL"))
		supaKey := strings.TrimSpace(os.Getenv("SUPABASE_PUBLISHABLE_KEY"))
		if supaURL == "" || supaKey == "" {
			supaURL, supaKey = "", ""
		}
		setCORS(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// Never cache: a stale key surviving a key rotation would be hard to
		// diagnose from the browser side.
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(browserConfig{SupabaseURL: supaURL, SupabaseAnonKey: supaKey})
	case "/", "/index.html":
		// Serve the single-page frontend. /api/* responses carry explicit CORS
		// headers (see setCORS) so browser fetch works regardless of origin.
		// Falls back to "ok" (a plain health check) when index.html isn't
		// present next to the binary.
		if data, err := os.ReadFile("index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	case "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	default:
		http.NotFound(w, r)
	}
}

// byok holds the per-request key decision: the validated provider name, the
// key, and the (validated, possibly empty) model override. A zero byok means
// keyless/heuristic.
type byok struct {
	provider string
	key      string
	model    string
	// serverKey is true when key came from the server's own credentials rather
	// than from the caller. The operator pays for those calls, so tier.go pins
	// them to one model regardless of tier. It is never set for a key that
	// arrived in a request header.
	serverKey bool
}

// serverProviderKey returns the operator's own provider key, or "" when none is
// configured. Read per call rather than cached at startup so that restarting
// with the variable set is enough to arm the fallback — there is no second
// place to keep it in sync.
func serverProviderKey() string {
	return strings.TrimSpace(os.Getenv(serverKeyEnvVar))
}

// extractBYOK reads the X-LLM-Key header, resolves the provider (from the
// bodyProvider argument, falling back to the X-LLM-Provider header), and
// validates the optional model override. It returns the byok decision and true
// on success; on a client error it writes the response and returns false so
// the caller stops.
//
// When no key is supplied there are two outcomes. A trial-tier caller falls
// back to the server's own key (the point of the trial: no key to paste). Every
// other tier succeeds with a heuristic (keyless) decision exactly as before —
// free callers in particular are never handed the server's key, so a free
// signup cannot spend the operator's money.
func extractBYOK(w http.ResponseWriter, r *http.Request, bodyProvider, bodyModel string) (byok, bool) {
	// The model override is validated even on keyless requests so a malformed
	// value fails the same way regardless of key presence. Its value is never
	// echoed back or logged.
	model, errMsg := validateModel(bodyModel)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return byok{}, false
	}
	// Key from header only (never from body — a key in a JSON body is easier to
	// accidentally log).
	key := strings.TrimSpace(r.Header.Get("X-LLM-Key"))
	if key == "" {
		// The tier comes from Caddy's forward_auth (copy_headers
		// X-Pubvera-User X-Pubvera-Tier). Absent locally, which is why the
		// comparison is exact: `go run .` must not hand out the server key.
		if strings.TrimSpace(r.Header.Get("X-Pubvera-Tier")) == trialTier {
			if sk := serverProviderKey(); sk != "" {
				// The model override is carried through so the gate sees what
				// the caller actually asked for and can refuse it by name,
				// rather than silently answering with a different model.
				return byok{
					provider:  freeTierProvider,
					key:       sk,
					model:     model,
					serverKey: true,
				}, true
			}
		}
		return byok{}, true
	}
	provider := strings.ToLower(strings.TrimSpace(bodyProvider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(r.Header.Get("X-LLM-Provider")))
	}
	if provider == "" {
		writeError(w, http.StatusBadRequest, "X-LLM-Key supplied but no provider; set \"provider\" in body or X-LLM-Provider header")
		return byok{}, false
	}
	if _, ok := providers[provider]; !ok {
		writeError(w, http.StatusBadRequest, "unknown provider "+quoteToken(provider)+"; supported: "+supportedProviders)
		return byok{}, false
	}
	return byok{provider: provider, key: key, model: model}, true
}

// clampLimit normalizes a caller-supplied --limit into the CLI's accepted range,
// defaulting to def when out of bounds.
func clampLimit(limit, def int) int {
	if limit <= 0 || limit > 200 {
		return def
	}
	return limit
}

// cliPacingArgs is appended to every CLI invocation. OpenAlex 429s shared
// anonymous traffic aggressively; --rate-limit 0.15 (~9 req/min) keeps the CLI
// under that limit instead of tripping its adaptive backoff (which waits 60s
// per retry and blows the request budget). Pacing makes multi-request runs
// exceed the CLI's 60s default internal timeout, so --timeout is raised to
// 100s — still inside this wrapper's 120s request budget.
var cliPacingArgs = []string{"--rate-limit", "0.15", "--timeout", "100s"}

// runCLIJSON runs the CLI with the given argv (subcommand + positional args +
// flags already assembled by the caller) in an always-keyless child, then —
// when a key was available — performs the in-process LLM synthesis over
// the CLI's JSON output and merges it into the response. An LLM failure never
// fails the request: the heuristic result is returned with a redacted
// llm_error. It centralizes the exec, timeouts, key-redaction, and
// JSON-validation shared by every endpoint.
func runCLIJSON(w http.ResponseWriter, r *http.Request, b byok, endpoint string, claims []string, args []string, cacheKey string) {
	// The plan gate runs first — before the CLI child, and therefore before the
	// llmSynthesize call further down. A refused request must not consume an
	// OpenAlex run it will never be allowed to synthesize over, and nothing has
	// been written to the response body yet, so a 403 is still possible here.
	if !enforceTierGate(w, r, b, endpoint) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	// The cache covers the CLI leg ONLY: the child CLI is always keyless, so its
	// payload (OpenAlex search + heuristic scoring) is identical for every caller
	// and safe to share. The LLM synthesis below is per-caller and is never
	// cached — it still runs on every request, hit or miss.
	var raw []byte
	if cacheKey != "" {
		if cached, ok := cacheStore.Get(ctx, cacheKey); ok {
			// A stored payload that is not valid JSON (truncated write, foreign
			// key collision) is treated as a miss rather than served: a cache can
			// only ever make the response faster, never different.
			if json.Valid(cached) {
				raw = cached
			} else {
				log.Printf("cache: entry %s is not valid JSON, recomputing", cacheKey)
			}
		}
	}

	if raw == nil {
		var ok bool
		// The cache write moved INTO the shared run (see runCLIRaw): it belongs
		// to the run that produced the bytes, not to whichever caller happens to
		// still be waiting when it finishes. That is what keeps N concurrent
		// callers at one CLI run AND one cache write instead of N of each.
		raw, ok = runCLIRaw(ctx, w, b, cacheKey, args)
		if !ok {
			return // runCLIRaw already wrote the error response
		}
	}

	resp := consensusResponse{
		StanceSource: "heuristic",
		Result:       json.RawMessage(raw),
	}
	deep := llmWillRun(b, endpoint)
	if deep {
		syn, err := llmSynthesize(ctx, b.provider, b.key, b.model, endpoint, claims, raw)
		if err != nil {
			// Already sanitized/redacted by providers.go; safe for client + log-free.
			resp.LLMError = err.Error()
		} else {
			resp.LLMSynthesis = syn
			resp.StanceSource = "llm:" + b.provider
		}
	}
	// Divergence is only defined for the single-claim consensus shape; compare
	// nests two results under claim_a/claim_b and would decode as an empty
	// consensusFacts, so it is deliberately not flagged here.
	if endpoint == "consensus" {
		resp.Divergence, resp.DivergenceReason = divergenceFlag(raw, resp.LLMSynthesis)
	}
	// Usage is recorded HERE and nowhere earlier: reaching this line means the
	// CLI ran (or a cache hit served its bytes) and the JSON is valid — every
	// failure path returned before it. Quota is checked before a query runs and
	// recorded after it succeeds, two separate calls on purpose, so a query that
	// fails never consumes the caller's allowance.
	kind := "base"
	if deep {
		kind = "deep"
	}
	// The model and token counts travel with the record so the auth service can
	// price the query. They come from resp.LLMSynthesis rather than a local
	// variable because the synthesis is what survived the deep branch: a failed
	// or skipped LLM call leaves it nil, and then there is nothing to price.
	var model string
	var inTok, outTok int
	if resp.LLMSynthesis != nil {
		// Inside this block on purpose: b.serverKey says whose key WOULD have
		// been used, this says an LLM actually used it. gaps and evidence never
		// synthesize, so they leave the field absent rather than naming a model
		// that never ran.
		resp.ServerKey = b.serverKey
		model = resp.LLMSynthesis.Model
		inTok = resp.LLMSynthesis.InputTokens
		outTok = resp.LLMSynthesis.OutputTokens
	}
	recordUsage(r, kind, model, inTok, outTok)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// recordApp is this app's name in the auth service's usage ledger.
const recordApp = "corpova"

// recordTimeout bounds one recording call. It is deliberately short: the answer
// is already computed and waiting behind it, so a sick auth service must cost
// the caller two seconds, not two minutes. A var rather than a const only so a
// test can shorten it; nothing in production writes it.
var recordTimeout = 2 * time.Second

// recordClient is the one client every recording call shares. A client per
// request would build — and then abandon — a connection pool per request, which
// under load is how a server runs out of sockets while talking to a single host.
var recordClient = &http.Client{Timeout: recordTimeout}

// recordUsage reports one successful analysis to the auth service's record
// endpoint (POST /auth/record?app=&kind=) as an internal caller: the
// X-Pubvera-User + X-Pubvera-Internal-Token pair rather than a bearer token.
// kind is "deep" when the LLM synthesis ran and "base" when it did not.
//
// It runs server-side because that is the only place it can be trusted: a
// browser-side call can simply be blocked by the user, and then paid queries
// cost nothing.
//
// Three states skip the call entirely, and none of them is an error:
//   - AUTH_RECORD_URL empty — recording is not configured;
//   - INTERNAL_RECORD_TOKEN empty — same;
//   - no X-Pubvera-User on the request, which is every request until Caddy's
//     forward_auth is switched on and starts copying the header upstream. There
//     is no id to attribute the usage to, and inventing one would be worse than
//     losing the entry.
//
// The first two are the current deployment state, so the untouched path through
// this function is the one that does nothing at all.
//
// A failed recording NEVER stops the response: the caller asked a question and
// the answer is ready; losing a bookkeeping entry is the lesser harm compared
// with withholding a result they are entitled to. Every path below returns
// normally and at most logs.
func recordUsage(r *http.Request, kind, model string, inputTokens, outputTokens int) {
	rawURL := strings.TrimSpace(os.Getenv("AUTH_RECORD_URL"))
	token := strings.TrimSpace(os.Getenv("INTERNAL_RECORD_TOKEN"))
	user := strings.TrimSpace(r.Header.Get("X-Pubvera-User"))
	if rawURL == "" || token == "" || user == "" {
		return
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		log.Printf("record: AUTH_RECORD_URL is not a URL: %v", err)
		return
	}
	// Set on the parsed query rather than by string concatenation, so a URL that
	// already carries parameters keeps them instead of growing a second "?".
	q := u.Query()
	q.Set("app", recordApp)
	q.Set("kind", kind)
	// Sent only as a complete set. Token counts without a model cannot be
	// priced, and the auth service would drop them anyway; leaving them off
	// keeps the request honest about what it knows.
	if model != "" {
		q.Set("model", model)
		q.Set("in_tok", strconv.Itoa(inputTokens))
		q.Set("out_tok", strconv.Itoa(outputTokens))
	}
	u.RawQuery = q.Encode()

	// context.Background(), deliberately NOT the request's: the request is about
	// to complete, and its context is cancelled the moment the handler returns —
	// cancelling the record with it would lose exactly the entries that matter.
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		log.Printf("record: build request: %v", err)
		return
	}
	// The internal-caller pair. The token is a shared secret and is never logged,
	// like every other secret this server handles.
	req.Header.Set("X-Pubvera-User", user)
	req.Header.Set("X-Pubvera-Internal-Token", token)

	resp, err := recordClient.Do(req)
	if err != nil {
		log.Printf("record: %s entry failed: %v", kind, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain (bounded) before closing so the connection goes back to the pool
	// instead of being torn down after every call.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("record: auth service returned HTTP %d for a %s entry", resp.StatusCode, kind)
	}
}

// runCLIRaw returns the CLI payload for one request — the cache's miss path,
// factored out of runCLIJSON so that a cache HIT skips spawning a child process
// entirely. On failure it writes the HTTP error itself and returns false, so the
// caller only has to return.
//
// It no longer necessarily RUNS the CLI. Concurrent callers of the same key
// share one run (singleflight.go): the first starts it, the rest wait for it and
// receive its bytes. Sharing is safe precisely because the child is keyless —
// the same argument that makes the payload cacheable across callers makes it
// shareable between simultaneous ones. Callers of different keys never wait on
// each other.
//
// Note what is deliberately absent: no Redis error can reach this function's
// HTTP response. cacheStore.Set is fire-and-forget inside the run, so the worst
// a broken cache can do is make every request take this route — precisely how
// the server behaved before the cache existed.
func runCLIRaw(ctx context.Context, w http.ResponseWriter, b byok, cacheKey string, args []string) ([]byte, bool) {
	raw, _, err := cliFlight.Do(ctx, cacheKey, func(runCtx context.Context) ([]byte, error) {
		// The slot is taken INSIDE the shared run, not around it. A caller that
		// joins an in-flight run spawns no process, so charging it a slot would
		// count children that do not exist and reject work that costs nothing.
		// Only genuinely new runs compete.
		if slotErr := cliSem.acquire(runCtx); slotErr != nil {
			return nil, slotErr
		}
		defer cliSem.release()

		out, runErr := runCLIOnce(runCtx, args)
		if runErr != nil {
			return nil, runErr
		}
		// One run, one write. A failed run is never cached.
		if cacheKey != "" {
			cacheStore.Set(cacheKey, out)
		}
		return out, nil
	})
	if err != nil {
		writeCLIError(w, b, err)
		return nil, false
	}
	return raw, true
}

// cliError is a CLI-leg failure with the HTTP status it should produce. It is
// shared verbatim with every caller waiting on the same run, which is safe
// because its message comes from a child that never received anyone's key: the
// text is caller-independent. Each caller still redacts its own key from it in
// writeCLIError, keeping the belt-and-braces guarantee per request.
type cliError struct {
	status int
	// retryAfter, when > 0, is sent as the Retry-After header in seconds. Only
	// the 503 from the concurrency semaphore sets it: a "not now" without a
	// "come back when" is not actionable, and a client that retries immediately
	// makes the overload worse.
	retryAfter int
	msg        string
}

func (e *cliError) Error() string { return e.msg }

// writeCLIError turns a run failure into this caller's HTTP response.
func writeCLIError(w http.ResponseWriter, b byok, err error) {
	var ce *cliError
	if errors.As(err, &ce) {
		// Headers must be set before the status line. writeError calls
		// WriteHeader, after which any Header().Set is silently discarded.
		if ce.retryAfter > 0 {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", ce.retryAfter))
		}
		writeError(w, ce.status, redact(ce.msg, b.key))
		return
	}
	// Not a CLI failure but this caller's own context ending: it timed out, or
	// the client went away. Either way this request is over; the run continues
	// for whoever else is waiting on it.
	writeError(w, http.StatusGatewayTimeout, redact(err.Error(), b.key))
}

// runCLIOnce runs the child CLI exactly once and returns its validated JSON.
// It is the only place a child process is spawned.
func runCLIOnce(ctx context.Context, args []string) ([]byte, error) {
	args = append(args, cliPacingArgs...)

	// #nosec G204 -- args are fixed subcommands/flags plus user text as discrete
	// argv elements (no shell); the child env carries no keys at all.
	cmd := exec.CommandContext(ctx, cliBinaryPath(), args...)
	cmd.Env = buildChildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Only args[0] (the fixed subcommand) may be logged: the remaining args
	// carry the user's claim text, which must never reach the server log.
	sub := "(none)"
	if len(args) > 0 {
		sub = args[0]
	}

	start := time.Now()
	err := cmd.Run()
	elapsedMS := time.Since(start).Milliseconds()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		log.Printf("cli: %s failed after %dms: %s", sub, elapsedMS, truncate(msg, 300))
		return nil, &cliError{status: http.StatusBadGateway, msg: "CLI failed: " + msg}
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	if !json.Valid(raw) {
		log.Printf("cli: %s returned non-JSON output after %dms (%d bytes)", sub, elapsedMS, len(raw))
		return nil, &cliError{status: http.StatusBadGateway, msg: "CLI returned non-JSON output"}
	}
	// A successful run can still have written to stderr, and those messages are
	// the ones worth seeing: the CLI warns there when the OpenAlex per-IP quota
	// is nearly spent, and prints its rate-limit and server-error retries the
	// same way. All of them happen while the command goes on to succeed, so
	// logging stderr only on failure discarded exactly the warnings that arrive
	// early enough to act on. Truncated like the failure path, and never sent to
	// the client: this is operator information, not an error.
	if w := strings.TrimSpace(stderr.String()); w != "" {
		log.Printf("cli: %s ok in %dms (%d bytes) — stderr: %s", sub, elapsedMS, len(raw), truncate(w, 300))
	} else {
		log.Printf("cli: %s ok in %dms (%d bytes)", sub, elapsedMS, len(raw))
	}
	return raw, nil
}

// decodePOST enforces POST + decodes a JSON body into dst. On any failure it
// writes the response and returns false.
func decodePOST(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// ---- single-claim endpoints (consensus, evidence, gaps, controversies) ------

// claimRequest is the shared body for the single-claim subcommands. Provider
// and Model select the in-process LLM synthesis; Model is an opaque token
// validated by validateModel and defaults to the provider's DefaultModel.
type claimRequest struct {
	Claim    string `json:"claim"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Limit    int    `json:"limit"`
}

// handleClaimCmd is the shared handler for every subcommand whose CLI shape is
// "<subcommand> <claim> --json --limit <n>". defLimit is that subcommand's CLI
// default, used when the caller omits or over-ranges limit.
func handleClaimCmd(w http.ResponseWriter, r *http.Request, subcommand string, defLimit int) {
	setCORS(w)
	if preflight(w, r) {
		return
	}
	var req claimRequest
	if !decodePOST(w, r, &req) {
		return
	}
	// normalizeClaim (whitespace-collapsing TrimSpace) is applied ONCE, here, so
	// the cache key and the argument handed to the CLI are derived from the same
	// string. The CLI echoes the claim back in its JSON, so keying on a
	// normalized claim while running the CLI on the raw one would let a hit
	// return a payload with different spacing than the miss it replaced.
	req.Claim = normalizeClaim(req.Claim)
	if req.Claim == "" {
		writeError(w, http.StatusBadRequest, "claim is required")
		return
	}
	b, ok := extractBYOK(w, r, req.Provider, req.Model)
	if !ok {
		return
	}
	limit := clampLimit(req.Limit, defLimit)
	claims := []string{req.Claim}
	args := []string{subcommand, req.Claim, "--json", "--limit", fmt.Sprintf("%d", limit)}
	runCLIJSON(w, r, b, subcommand, claims, args, cacheKey(subcommand, limit, claims))
}

func handleConsensus(w http.ResponseWriter, r *http.Request) {
	handleClaimCmd(w, r, "consensus", 40)
}

func handleEvidence(w http.ResponseWriter, r *http.Request) {
	handleClaimCmd(w, r, "evidence", 50)
}

func handleGaps(w http.ResponseWriter, r *http.Request) {
	handleClaimCmd(w, r, "gaps", 60)
}

func handleControversies(w http.ResponseWriter, r *http.Request) {
	handleClaimCmd(w, r, "controversies", 50)
}

// ---- two-claim endpoint (compare) -------------------------------------------

type compareRequest struct {
	Claim1   string `json:"claim1"`
	Claim2   string `json:"claim2"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Limit    int    `json:"limit"`
}

func handleCompare(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if preflight(w, r) {
		return
	}
	var req compareRequest
	if !decodePOST(w, r, &req) {
		return
	}
	// Same single normalization point as handleClaimCmd — see the comment there.
	req.Claim1 = normalizeClaim(req.Claim1)
	req.Claim2 = normalizeClaim(req.Claim2)
	if req.Claim1 == "" || req.Claim2 == "" {
		writeError(w, http.StatusBadRequest, "both claim1 and claim2 are required")
		return
	}
	b, ok := extractBYOK(w, r, req.Provider, req.Model)
	if !ok {
		return
	}
	limit := clampLimit(req.Limit, 40)
	claims := []string{req.Claim1, req.Claim2}
	args := []string{"compare", req.Claim1, req.Claim2, "--json", "--limit", fmt.Sprintf("%d", limit)}
	runCLIJSON(w, r, b, "compare", claims, args, cacheKey("compare", limit, claims))
}

// buildChildEnv returns the environment for the child CLI process: the
// server's own environment with EVERY provider key removed. The child is
// always keyless — keys are used only for the in-process LLM call and
// must never reach a subprocess.
func buildChildEnv() []string {
	strip := make(map[string]struct{}, len(allProviderEnvVars))
	for _, v := range allProviderEnvVars {
		strip[v] = struct{}{}
	}
	base := os.Environ()
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if _, drop := strip[name]; drop {
			continue // never inherit the server's own provider keys
		}
		out = append(out, kv)
	}
	return out
}

// redact removes the raw key substring from s so a key that appears in CLI
// stderr can never be returned to a client or written to a log. It is a
// belt-and-braces measure on top of the CLI's own credential redaction.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[REDACTED]")
}

// truncate shortens s to at most max runes, appending "..." when it actually
// cut something. Rune-based so it can never split a multi-byte character.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// quoteToken quotes a short untrusted token for safe inclusion in an error
// message, stripping control bytes so it can't echo terminal escapes.
func quoteToken(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return "\"" + strings.Map(func(r rune) rune {
		if r < 0x20 {
			return '?'
		}
		return r
	}, s) + "\""
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
