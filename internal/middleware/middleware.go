package middleware

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"construct/source/internal/config"

	goauth "github.com/construct-space/go-auth"
)

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func CORS(cfg *config.Config) func(http.Handler) http.Handler {
	allowed := make(map[string]bool)
	for _, o := range cfg.AllowedOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// AccountsUser is the profile returned by accounts.lisaos.dev/api/me.
type AccountsUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Auth validates a Bearer token against accounts.lisaos.dev.
// On success, sets X-User-ID header and passes through.
func Auth(cfg *config.Config) func(http.Handler) http.Handler {
	// Simple in-memory cache: token → (user, expiry)
	type cached struct {
		user    AccountsUser
		expires time.Time
	}
	cache := make(map[string]cached)
	var cacheMu sync.Mutex
	const maxCacheEntries = 4096

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Gateway-trusted path: my.lisaos.dev already validated the
			// caller via accounts and forwards X-Auth-* headers alongside
			// a matching X-Internal-Secret. Trust those and skip the Bearer
			// check entirely — browsers don't send a Bearer token.
			if id := gatewayIdentity(r, cfg); id.Authenticated() {
				r.Header.Set("X-User-ID", id.UserID)
				if id.Email != "" {
					r.Header.Set("X-User-Email", id.Email)
				}
				next.ServeHTTP(w, r)
				return
			}

			token := bearerToken(r)
			if token == "" {
				writeJSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}

			// Check cache
			cacheMu.Lock()
			if c, ok := cache[token]; ok && time.Now().Before(c.expires) {
				cacheMu.Unlock()
				r.Header.Set("X-User-ID", c.user.ID)
				r.Header.Set("X-User-Email", c.user.Email)
				next.ServeHTTP(w, r)
				return
			}
			cacheMu.Unlock()

			// Validate against accounts service
			user, err := validateToken(cfg.AccountsURL, token)
			if err != nil {
				writeJSON(w, 401, map[string]string{"error": "invalid token"})
				return
			}

			// Cache for 5 minutes
			cacheMu.Lock()
			if len(cache) >= maxCacheEntries {
				now := time.Now()
				for key, value := range cache {
					if now.After(value.expires) || len(cache) >= maxCacheEntries {
						delete(cache, key)
					}
				}
			}
			cache[token] = cached{user: *user, expires: time.Now().Add(5 * time.Minute)}
			cacheMu.Unlock()

			r.Header.Set("X-User-ID", user.ID)
			r.Header.Set("X-User-Email", user.Email)
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the auth token from either the Authorization
// header or, when permitWS is implied by the caller, the ?token=
// query parameter. Browser WebSocket constructors can't set custom
// headers, so the device-bus WS endpoint accepts the query form
// as the only practical way to authenticate the upgrade request.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		t := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if t != "" {
			return t
		}
	}
	if r.Method == "GET" {
		if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
			return t
		}
	}
	return ""
}

// gatewayTrusted reports whether the request was signed by the gateway
// via X-Internal-Secret. Accepts either INTERNAL_SHARED_SECRET (unified
// name) or Cfg.ServiceAPIKey (legacy). Mirror of the same-named helper
// in the handlers package — kept here to avoid an import cycle.
//
//nolint:unused // kept in parity with handlers.gatewayTrusted; to be consolidated
func gatewayTrusted(r *http.Request, cfg *config.Config) bool {
	return gatewayIdentity(r, cfg).Authenticated()
}

func gatewayIdentity(r *http.Request, cfg *config.Config) goauth.Identity {
	if id := goauth.Gateway(r, os.Getenv("INTERNAL_SHARED_SECRET")); id.Authenticated() {
		return id
	}
	if cfg != nil && cfg.ServiceAPIKey != "" {
		return goauth.Gateway(r, cfg.ServiceAPIKey)
	}
	return goauth.Identity{}
}

// ServiceAuth validates inter-service API key.
func ServiceAuth(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Authorization")
			if key == "" || key != "Bearer "+cfg.ServiceAPIKey {
				writeJSON(w, 401, map[string]string{"error": "invalid service key"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validateToken(accountsURL, token string) (*AccountsUser, error) {
	userInfoURL, err := accountsUserInfoURL(accountsURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, http.ErrAbortHandler
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var user AccountsUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func accountsUserInfoURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "https://my.lisaos.dev"
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Host == "accounts.lisaos.dev" {
		u.Scheme = "https"
		u.Host = "my.lisaos.dev"
		u.Path = ""
	}

	path := strings.TrimRight(u.Path, "/")
	if u.Host == "my.lisaos.dev" {
		switch path {
		case "", "/api", "/api/accounts":
			u.Path = "/api/accounts/me"
		default:
			u.Path = path + "/me"
		}
		return u.String(), nil
	}

	switch path {
	case "":
		u.Path = "/api/me"
	case "/api":
		u.Path = "/api/me"
	default:
		u.Path = path + "/api/me"
	}
	return u.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
