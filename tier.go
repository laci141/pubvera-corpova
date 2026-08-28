package main

import (
	"encoding/json"
	"net/http"
	"strings"
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

// tierAllowsModel reports whether a caller on the given tier may run the given
// request.
//
// Two distinct reasons to refuse, deliberately kept separate:
//
//   - free: pinned to one model whoever pays. Free callers are always BYOK
//     (extractBYOK never hands them the server key), so this is a plan limit,
//     not a cost control.
//   - any tier on the server's key: pinned because the operator pays. A trial
//     caller who supplies their OWN key is spending their own money and is
//     therefore NOT restricted — same freedom pro and max have.
//
// An absent or empty tier ALLOWS. That is the local development case: `go run .`
// has no Caddy in front of it, so nothing sets X-Pubvera-Tier and every request
// would otherwise be treated as free. The consequence is that this gate relies
// entirely on Caddy setting the header on /api/* (forward_auth +
// `copy_headers X-Pubvera-User X-Pubvera-Tier`); if that copy is ever dropped,
// the gate silently stops gating.
func tierAllowsModel(tier string, b byok, endpoint string) bool {
	// No provider call means no cost and no model to refuse. Refusing here
	// would lock callers out of endpoints that never spend anything.
	if !llmWillRun(b, endpoint) {
		return true
	}
	if b.serverKey {
		return runsCheapModel(b)
	}
	if strings.TrimSpace(tier) != freeTier {
		return true
	}
	return runsCheapModel(b)
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
