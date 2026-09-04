package main

import (
	"encoding/json"
	"net/http"
)

// Tiers that may run on the server's own provider key. A caller on one of
// these tiers who supplies no key of their own falls back to it (main.go:
// extractBYOK), and the spend lands on the operator's account rather than the
// caller's — which is why the model is pinned below.
const (
	freeTier  = "free"
	trialTier = "trial"
)

// The one provider/model pair the server's key is allowed to run. The pair is
// hardcoded here on purpose: model_pricing.min_tier exists in the database but
// is not consulted by this service, so the gate must not depend on a database
// round trip in the request path.
const (
	freeTierProvider = "deepseek"
	freeTierModel    = "deepseek-chat"
)

// llmWillRun reports whether this request will actually reach the provider.
//
// It is the single definition of that condition: runCLIJSON calls it to decide
// whether to synthesize, and the tier gate calls it to decide whether there is
// anything worth refusing. Keeping one copy matters — an earlier version of the
// gate refused on provider alone, which locked free callers out of gaps and
// evidence even though those endpoints never spend a cent.
//
// gaps and evidence emit summary statistics only (no study list, no abstracts),
// so an LLM synthesis over them has nothing to judge — it yielded
// 0.00-confidence or unparseable verdicts. An empty key means the caller never
// supplied credentials, so there is nothing to call the provider with.
func llmWillRun(b byok, endpoint string) bool {
	return b.key != "" && endpoint != "gaps" && endpoint != "evidence"
}

// resolveModel returns the model that would actually be sent to the provider.
// It repeats the fallback llmSynthesize applies (providers.go: an empty model
// override becomes spec.DefaultModel), because the gate has to compare the
// effective model, not the override. A caller who left the field empty on a
// non-deepseek provider is still choosing that provider's default model.
//
// An unknown or empty provider yields "": unknown providers are already
// rejected by extractBYOK.
func resolveModel(provider, model string) string {
	if model != "" {
		return model
	}
	if spec, ok := providers[provider]; ok {
		return spec.DefaultModel
	}
	return ""
}

// runsCheapModel reports whether the resolved request is the one pinned pair.
func runsCheapModel(b byok) bool {
	return b.provider == freeTierProvider && resolveModel(b.provider, b.model) == freeTierModel
}

// tierAllowsModel reports whether a caller may run the given request.
//
// There is exactly ONE reason to refuse: the request would spend the
// operator's money. The server's own key runs one pinned model and nothing
// else. A caller who supplies their OWN key is spending their own money and is
// never restricted, whatever their tier — a free caller with a personal key has
// the same model freedom as pro and max.
//
// That is a deliberate narrowing, decided 2026-09-04 after a free-tier tester
// pasted his own Gemini key and was refused. The gate exists to protect the
// operator's spend; with a caller's own key there is no spend to protect. Pro
// is sold on what genuinely costs money — the included key's daily budget, the
// number of apps, the request limits — not on blocking a model the caller is
// paying for themselves.
//
// The tier argument is no longer consulted. It is kept in the signature so the
// Caddy header stays plumbed through to one obvious place, which is where a
// future tier-dependent rule would go.
//
// Note this gate covers model choice only. Whether a caller's plan includes
// this app at all is decided upstream by Caddy's forward_auth, and a personal
// API key does not and must not unlock that.
func tierAllowsModel(tier string, b byok, endpoint string) bool {
	// No provider call means no cost and no model to refuse. Refusing here
	// would lock callers out of endpoints that never spend anything.
	if !llmWillRun(b, endpoint) {
		return true
	}
	if b.serverKey {
		return runsCheapModel(b)
	}
	return true
}

// tierRefusalMessage explains the refusal in the terms that apply to this
// caller. The two cases are not interchangeable: telling a trial user to
// "upgrade to Pro" would be wrong when all they need to do is paste their own
// key, and telling a free user to paste a key would hide the plan limit.
func tierRefusalMessage(b byok) string {
	if b.serverKey {
		return "The included trial key only runs " + freeTierModel +
			". Enter your own API key to use other models."
	}
	return "The free plan can only use " + freeTierModel +
		". Upgrade to Pro to use other models."
}

// enforceTierGate applies tierAllowsModel to an incoming request. It returns
// true when the request may proceed; on refusal it writes the 403 response and
// returns false so the caller stops.
//
// The refusal is explicit rather than a silent downgrade to deepseek-chat: the
// caller picked a model, and quietly answering with a different one would hide
// both the plan limit and the fact that the answer came from elsewhere.
func enforceTierGate(w http.ResponseWriter, r *http.Request, b byok, endpoint string) bool {
	if tierAllowsModel(r.Header.Get("X-Pubvera-Tier"), b, endpoint) {
		return true
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "model_locked",
		"message": tierRefusalMessage(b),
	})
	return false
}
