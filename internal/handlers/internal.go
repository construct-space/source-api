package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// requireInternalSecret gates /internal/* endpoints with a shared secret.
// Accepts either INTERNAL_SHARED_SECRET (the unified name) or
// Cfg.ServiceAPIKey (legacy name, still honored so deploy doesn't have to
// flip in lockstep). Returns 503 if neither is configured.
func requireInternalSecret(w http.ResponseWriter, r *http.Request) bool {
	if !gatewayTrusted(r) {
		// Distinguish "no secret configured" from "wrong secret" so a
		// misconfigured deploy is immediately visible.
		if Cfg.ServiceAPIKey == "" && os.Getenv("INTERNAL_SHARED_SECRET") == "" {
			WriteJSON(w, 503, map[string]any{"error": "internal endpoints disabled"})
			return false
		}
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return false
	}
	return true
}

// GET /internal/membership?user_id=<uuid>
// Service-to-service membership lookup. Called by accounts /me enrichment
// and developer's enrollment-eligibility check.
// Returns the user's OrgMember + Organization summary, or 404. `is_owner`
// reflects Organization.OwnerID equality so callers can authorize on
// owner ∪ role without a second round-trip.
func InternalGetMembership(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		WriteJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}

	var member models.OrgMember
	if err := database.DB.Where("user_id = ? AND status = ?", userID, "active").First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not in any organization"})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", member.OrgID).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "organization not found"})
		return
	}

	roles := memberRoleKeys(&member)

	WriteJSON(w, 200, map[string]any{
		"org_id":    org.ID,
		"org_name":  org.Name,
		"org_slug":  org.Slug,
		"org_icon":  org.Icon,
		"member_id": member.ID,
		"roles":     roles,
		"is_owner":  org.OwnerID == userID,
	})
}

// memberRoleKeys returns lowercase role name(s) held by a member. Single-role
// model today; returns a slice to match the /me JWT-like shape and leave room
// for multi-role assignment later.
func memberRoleKeys(member *models.OrgMember) []string {
	if member == nil {
		return []string{}
	}
	name := roleName(member)
	if name == "" {
		return []string{}
	}
	// Normalize to the lowercase key form used in JWT claims / permission checks.
	return []string{strings.ToLower(name)}
}

// POST /internal/developer-role/seed
// Body: { "org_id": "..." }
// Called by infra/developer when an org owner enrolls the org as a publisher.
// Creates the Developer role with default permissions if not already present.
func InternalSeedDeveloperRole(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "invalid body"})
		return
	}
	orgID, _ := body["org_id"].(string)
	if orgID == "" {
		WriteJSON(w, 400, map[string]any{"error": "org_id required"})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", orgID).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "organization not found"})
		return
	}

	seedSingleBuiltinRole(orgID, models.DeveloperRoleDef)

	WriteJSON(w, 200, map[string]any{"ok": true, "org_id": orgID, "role": "Developer"})
}

// POST /internal/developer-role/unseed
// Body: { "org_id": "..." }
// Called when an org un-enrolls as publisher. Deletes the Developer role and
// unassigns it from members (they fall back to their previous role or Member).
func InternalUnseedDeveloperRole(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "invalid body"})
		return
	}
	orgID, _ := body["org_id"].(string)
	if orgID == "" {
		WriteJSON(w, 400, map[string]any{"error": "org_id required"})
		return
	}

	var role models.OrgRole
	if err := database.DB.Where("org_id = ? AND name = ?", orgID, "Developer").First(&role).Error; err != nil {
		// Already absent — idempotent success.
		WriteJSON(w, 200, map[string]any{"ok": true, "already_absent": true})
		return
	}

	// Reassign members holding this role back to Member.
	var memberRole models.OrgRole
	if err := database.DB.Where("org_id = ? AND name = ?", orgID, "Member").First(&memberRole).Error; err == nil {
		database.DB.Model(&models.OrgMember{}).
			Where("org_id = ? AND role_id = ?", orgID, role.ID).
			Updates(map[string]any{"role_id": memberRole.ID, "role": "member"})
	}

	database.DB.Where("role_id = ?", role.ID).Delete(&models.OrgRolePermission{})
	database.DB.Delete(&role)

	log.Printf("[developer-role] unseeded for org=%s", orgID)
	WriteJSON(w, 200, map[string]any{"ok": true, "org_id": orgID})
}

// seedSingleBuiltinRole is a one-role variant of SeedBuiltinRoles used by
// the conditional Developer seed path. Idempotent.
func seedSingleBuiltinRole(orgID string, def models.BuiltinRoleDef) {
	var role models.OrgRole
	if err := database.DB.Where("org_id = ? AND name = ?", orgID, def.Name).First(&role).Error; err != nil {
		role = models.OrgRole{
			ID:          uuid(),
			OrgID:       orgID,
			Name:        def.Name,
			Description: def.Description,
			IsBuiltin:   true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := database.DB.Create(&role).Error; err != nil {
			log.Printf("[developer-role] create failed: %v", err)
			return
		}
	}

	expanded := models.ExpandPermissions(def.Permissions)
	var existing []models.OrgRolePermission
	database.DB.Where("role_id = ?", role.ID).Find(&existing)
	have := map[string]bool{}
	for _, p := range existing {
		have[p.Permission] = true
	}
	for _, perm := range expanded {
		if !have[perm] {
			database.DB.Create(&models.OrgRolePermission{
				ID:         uuid(),
				RoleID:     role.ID,
				Permission: perm,
			})
		}
	}
}
