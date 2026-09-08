package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// --- Permission checking ---

// isOwnerRole checks if a member's role_id points to the Owner role.
func isOwnerRole(member *models.OrgMember) bool {
	if member == nil || member.RoleID == nil || *member.RoleID == "" {
		return false
	}
	var role models.OrgRole
	if err := database.DB.Where("id = ?", *member.RoleID).First(&role).Error; err != nil {
		return false
	}
	return isOwnerRoleName(role.Name)
}

func isOwnerRoleName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Owner")
}

// roleName returns the display name for a member's role via role_id.
func roleName(member *models.OrgMember) string {
	if member == nil || member.RoleID == nil || *member.RoleID == "" {
		return ""
	}
	var role models.OrgRole
	if err := database.DB.Where("id = ?", *member.RoleID).First(&role).Error; err != nil {
		return ""
	}
	return role.Name
}

// HasPermission checks whether a member has a specific permission via their role_id.
func HasPermission(member *models.OrgMember, permission string) bool {
	if member == nil {
		log.Printf("[perm] HasPermission called with nil member for %q", permission)
		return false
	}

	if member.RoleID == nil || *member.RoleID == "" {
		log.Printf("[perm] DENIED %q for member %s — no role_id", permission, member.ID)
		return false
	}

	// Owner always has all permissions
	if isOwnerRole(member) {
		return true
	}

	var perms []models.OrgRolePermission
	database.DB.Where("role_id = ?", *member.RoleID).Find(&perms)

	for _, p := range perms {
		if models.MatchPermission(p.Permission, permission) {
			return true
		}
	}

	permStrs := make([]string, len(perms))
	for i, p := range perms {
		permStrs[i] = p.Permission
	}
	log.Printf("[perm] DENIED %q for member %s (role_id=%s, role_name=%s) — has %d perms: %v", permission, member.ID, *member.RoleID, roleName(member), len(perms), permStrs)

	return false
}

func canGrantPermissions(member *models.OrgMember, permissions []string) bool {
	if isOwnerRole(member) {
		return true
	}
	for _, permission := range permissions {
		if strings.TrimSpace(permission) == "" {
			continue
		}
		if !HasPermission(member, permission) {
			return false
		}
	}
	return true
}

func requireGrantablePermissions(w http.ResponseWriter, member *models.OrgMember, permissions []string) bool {
	if !canGrantPermissions(member, permissions) {
		WriteJSON(w, 403, map[string]string{"error": "cannot grant permissions you do not hold"})
		return false
	}
	return true
}

// rolePermissionStrings returns the permission strings a role grants. Used to
// enforce "can't grant permissions you don't hold" when assigning a role to a
// member (not just when creating/editing the role itself).
func rolePermissionStrings(roleID string) []string {
	var perms []models.OrgRolePermission
	database.DB.Where("role_id = ?", roleID).Find(&perms)
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.Permission
	}
	return out
}

// requirePermission is a helper for handlers — checks permission and writes 403 if denied.
func requirePermission(w http.ResponseWriter, member *models.OrgMember, permission string) bool {
	if !HasPermission(member, permission) {
		WriteJSON(w, 403, map[string]string{"error": "insufficient permissions"})
		return false
	}
	return true
}

// --- Seed ---

// SeedBuiltinRoles creates or updates the built-in roles for an organization.
// Creates missing roles and syncs permissions for existing builtin roles
// so that newly added permissions are picked up automatically.
func SeedBuiltinRoles(orgID string) {
	for _, def := range models.BuiltinRoles {
		var role models.OrgRole
		if err := database.DB.Where("org_id = ? AND name = ?", orgID, def.Name).First(&role).Error; err != nil {
			// Role doesn't exist — create it
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
				continue
			}
		}

		// Sync permissions: add any missing from the definition
		expanded := models.ExpandPermissions(def.Permissions)

		var existing []models.OrgRolePermission
		database.DB.Where("role_id = ?", role.ID).Find(&existing)
		have := map[string]bool{}
		for _, p := range existing {
			have[p.Permission] = true
		}

		added := 0
		for _, perm := range expanded {
			if !have[perm] {
				database.DB.Create(&models.OrgRolePermission{
					ID:         uuid(),
					RoleID:     role.ID,
					Permission: perm,
				})
				added++
			}
		}
		if added > 0 {
			log.Printf("[roles] org=%s role=%s: added %d missing permissions", orgID, def.Name, added)
		}
	}
}

