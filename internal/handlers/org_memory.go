package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// GetOrgMemory — GET /api/org/memory
// Returns the org's shared agent memory. Any member may read it.
func GetOrgMemory(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	var m models.OrgMemory
	// First with no error check — an empty row (no memory yet) is valid.
	database.DB.Where("org_id = ?", orgID).First(&m)
	WriteJSON(w, 200, map[string]any{
		"content":    m.Content,
		"updated_by": m.UpdatedBy,
		"updated_at": m.UpdatedAt,
	})
}

// PutOrgMemory — PUT /api/org/memory
// Overwrites the org's shared memory. Any member may write it (shared,
// collaborative — like a team notepad the agent also edits).
func PutOrgMemory(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	m := models.OrgMemory{
		OrgID:     caller.OrgID,
		Content:   body.Content,
		UpdatedBy: caller.UserID,
		UpdatedAt: time.Now(),
	}
	// Upsert — OrgID is the primary key, so Save inserts or updates.
	if err := database.DB.Save(&m).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to save memory"})
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true, "updated_at": m.UpdatedAt})
}
