package handlers

import (
	"net/http"
	"os"
	"strings"
)

// GetSettings — GET /api/settings
// Returns system-level config flags. Read-only, sourced from env vars.
func GetSettings(w http.ResponseWriter, r *http.Request) {
	settings := map[string]any{
		"ai_enabled":     envBool("AI_ENABLED", true),
		"billing_enabled": envBool("BILLING_ENABLED", true),
		"spaces_enabled":  envBool("SPACES_ENABLED", true),
		"max_projects":    envInt("MAX_PROJECTS", 0), // 0 = unlimited
	}

	WriteJSON(w, 200, map[string]any{"data": settings})
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return strings.ToLower(v) == "true" || v == "1"
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