// SyncAllBuiltinRoles syncs builtin role permissions for every existing org
// and backfills role_id for members that only have a role name string.
// Called on server startup.
func SyncAllBuiltinRoles() {
	var orgs []models.Organization
	database.DB.Find(&orgs)
	log.Printf("[roles] syncing builtin permissions for %d org(s)...", len(orgs))
	for _, org := range orgs {
		SeedBuiltinRoles(org.ID)
		backfillRoleIDs(org.ID)
	}
	log.Printf("[roles] sync complete")
}

// backfillRoleIDs ensures every member has a correct role_id.
// Resolves role_id from the role name string (the old source of truth)
// and fixes any mismatches where role_id points to the wrong role.
func backfillRoleIDs(orgID string) {
	var members []models.OrgMember
	database.DB.Where("org_id = ?", orgID).Find(&members)

	for _, m := range members {
		if m.Role == "" {
			continue
		}

		// Find the role that matches the member's role name string
		var correctRole models.OrgRole
		if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?)", orgID, m.Role).First(&correctRole).Error; err != nil {
			continue
		}

		// If role_id is missing or points to the wrong role, fix it
		if m.RoleID == nil || *m.RoleID != correctRole.ID {
			old := ""
			if m.RoleID != nil {
				old = *m.RoleID
			}
			log.Printf("[roles] fixing member %s: role=%q, old role_id=%s -> %s (%s)", m.ID, m.Role, old, correctRole.ID, correctRole.Name)
			database.DB.Model(&m).Update("role_id", correctRole.ID)
		}
	}
}

// --- Role CRUD ---

// ListRoles — GET /api/org/roles
func ListRoles(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var roles []models.OrgRole
	database.DB.Where("org_id = ?", caller.OrgID).Order("is_builtin DESC, name ASC").Find(&roles)

	// Attach permissions to each role
	result := make([]models.RoleWithPermissions, len(roles))
	for i, role := range roles {
		var perms []models.OrgRolePermission
		database.DB.Where("role_id = ?", role.ID).Find(&perms)

		permStrings := make([]string, len(perms))
		for j, p := range perms {
			permStrings[j] = p.Permission
		}

		result[i] = models.RoleWithPermissions{
			OrgRole:     role,
			Permissions: permStrings,
		}
	}

	WriteJSON(w, 200, result)
}

// GetRole — GET /api/org/roles/{id}
func GetRole(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var role models.OrgRole
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&role).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "role not found"})
		return
	}

	var perms []models.OrgRolePermission
	database.DB.Where("role_id = ?", role.ID).Find(&perms)

	permStrings := make([]string, len(perms))
	for i, p := range perms {
		permStrings[i] = p.Permission
	}

	WriteJSON(w, 200, models.RoleWithPermissions{
		OrgRole:     role,
		Permissions: permStrings,
	})
}

// CreateRole — POST /api/org/roles
func CreateRole(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "roles.create") {
		return
	}

	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	if !requireGrantablePermissions(w, caller, body.Permissions) {
		return
	}

	// Check name uniqueness
	var existing models.OrgRole
	if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?)", caller.OrgID, strings.TrimSpace(body.Name)).First(&existing).Error; err == nil {
		WriteJSON(w, 400, map[string]string{"error": "role name already exists"})
		return
	}

	role := models.OrgRole{
		ID:          uuid(),
		OrgID:       caller.OrgID,
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		IsBuiltin:   false,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := database.DB.Create(&role).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create role"})
		return
	}

	// Insert permissions
	for _, perm := range body.Permissions {
		database.DB.Create(&models.OrgRolePermission{
			ID:         uuid(),
			RoleID:     role.ID,
			Permission: perm,
		})
	}

	logOrgActivity(caller.OrgID, "created", "role", role.ID, role.Name, caller.ID)

	WriteJSON(w, 201, models.RoleWithPermissions{
		OrgRole:     role,
		Permissions: body.Permissions,
	})
}

