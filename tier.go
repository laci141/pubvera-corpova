package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The free plan is allowed exactly one model, on exactly one provider. The pair
// is hardcoded here on purpose: model_pricing.min_tier exists in the database
// but is not consulted by this service, so the gate must not depend on a
// database round trip in the request path.
const (
	freeTier         = "free"
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

// tierAllowsModel reports whether a caller on the given tier may run the given
// request.
//
// An absent or empty tier ALLOWS. That is the local development case: `go run .`
// has no Caddy in front of it, so nothing sets X-Pubvera-Tier and every request
// would otherwise be treated as free. The consequence is that this gate relies
// entirely on Caddy setting the header on /api/* (forward_auth +
// `copy_headers X-Pubvera-User X-Pubvera-Tier`); if that copy is ever dropped,
// the gate silently stops gating.
//
// Any tier other than "free" — pro, max, owner — is unaffected.
func tierAllowsModel(tier string, b byok, endpoint string) bool {
	if strings.TrimSpace(tier) != freeTier {
		return true
	}
	// No provider call means no cost and no model to refuse. Refusing here
	// would lock free callers out of endpoints that never spend anything.
	if !llmWillRun(b, endpoint) {
		return true
	}
	return b.provider == freeTierProvider && resolveModel(b.provider, b.model) == freeTierModel
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
		"message": "The free plan can only use deepseek-chat. Upgrade to Pro to use other models.",
	})
	return false
}
