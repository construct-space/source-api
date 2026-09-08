package handlers

import (
	"encoding/json"
	"net/http"
	"os"

	"construct/source/internal/config"

	goauth "github.com/construct-space/go-auth"
)

var Cfg *config.Config

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// gatewayTrusted reports whether the request was signed by the gateway,
// i.e. carries a matching X-Internal-Secret. Only the gateway (or a trusted
// peer service) holds that secret, so headers on the request can be trusted
// as coming from a validated caller.
//
// Prefers INTERNAL_SHARED_SECRET (the unified name across services) and
// falls back to Cfg.ServiceAPIKey for the existing /internal/* endpoints
// that were already set up with SERVICE_API_KEY.
func gatewayTrusted(r *http.Request) bool {
	if goauth.Trusted(r, os.Getenv("INTERNAL_SHARED_SECRET")) {
		return true
	}
	return Cfg != nil && Cfg.ServiceAPIKey != "" && goauth.Trusted(r, Cfg.ServiceAPIKey)
}

// gatewayIdentity decodes attested X-Auth-* headers when either secret matches.
// Preserves the SERVICE_API_KEY fallback for legacy deployments.
func gatewayIdentity(r *http.Request) goauth.Identity {
	if id := goauth.Gateway(r, os.Getenv("INTERNAL_SHARED_SECRET")); id.Authenticated() {
		return id
	}
	if Cfg != nil && Cfg.ServiceAPIKey != "" {
		return goauth.Gateway(r, Cfg.ServiceAPIKey)
	}
	return goauth.Identity{}
}

// UserID returns the authenticated user's UUID. Prefers gateway-trusted
// X-Auth-User-ID (set by my.lisaos.dev after validating the token),
// then falls back to the legacy client-set X-User-ID used by construct-app
// and CLI direct calls.
func UserID(r *http.Request) string {
	if id := gatewayIdentity(r); id.Authenticated() {
		return id.UserID
	}
	return r.Header.Get("X-User-ID")
}

// derefBool returns *p if p is non-nil, otherwise def. Used by handlers
// that take optional bool fields in JSON payloads (nil = "not provided",
// so fall back to the model's existing value / default).
func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func UserEmail(r *http.Request) string {
	if id := gatewayIdentity(r); id.Authenticated() {
		return id.Email
	}
	return r.Header.Get("X-User-Email")
}
