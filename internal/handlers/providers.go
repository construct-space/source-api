package handlers

// User BYOK provider key management.
//
// The provider catalog itself (read + admin) moved to api/provider on
// 2026-05-19. Source kept the per-user key storage (ProviderKey table) and
// the org-managed key storage (OrgProviderKey) because they're identity-
// scoped data, not catalog data.
//
// Clients fetch the catalog from provider-api (`/api/providers`) and the
// user's saved keys here (`/api/providers/keys`), then merge in the UI.

import (
	"encoding/json"
	"net/http"
	"strings"

	"construct/source/internal/database"
	"construct/source/internal/models"

	"gorm.io/gorm"
)

type keyStatus struct {
	Provider   string `json:"provider"`
	HasUserKey bool   `json:"has_user_key"`
	MaskedKey  string `json:"masked_key,omitempty"`
}

// ListKeys — GET /api/providers/keys
//
// Returns only the user's saved keys, masked. The client merges this list
// with the catalog from provider-api to render the picker (which providers
// have a user key, which have a shared/org key, which are unconfigured).
func ListKeys(w http.ResponseWriter, r *http.Request) {
	uid := UserID(r)

	var keys []models.ProviderKey
	database.DB.Where("user_id = ?", uid).Find(&keys)

	statuses := make([]keyStatus, 0, len(keys))
	for _, k := range keys {
		if k.APIKey == "" {
			continue
		}
		statuses = append(statuses, keyStatus{
			Provider:   k.Provider,
			HasUserKey: true,
			MaskedKey:  maskKey(k.APIKey),
		})
	}

	WriteJSON(w, 200, map[string]any{"data": statuses})
}

// SetKey — PUT /api/providers/keys/{provider}
//
// Validation was previously done against source's local catalog; that
// catalog moved to api/provider. Source now stores whatever slug the
// caller sends — provider-api is the truth for "is this a real provider."
// A bogus slug just sits unused; chat attempts via that slug fail at
// provider-api routing time.
func SetKey(w http.ResponseWriter, r *http.Request) {
	uid := UserID(r)
	providerID := strings.TrimSpace(r.PathValue("provider"))
	if providerID == "" {
		WriteJSON(w, 400, map[string]string{"error": "provider is required"})
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Key) == "" {
		WriteJSON(w, 400, map[string]string{"error": "key is required"})
		return
	}

	var existing models.ProviderKey
	err := database.DB.Where("user_id = ? AND provider = ?", uid, providerID).First(&existing).Error
	switch err {
	case gorm.ErrRecordNotFound:
		database.DB.Create(&models.ProviderKey{UserID: uid, Provider: providerID, APIKey: body.Key})
	case nil:
		existing.APIKey = body.Key
		database.DB.Save(&existing)
	}

	WriteJSON(w, 200, map[string]any{"success": true})
}

// DeleteKey — DELETE /api/providers/keys/{provider}
func DeleteKey(w http.ResponseWriter, r *http.Request) {
	uid := UserID(r)
	providerID := strings.TrimSpace(r.PathValue("provider"))
	if providerID == "" {
		WriteJSON(w, 400, map[string]string{"error": "provider is required"})
		return
	}

	database.DB.Where("user_id = ? AND provider = ?", uid, providerID).Delete(&models.ProviderKey{})
	WriteJSON(w, 200, map[string]any{"success": true})
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "..." + key[len(key)-4:]
}