// UpdateRole — PUT /api/org/roles/{id}
func UpdateRole(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "roles.edit") {
		return
	}

	id := r.PathValue("id")
	var role models.OrgRole
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&role).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "role not found"})
		return
	}

	var body struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		Permissions []string `json:"permissions"` // full replacement if provided
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	// Built-in roles: can edit permissions and description, but not rename
	if role.IsBuiltin && body.Name != nil && !strings.EqualFold(*body.Name, role.Name) {
		WriteJSON(w, 400, map[string]string{"error": "cannot rename a built-in role"})
		return
	}

	// Owner role permissions are immutable
	if role.IsBuiltin && isOwnerRoleName(role.Name) && body.Permissions != nil {
		WriteJSON(w, 400, map[string]string{"error": "cannot modify Owner role permissions"})
		return
	}
	if body.Permissions != nil && !requireGrantablePermissions(w, caller, body.Permissions) {
		return
	}

	if body.Name != nil {
		// Check uniqueness for name change
		var dup models.OrgRole
		if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?) AND id != ?", caller.OrgID, strings.TrimSpace(*body.Name), role.ID).First(&dup).Error; err == nil {
			WriteJSON(w, 400, map[string]string{"error": "role name already exists"})
			return
		}
		role.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		role.Description = strings.TrimSpace(*body.Description)
	}

	role.UpdatedAt = time.Now()
	database.DB.Save(&role)

	// Replace permissions if provided
	if body.Permissions != nil {
		database.DB.Where("role_id = ?", role.ID).Delete(&models.OrgRolePermission{})
		for _, perm := range body.Permissions {
			database.DB.Create(&models.OrgRolePermission{
				ID:         uuid(),
				RoleID:     role.ID,
				Permission: perm,
			})
		}

		// Sync the denormalized role name on members
		database.DB.Model(&models.OrgMember{}).Where("role_id = ? AND org_id = ?", role.ID, caller.OrgID).Update("role", strings.ToLower(role.Name))
	}

	logOrgActivity(caller.OrgID, "updated", "role", role.ID, role.Name, caller.ID)

	// Return with permissions
	var perms []models.OrgRolePermission
	database.DB.Where("role_id = ?", role.ID).Find(&perms)
	permStrings := make([]string, len(perms))
	for i, p := range perms {
		permStrings[i] = p.Permission
	}

	WriteJSON(w, 200, models.RoleWithPermissions{
		OrgRole:     role,
		Permissions: permStrings,
	})
}

// DeleteRole — DELETE /api/org/roles/{id}
func DeleteRole(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "roles.delete") {
		return
	}

	id := r.PathValue("id")
	var role models.OrgRole
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&role).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "role not found"})
		return
	}

	if role.IsBuiltin {
		WriteJSON(w, 400, map[string]string{"error": "cannot delete a built-in role"})
		return
	}

	// Find the "Member" built-in role to reassign
	var memberRole models.OrgRole
	if err := database.DB.Where("org_id = ? AND name = ? AND is_builtin = ?", caller.OrgID, "Member", true).First(&memberRole).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "cannot find fallback Member role"})
		return
	}

	// Reassign members from deleted role to Member
	database.DB.Model(&models.OrgMember{}).
		Where("role_id = ? AND org_id = ?", role.ID, caller.OrgID).
		Updates(map[string]string{"role_id": memberRole.ID, "role": "member"})

	// Delete permissions and role
	database.DB.Where("role_id = ?", role.ID).Delete(&models.OrgRolePermission{})
	database.DB.Delete(&role)

	logOrgActivity(caller.OrgID, "deleted", "role", role.ID, role.Name, caller.ID)

	WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

// ListPermissions — GET /api/org/permissions
func ListPermissions(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	WriteJSON(w, 200, models.AllPermissions)
}

// AssignMemberRole — PUT /api/org/members/{id}/role
func AssignMemberRole(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.edit") {
		return
	}

	id := r.PathValue("id")
	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	// Cannot change the owner's role
	if isOwnerRole(&member) {
		WriteJSON(w, 400, map[string]string{"error": "cannot change the owner's role"})
		return
	}

	var body struct {
		RoleID string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RoleID == "" {
		WriteJSON(w, 400, map[string]string{"error": "role_id is required"})
		return
	}

	// Verify role exists in this org
	var role models.OrgRole
	if err := database.DB.Where("id = ? AND org_id = ?", body.RoleID, caller.OrgID).First(&role).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "role not found"})
		return
	}

	// Cannot assign Owner role
	if isOwnerRoleName(role.Name) {
		WriteJSON(w, 400, map[string]string{"error": "cannot assign Owner role"})
		return
	}

	// SECURITY: can't grant a role that confers permissions the caller doesn't
	// hold — otherwise a member with only members.edit could promote anyone
	// (incl. themselves) to Admin. Mirrors the check on role create/update.
	if !requireGrantablePermissions(w, caller, rolePermissionStrings(role.ID)) {
		return
	}

	member.RoleID = &role.ID
	member.Role = strings.ToLower(role.Name)
	database.DB.Save(&member)

	logOrgActivity(caller.OrgID, "role_changed", "member", member.ID, member.Name+" -> "+role.Name, caller.ID)

	WriteJSON(w, 200, member)
}
