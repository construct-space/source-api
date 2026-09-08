package handlers

import (
	"encoding/json"
	"net/http"

	"construct/source/internal/database"
	"construct/source/internal/models"

	"gorm.io/gorm"
)

// ─── Org preferences ─────────────────────────────────────────────────────────
// Stored per-org with uniqueness on (org_id, key). v1 schema:
//   members.invite_role_default   string  default "member"
//   members.require_2fa           bool    default false
//   projects.default_visibility   enum("private","internal","public") default "private"
// New keys can be added freely — values are free-form JSON.

// defaultOrgPrefs is the baseline every GET falls back to; it's the source
// of truth for "what keys are recognised" today. Keeping it in one place
// prevents the UI and backend from drifting.
var defaultOrgPrefs = map[string]any{
	"members.invite_role_default":  "member",
	"members.require_2fa":          false,
	"projects.default_visibility":  "private",
	// When true, org members may connect their own OAuth-based LLM
	// provider accounts (Claude Pro / ChatGPT Plus). When false, members
	// must use the org-level provider keys only. App-side enforced —
	// source just owns the policy bit.
	"providers.allow_member_oauth": true,
}

// OrgPrefString fetches a string-valued org pref, falling back to the
// default map if the row is absent or the value can't be coerced. Used
// by other handlers when they want to apply a policy default.
func OrgPrefString(orgID, key string) string {
	var p models.OrgPreference
	if err := database.DB.Where("org_id = ? AND key = ?", orgID, key).First(&p).Error; err == nil {
		// Stored strings come through verbatim; JSON-encoded strings get
		// unwrapped. Non-strings fall through to the default.
		if len(p.Value) > 0 && p.Value[0] == '"' {
			var s string
			if err := json.Unmarshal([]byte(p.Value), &s); err == nil {
				return s
			}
		} else {
			return p.Value
		}
	}
	if v, ok := defaultOrgPrefs[key].(string); ok {
		return v
	}
	return ""
}

// GetOrgPreferences — GET /api/org/preferences
// Returns every preference for the caller's org, backfilled with defaults
// for keys that have never been set. Any authenticated member can read.
func GetOrgPreferences(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var prefs []models.OrgPreference
	database.DB.Where("org_id = ?", caller.OrgID).Find(&prefs)

	data := make(map[string]any, len(defaultOrgPrefs))
	for k, v := range defaultOrgPrefs {
		data[k] = v
	}
	for _, p := range prefs {
		var parsed any
		if err := json.Unmarshal([]byte(p.Value), &parsed); err == nil {
			data[p.Key] = parsed
		} else {
			data[p.Key] = p.Value
		}
	}

	WriteJSON(w, 200, map[string]any{"data": data})
}

// SetOrgPreference — PUT /api/org/preferences/{key}
// Requires the `org.settings.edit` permission — gated so only admins/
// owners can change team-wide policies.
func SetOrgPreference(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "org.settings.edit") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		WriteJSON(w, 400, map[string]string{"error": "key is required"})
		return
	}

	var body struct {
		Value any `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	// Serialize to JSON so booleans and numbers round-trip cleanly; plain
	// strings stay readable in DB dumps by skipping the quote wrap.
	var valueStr string
	switch v := body.Value.(type) {
	case string:
		valueStr = v
	default:
		b, _ := json.Marshal(v)
		valueStr = string(b)
	}

	var existing models.OrgPreference
	err := database.DB.Where("org_id = ? AND key = ?", caller.OrgID, key).First(&existing).Error
	switch err {
	case gorm.ErrRecordNotFound:
		database.DB.Create(&models.OrgPreference{OrgID: caller.OrgID, Key: key, Value: valueStr})
	case nil:
		existing.Value = valueStr
		database.DB.Save(&existing)
	}

	WriteJSON(w, 200, map[string]any{"success": true})
}
