package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/mailer"
	"construct/source/internal/models"
	"construct/source/internal/services"
)

// fetchUserAvatars calls accounts /internal/users/batch to resolve
// avatar URLs for a set of user IDs. Returns a map of user_id → avatar_url.
// Best-effort: returns an empty map on any error so callers fall back to
// the local org_members.avatar value.
func fetchUserAvatars(userIDs []string) map[string]string {
	out := map[string]string{}
	if len(userIDs) == 0 || Cfg == nil || Cfg.AccountsURL == "" || Cfg.ServiceAPIKey == "" {
		return out
	}
	body, err := json.Marshal(map[string]any{"ids": userIDs})
	if err != nil {
		return out
	}
	req, err := http.NewRequest("POST", Cfg.AccountsURL+"/internal/users/batch", bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", Cfg.ServiceAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}
	var parsed struct {
		Users []struct {
			ID        string `json:"id"`
			AvatarURL string `json:"avatar_url"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return out
	}
	for _, u := range parsed.Users {
		if u.AvatarURL != "" {
			out[u.ID] = u.AvatarURL
		}
	}
	return out
}

// enrichMembersWithAvatars fills in member.Avatar from the accounts
// service when the org_members row has no avatar set. Mutates in place.
func enrichMembersWithAvatars(members []models.OrgMember) {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		if m.Avatar == "" && m.UserID != "" {
			ids = append(ids, m.UserID)
		}
	}
	if len(ids) == 0 {
		return
	}
	avatars := fetchUserAvatars(ids)
	for i := range members {
		if members[i].Avatar == "" {
			if a, ok := avatars[members[i].UserID]; ok {
				members[i].Avatar = a
			}
		}
	}
}

// --- Helpers ---

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// getOrgID returns the org ID for the authenticated user, or empty string if not in an org.
func getOrgID(userID string) string {
	m := getMember(userID)
	if m != nil {
		return m.OrgID
	}
	return ""
}

// getMember returns the OrgMember for the authenticated user, or nil.
// Looks up by user_id first, then falls back to email-based matching.
func getMember(userID string) *models.OrgMember {
	var member models.OrgMember
	if err := database.DB.Where("user_id = ?", userID).First(&member).Error; err == nil {
		return &member
	}
	return nil
}

// getMemberFromRequest resolves the caller's OrgMember by user_id,
// falling back to email match + user_id backfill for members added before login.
func getMemberFromRequest(r *http.Request) *models.OrgMember {
	return ensureMemberLinked(UserID(r), UserEmail(r))
}

// ensureMemberLinked finds a member by user_id, or falls back to email only
// when the matching row has NO user_id yet (i.e., an invite that hasn't been
// claimed). This prevents a new account that happens to reuse an email from
// silently adopting a previous account's org membership.
func ensureMemberLinked(userID, email string) *models.OrgMember {
	if userID == "" {
		return nil
	}

	// Try user_id first — the only way an already-linked member is resolved.
	member := getMember(userID)
	if member != nil {
		return member
	}

	// Fallback: claim an unlinked invite (user_id empty) matching this email.
	if email == "" {
		return nil
	}
	var m models.OrgMember
	err := database.DB.
		Where("email = ? AND status = ? AND (user_id IS NULL OR user_id = ?)", email, "active", "").
		First(&m).Error
	if err != nil {
		return nil
	}

	// Backfill user_id on the unlinked invite row.
	database.DB.Model(&m).Update("user_id", userID)
	m.UserID = userID
	return &m
}

// getOrgIDFromRequest resolves the org ID using user_id + email fallback.
func getOrgIDFromRequest(r *http.Request) string {
	m := getMemberFromRequest(r)
	if m != nil {
		return m.OrgID
	}
	return ""
}

// logOrgActivity writes an activity entry.
func logOrgActivity(orgID, action, resourceType, resourceID, resourceName, performedBy string) {
	entry := models.OrgActivity{
		ID:           uuid(),
		OrgID:        orgID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		PerformedBy:  performedBy,
		CreatedAt:    time.Now(),
	}
	database.DB.Create(&entry)
}

// shortCode generates a 6-character uppercase alphanumeric code.
func shortCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I confusion
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	// Strip anything that isn't a-z, 0-9, or hyphen
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// --- Org CRUD ---

// CreateOrg — POST /api/org
func CreateOrg(w http.ResponseWriter, r *http.Request) {
	uid := UserID(r)

	// Check user is not already in an org
	if orgID := getOrgID(uid); orgID != "" {
		WriteJSON(w, 400, map[string]string{"error": "already in an organization"})
		return
	}

	var body struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}

	org := models.Organization{
		ID:      uuid(),
		Name:    strings.TrimSpace(body.Name),
		Slug:    slugify(body.Name),
		Icon:    body.Icon,
		OwnerID: uid,
	}
	if err := database.DB.Create(&org).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create organization"})
		return
	}

	// Seed built-in roles for the new org
	SeedBuiltinRoles(org.ID)

	// Find the Owner role to assign role_id
	var ownerRole models.OrgRole
	database.DB.Where("org_id = ? AND name = ?", org.ID, "Owner").First(&ownerRole)

	// Add creator as owner member
	member := models.OrgMember{
		ID:           uuid(),
		OrgID:        org.ID,
		UserID:       uid,
		Name:         "Owner",
		Email:        "",
		Role:         "owner",
		RoleID:       &ownerRole.ID,
		Status:       "active",
		JoinedAt:     time.Now(),
		LastActiveAt: time.Now(),
	}
	if err := database.DB.Create(&member).Error; err != nil {
		// Rollback org creation if member fails
		database.DB.Delete(&org)
		WriteJSON(w, 500, map[string]string{"error": "failed to create owner member: " + err.Error()})
		return
	}

	logOrgActivity(org.ID, "created", "organization", org.ID, org.Name, member.ID)

	WriteJSON(w, 201, org)
}

// ListOrgsPublic — GET /api/orgs?ids=<id1>,<id2>,...
//
// Batch resolver for the bare-minimum public fields of an org (id, name,
// slug, icon). Used by my.lisaos.dev pages that render lists of org ids
// — space installers, allowlist entries, transfer counterparties — so a user
// sees "Basecode" instead of `org-f3d9-…`. Authentication is still required
// (served behind the gateway's /api/source/ proxy, which runs through the
// session auth_request); we're just not gating by membership since org name
// + slug are the same surface the sign-up flow exposes publicly.
//
// Caps at 100 ids per request to keep the SQL IN() list bounded. Unknown ids
// are silently dropped — clients can detect gaps and show a raw UUID
// fallback.
func ListOrgsPublic(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		WriteJSON(w, 200, map[string]any{"orgs": []any{}})
		return
	}

	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			ids = append(ids, s)
			if len(ids) >= 100 {
				break
			}
		}
	}
	if len(ids) == 0 {
		WriteJSON(w, 200, map[string]any{"orgs": []any{}})
		return
	}

	var orgs []models.Organization
	if err := database.DB.
		Select("id", "name", "slug", "icon").
		Where("id IN ?", ids).
		Find(&orgs).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}

	// Return only the minimal public shape — don't leak owner_id,
	// developer_status, or timestamps from Organization.
	type publicOrg struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Icon string `json:"icon,omitempty"`
	}
	out := make([]publicOrg, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, publicOrg{ID: o.ID, Name: o.Name, Slug: o.Slug, Icon: o.Icon})
	}
	WriteJSON(w, 200, map[string]any{"orgs": out})
}

// GetOrg — GET /api/org
func GetOrg(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{
			"error": "not in an organization",
			"code":  "not_in_org",
		})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", orgID).Error; err != nil {
		WriteJSON(w, 404, map[string]string{
			"error": "organization not found",
			"code":  "org_not_found",
		})
		return
	}

	WriteJSON(w, 200, org)
}

// UpdateOrg — PUT /api/org
func UpdateOrg(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "org.edit") {
		return
	}

	var body struct {
		Name *string `json:"name"`
		Icon *string `json:"icon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", caller.OrgID).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "organization not found"})
		return
	}

	if body.Name != nil {
		org.Name = strings.TrimSpace(*body.Name)
		org.Slug = slugify(*body.Name)
	}
	if body.Icon != nil {
		org.Icon = *body.Icon
	}

	database.DB.Save(&org)
	logOrgActivity(org.ID, "updated", "organization", org.ID, org.Name, caller.ID)

	WriteJSON(w, 200, org)
}

// DeleteOrg — DELETE /api/org
func DeleteOrg(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "org.delete") {
		return
	}

	orgID := caller.OrgID

	// Archive published spaces before deleting the org — NPM-style, existing
	// installs must keep resolving. See plan §9 "Org deletion does not
	// cascade-delete spaces".
	services.ArchiveSpacesForOrg(orgID)

	// Role catalog owned by this org is going away; clean up role rows
	// locally as part of the cascade below. The conditional Developer role
	// is removed alongside the other org_roles rows.
	database.DB.Where("org_id = ?", orgID).Delete(&models.OrgRolePermission{})
	database.DB.Where("org_id = ?", orgID).Delete(&models.OrgRole{})

	// Delete all related data
	database.DB.Where("org_id = ?", orgID).Delete(&models.OrgActivity{})
	database.DB.Where("org_id = ?", orgID).Delete(&models.OrgInvite{})
	database.DB.Where("team_id IN (?)", database.DB.Model(&models.Team{}).Select("id").Where("org_id = ?", orgID)).Delete(&models.TeamMember{})
	database.DB.Where("org_id = ?", orgID).Delete(&models.Team{})
	database.DB.Where("org_id = ?", orgID).Delete(&models.Department{})
	database.DB.Where("org_id = ?", orgID).Delete(&models.OrgMember{})
	database.DB.Delete(&models.Organization{}, "id = ?", orgID)

	WriteJSON(w, 200, map[string]any{"success": true})
}

// --- Members ---

// ListMembers — GET /api/org/members
//
// Supports optional pagination via ?limit=&offset=. When either is set,
// the response is wrapped as {items,total} so the client can render a
// pager. Without params the response is a bare array (unchanged) so
// existing callers that just want everything keep working.
func ListMembers(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	q := r.URL.Query()
	limitStr := q.Get("limit")
	offsetStr := q.Get("offset")
	paginated := limitStr != "" || offsetStr != ""

	tx := database.DB.Where("org_id = ?", orgID)
	if paginated {
		var total int64
		tx.Model(&models.OrgMember{}).Count(&total)

		limit, _ := strconv.Atoi(limitStr)
		if limit <= 0 {
			limit = 20
		}
		if limit > 200 {
			limit = 200
		}
		offset, _ := strconv.Atoi(offsetStr)
		if offset < 0 {
			offset = 0
		}

		var items []models.OrgMember
		tx.Order("LOWER(name) ASC, created_at ASC").Limit(limit).Offset(offset).Find(&items)
		enrichMembersWithAvatars(items)
		WriteJSON(w, 200, map[string]any{"items": items, "total": total})
		return
	}

	var members []models.OrgMember
	tx.Order("LOWER(name) ASC, created_at ASC").Find(&members)
	enrichMembersWithAvatars(members)
	WriteJSON(w, 200, members)
}

// GetMember — GET /api/org/members/{id}
func GetMember(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	WriteJSON(w, 200, member)
}

// CreateMember — POST /api/org/members
func CreateMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.invite") {
		return
	}

	var body struct {
		Name         string  `json:"name"`
		Email        string  `json:"email"`
		Role         string  `json:"role"`
		RoleID       string  `json:"role_id"`
		Title        string  `json:"title"`
		DepartmentID *string `json:"department_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Email) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name and email are required"})
		return
	}

	// Resolve role: prefer role_id, fall back to role name string
	var orgRole models.OrgRole
	if body.RoleID != "" {
		if err := database.DB.Where("id = ? AND org_id = ?", body.RoleID, caller.OrgID).First(&orgRole).Error; err != nil {
			WriteJSON(w, 400, map[string]string{"error": "invalid role_id"})
			return
		}
	} else {
		roleName := body.Role
		if roleName == "" {
			roleName = "member"
		}
		displayName := strings.ToUpper(roleName[:1]) + roleName[1:]
		if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?)", caller.OrgID, displayName).First(&orgRole).Error; err != nil {
			WriteJSON(w, 400, map[string]string{"error": "role not found: " + roleName})
			return
		}
	}
	if isOwnerRoleName(orgRole.Name) {
		WriteJSON(w, 400, map[string]string{"error": "cannot assign Owner role"})
		return
	}
	// SECURITY: can't seat a member with a role granting permissions the
	// caller doesn't hold (privilege escalation via invite).
	if !requireGrantablePermissions(w, caller, rolePermissionStrings(orgRole.ID)) {
		return
	}

	now := time.Now()
	member := models.OrgMember{
		ID:           uuid(),
		OrgID:        caller.OrgID,
		Name:         strings.TrimSpace(body.Name),
		Email:        strings.TrimSpace(body.Email),
		Role:         strings.ToLower(orgRole.Name),
		RoleID:       &orgRole.ID,
		Title:        body.Title,
		DepartmentID: body.DepartmentID,
		Status:       "active",
		JoinedAt:     now,
		LastActiveAt: now,
	}
	if err := database.DB.Create(&member).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create member: " + err.Error()})
		return
	}

	logOrgActivity(caller.OrgID, "added", "member", member.ID, member.Name, caller.ID)

	// Send notification email
	var org models.Organization
	if err := database.DB.First(&org, "id = ?", caller.OrgID).Error; err == nil {
		safeAsync("mailer.member_added", func() { _ = mailer.SendMemberAdded(Cfg, member.Email, caller.Name, org.Name, member.Role) })
	}

	WriteJSON(w, 201, member)
}

// UpdateMember — PUT /api/org/members/{id}
func UpdateMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	// Members can edit themselves; editing others requires members.edit permission
	if caller.ID != member.ID && !HasPermission(caller, "members.edit") {
		WriteJSON(w, 403, map[string]string{"error": "insufficient permissions"})
		return
	}

	var body struct {
		Name         *string `json:"name"`
		Email        *string `json:"email"`
		RoleID       *string `json:"role_id"`
		Title        *string `json:"title"`
		Phone        *string `json:"phone"`
		Bio          *string `json:"bio"`
		Avatar       *string `json:"avatar"`
		Status       *string `json:"status"`
		DepartmentID *string `json:"department_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	// Changing roles requires members.edit and target can't be owner
	if body.RoleID != nil {
		if !HasPermission(caller, "members.edit") {
			WriteJSON(w, 403, map[string]string{"error": "only admins can change roles"})
			return
		}
		if isOwnerRole(&member) {
			WriteJSON(w, 403, map[string]string{"error": "cannot change the owner's role"})
			return
		}
	}

	// Without members.edit, self-editing is limited to profile fields
	if caller.ID == member.ID && !HasPermission(caller, "members.edit") {
		body.RoleID = nil
		body.Status = nil
		body.DepartmentID = nil
		body.Email = nil
	}

	if body.Name != nil {
		member.Name = strings.TrimSpace(*body.Name)
	}
	if body.Email != nil {
		member.Email = strings.TrimSpace(*body.Email)
	}
	if body.RoleID != nil && *body.RoleID != "" {
		var newRole models.OrgRole
		if err := database.DB.Where("id = ? AND org_id = ?", *body.RoleID, caller.OrgID).First(&newRole).Error; err == nil {
			if isOwnerRoleName(newRole.Name) {
				WriteJSON(w, 400, map[string]string{"error": "cannot assign Owner role"})
				return
			}
			// SECURITY: can't grant a role conferring permissions the caller
			// doesn't hold. Without this a members.edit holder could promote
			// themselves (no self-target restriction here) or anyone to Admin.
			if !requireGrantablePermissions(w, caller, rolePermissionStrings(newRole.ID)) {
				return
			}
			member.RoleID = &newRole.ID
			member.Role = strings.ToLower(newRole.Name)
		}
	}
	if body.Title != nil {
		member.Title = *body.Title
	}
	if body.Phone != nil {
		member.Phone = *body.Phone
	}
	if body.Bio != nil {
		member.Bio = *body.Bio
	}
	if body.Avatar != nil {
		member.Avatar = *body.Avatar
	}
	if body.Status != nil {
		member.Status = *body.Status
	}
	if body.DepartmentID != nil {
		member.DepartmentID = body.DepartmentID
	}

	database.DB.Save(&member)
	logOrgActivity(caller.OrgID, "updated", "member", member.ID, member.Name, caller.ID)

	WriteJSON(w, 200, member)
}

// DeleteMember — DELETE /api/org/members/{id}
func DeleteMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.remove") {
		return
	}

	id := r.PathValue("id")

	// Cannot delete yourself
	if id == caller.ID {
		WriteJSON(w, 400, map[string]string{"error": "cannot remove yourself"})
		return
	}

	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	// Cannot delete the owner
	if isOwnerRole(&member) {
		WriteJSON(w, 400, map[string]string{"error": "cannot remove the owner"})
		return
	}

	// Remove from all teams
	database.DB.Where("member_id = ?", member.ID).Delete(&models.TeamMember{})
	database.DB.Delete(&member)

	logOrgActivity(caller.OrgID, "removed", "member", member.ID, member.Name, caller.ID)

	WriteJSON(w, 200, map[string]any{"success": true})
}

// --- Departments ---

// ListDepartments — GET /api/org/departments
func ListDepartments(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var departments []models.Department
	database.DB.Where("org_id = ?", orgID).Find(&departments)

	WriteJSON(w, 200, departments)
}

// GetDepartment — GET /api/org/departments/{id}
func GetDepartment(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var dept models.Department
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&dept).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "department not found"})
		return
	}

	WriteJSON(w, 200, dept)
}

// CreateDepartment — POST /api/org/departments
func CreateDepartment(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.create") {
		return
	}

	var body struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Code        string  `json:"code"`
		HeadID      *string `json:"head_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}

	dept := models.Department{
		ID:          uuid(),
		OrgID:       caller.OrgID,
		Name:        strings.TrimSpace(body.Name),
		Description: body.Description,
		Code:        body.Code,
		HeadID:      body.HeadID,
	}
	if err := database.DB.Create(&dept).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create department"})
		return
	}

	logOrgActivity(caller.OrgID, "created", "department", dept.ID, dept.Name, caller.ID)

	WriteJSON(w, 201, dept)
}

// UpdateDepartment — PUT /api/org/departments/{id}
func UpdateDepartment(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.edit") {
		return
	}

	id := r.PathValue("id")
	var dept models.Department
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&dept).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "department not found"})
		return
	}

	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Code        *string `json:"code"`
		HeadID      *string `json:"head_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	if body.Name != nil {
		dept.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		dept.Description = *body.Description
	}
	if body.Code != nil {
		dept.Code = *body.Code
	}
	if body.HeadID != nil {
		dept.HeadID = body.HeadID
	}

	database.DB.Save(&dept)
	logOrgActivity(caller.OrgID, "updated", "department", dept.ID, dept.Name, caller.ID)

	WriteJSON(w, 200, dept)
}

// DeleteDepartment — DELETE /api/org/departments/{id}
func DeleteDepartment(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.delete") {
		return
	}

	id := r.PathValue("id")
	var dept models.Department
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&dept).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "department not found"})
		return
	}

	// Nullify department_id on members in this department
	database.DB.Model(&models.OrgMember{}).Where("department_id = ? AND org_id = ?", id, caller.OrgID).Update("department_id", nil)
	// Nullify department_id on teams in this department
	database.DB.Model(&models.Team{}).Where("department_id = ? AND org_id = ?", id, caller.OrgID).Update("department_id", nil)

	database.DB.Delete(&dept)
	logOrgActivity(caller.OrgID, "deleted", "department", dept.ID, dept.Name, caller.ID)

	WriteJSON(w, 200, map[string]any{"success": true})
}

// --- Teams ---

// ListTeams — GET /api/org/teams
func ListTeams(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var teams []models.Team
	database.DB.Where("org_id = ?", orgID).Find(&teams)

	WriteJSON(w, 200, teams)
}

// GetTeam — GET /api/org/teams/{id}
func GetTeam(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	WriteJSON(w, 200, team)
}

// CreateTeam — POST /api/org/teams
func CreateTeam(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.create") {
		return
	}

	var body struct {
		Name         string  `json:"name"`
		Description  string  `json:"description"`
		DepartmentID *string `json:"department_id"`
		LeadID       *string `json:"lead_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}

	team := models.Team{
		ID:           uuid(),
		OrgID:        caller.OrgID,
		Name:         strings.TrimSpace(body.Name),
		Description:  body.Description,
		DepartmentID: body.DepartmentID,
		LeadID:       body.LeadID,
	}
	if err := database.DB.Create(&team).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create team"})
		return
	}

	logOrgActivity(caller.OrgID, "created", "team", team.ID, team.Name, caller.ID)

	WriteJSON(w, 201, team)
}

// UpdateTeam — PUT /api/org/teams/{id}
func UpdateTeam(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.edit") {
		return
	}

	id := r.PathValue("id")
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	var body struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		DepartmentID *string `json:"department_id"`
		LeadID       *string `json:"lead_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	if body.Name != nil {
		team.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		team.Description = *body.Description
	}
	if body.DepartmentID != nil {
		team.DepartmentID = body.DepartmentID
	}
	if body.LeadID != nil {
		team.LeadID = body.LeadID
	}

	database.DB.Save(&team)
	logOrgActivity(caller.OrgID, "updated", "team", team.ID, team.Name, caller.ID)

	WriteJSON(w, 200, team)
}

// DeleteTeam — DELETE /api/org/teams/{id}
func DeleteTeam(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.delete") {
		return
	}

	id := r.PathValue("id")
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	// Remove all team members first
	database.DB.Where("team_id = ?", team.ID).Delete(&models.TeamMember{})
	database.DB.Delete(&team)

	logOrgActivity(caller.OrgID, "deleted", "team", team.ID, team.Name, caller.ID)

	WriteJSON(w, 200, map[string]any{"success": true})
}

// ListTeamMembers — GET /api/org/teams/{id}/members
func ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	// Verify team belongs to org
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	var teamMembers []models.TeamMember
	database.DB.Where("team_id = ?", team.ID).Find(&teamMembers)

	WriteJSON(w, 200, teamMembers)
}

// AddTeamMember — POST /api/org/teams/{id}/members
func AddTeamMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.edit") {
		return
	}

	id := r.PathValue("id")
	// Verify team belongs to org
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	var body struct {
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MemberID == "" {
		WriteJSON(w, 400, map[string]string{"error": "member_id is required"})
		return
	}

	// Verify member belongs to org
	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", body.MemberID, caller.OrgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	// Check if already in team
	var existing models.TeamMember
	if err := database.DB.Where("team_id = ? AND member_id = ?", team.ID, member.ID).First(&existing).Error; err == nil {
		WriteJSON(w, 400, map[string]string{"error": "member already in team"})
		return
	}

	tm := models.TeamMember{
		TeamID:   team.ID,
		MemberID: member.ID,
		JoinedAt: time.Now(),
	}
	database.DB.Create(&tm)

	logOrgActivity(caller.OrgID, "added_member", "team", team.ID, team.Name, caller.ID)

	WriteJSON(w, 201, tm)
}

// RemoveTeamMember — DELETE /api/org/teams/{id}/members/{memberId}
func RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "departments.edit") {
		return
	}

	id := r.PathValue("id")
	memberID := r.PathValue("memberId")

	// Verify team belongs to org
	var team models.Team
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&team).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "team not found"})
		return
	}

	result := database.DB.Where("team_id = ? AND member_id = ?", team.ID, memberID).Delete(&models.TeamMember{})
	if result.RowsAffected == 0 {
		WriteJSON(w, 404, map[string]string{"error": "team member not found"})
		return
	}

	logOrgActivity(caller.OrgID, "removed_member", "team", team.ID, team.Name, caller.ID)

	WriteJSON(w, 200, map[string]any{"success": true})
}

// --- Invites ---

// ListInvites — GET /api/org/invites
func ListInvites(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var invites []models.OrgInvite
	database.DB.Where("org_id = ?", orgID).Order("created_at DESC").Find(&invites)

	WriteJSON(w, 200, invites)
}

// CreateInvite — POST /api/org/invites
func CreateInvite(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.invite") {
		return
	}

	var body struct {
		Email        string  `json:"email"`
		Role         string  `json:"role"`
		DepartmentID *string `json:"department_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		WriteJSON(w, 400, map[string]string{"error": "email is required"})
		return
	}

	role := body.Role
	if role == "" {
		// Honor the org's policy for default invite role. Falls back to
		// "member" when the preference isn't set.
		role = OrgPrefString(caller.OrgID, "members.invite_role_default")
		if role == "" {
			role = "member"
		}
	}
	role = strings.ToLower(strings.TrimSpace(role))

	if role == "" {
		WriteJSON(w, 400, map[string]string{"error": "role is required"})
		return
	}
	roleName := strings.ToUpper(role[:1]) + role[1:]
	var inviteRole models.OrgRole
	if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?)", caller.OrgID, roleName).First(&inviteRole).Error; err != nil {
		WriteJSON(w, 400, map[string]string{"error": "role not found: " + role})
		return
	}
	if isOwnerRoleName(inviteRole.Name) {
		WriteJSON(w, 400, map[string]string{"error": "cannot invite members as Owner"})
		return
	}

	// Check for existing pending invite
	var existing models.OrgInvite
	if err := database.DB.Where("org_id = ? AND email = ? AND status = ?", caller.OrgID, body.Email, "pending").First(&existing).Error; err == nil {
		WriteJSON(w, 400, map[string]string{"error": "pending invite already exists for this email"})
		return
	}

	invite := models.OrgInvite{
		ID:           uuid(),
		OrgID:        caller.OrgID,
		Email:        strings.TrimSpace(body.Email),
		Role:         role,
		DepartmentID: body.DepartmentID,
		InvitedBy:    caller.ID,
		Token:        uuid(),
		Code:         shortCode(),
		Status:       "pending",
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour), // 7 days
	}
	if err := database.DB.Create(&invite).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create invite"})
		return
	}

	logOrgActivity(caller.OrgID, "invited", "invite", invite.ID, invite.Email, caller.ID)

	// Send invitation email
	var org models.Organization
	if err := database.DB.First(&org, "id = ?", caller.OrgID).Error; err == nil {
		safeAsync("mailer.org_invite", func() { _ = mailer.SendOrgInvite(Cfg, invite.Email, caller.Name, org.Name, invite.Token, invite.Code) })
	}

	WriteJSON(w, 201, invite)
}

// RevokeInvite — PUT /api/org/invites/{id}/revoke
func RevokeInvite(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.invite") {
		return
	}

	id := r.PathValue("id")
	var invite models.OrgInvite
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&invite).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "invite not found"})
		return
	}

	if invite.Status != "pending" {
		WriteJSON(w, 400, map[string]string{"error": "invite is not pending"})
		return
	}

	invite.Status = "revoked"
	database.DB.Save(&invite)

	logOrgActivity(caller.OrgID, "revoked", "invite", invite.ID, invite.Email, caller.ID)

	WriteJSON(w, 200, invite)
}

// GetInviteByToken — GET /api/org/invites/{token}/info (no auth required)
// Returns invite preview for the accept screen: email, role, org summary,
// and whether the org is enrolled as a publisher. The frontend uses the
// publisher flag to decide whether to show the personal-developer dormancy
// warning before the user confirms.
func GetInviteByToken(w http.ResponseWriter, r *http.Request) {
	tokenOrCode := r.PathValue("token")

	var invite models.OrgInvite
	err := database.DB.Where("token = ?", tokenOrCode).First(&invite).Error
	if err != nil {
		err = database.DB.Where("code = ?", strings.ToUpper(tokenOrCode)).First(&invite).Error
	}
	if err != nil {
		WriteJSON(w, 404, map[string]string{"error": "invite not found"})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", invite.OrgID).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "organization not found"})
		return
	}

	isPublisher := services.OrgIsEnrolledPublisher(org.ID)

	WriteJSON(w, 200, map[string]any{
		"email":      invite.Email,
		"role":       invite.Role,
		"status":     invite.Status,
		"expires_at": invite.ExpiresAt,
		"org": map[string]any{
			"id":           org.ID,
			"name":         org.Name,
			"slug":         org.Slug,
			"icon":         org.Icon,
			"is_publisher": isPublisher,
		},
	})
}

// AcceptInvite — POST /api/org/invites/{token}/accept (no auth required)
// The {token} path param can be either the full UUID token or the 6-char code.
func AcceptInvite(w http.ResponseWriter, r *http.Request) {
	tokenOrCode := r.PathValue("token")

	var invite models.OrgInvite
	// Try token first, then code
	err := database.DB.Where("token = ?", tokenOrCode).First(&invite).Error
	if err != nil {
		err = database.DB.Where("code = ?", strings.ToUpper(tokenOrCode)).First(&invite).Error
	}
	if err != nil {
		WriteJSON(w, 404, map[string]string{"error": "invite not found"})
		return
	}

	if invite.Status != "pending" {
		WriteJSON(w, 400, map[string]string{"error": "invite is no longer valid"})
		return
	}

	if time.Now().After(invite.ExpiresAt) {
		invite.Status = "expired"
		database.DB.Save(&invite)
		WriteJSON(w, 400, map[string]string{"error": "invite has expired"})
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// SECURITY: bind membership to the AUTHENTICATED caller, never a
	// client-supplied user_id. Previously body.user_id was trusted, so anyone
	// holding (or guessing) an invite token/code could enroll an arbitrary —
	// even a victim's — user id. The route is now behind auth (main.go).
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, 401, map[string]string{"error": "authentication required to accept an invite"})
		return
	}

	// Defense in depth against code brute-forcing: the accepting account's
	// email must match the address the invite was sent to. Only enforced when
	// the gateway/auth layer gave us the caller's email (it normally does).
	if callerEmail := strings.TrimSpace(r.Header.Get("X-User-Email")); callerEmail != "" {
		if !strings.EqualFold(callerEmail, strings.TrimSpace(invite.Email)) {
			WriteJSON(w, 403, map[string]string{"error": "this invite was sent to a different email address"})
			return
		}
	}

	// Single-org constraint: reject if user already belongs to any org.
	if orgID := getOrgID(userID); orgID != "" {
		WriteJSON(w, 409, map[string]string{
			"error": "user is already in an organization",
			"code":  "already_in_org",
		})
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = invite.Email
	}

	// Resolve role_id from the invite's role name
	var roleID *string
	inviteRoleName := strings.TrimSpace(invite.Role)
	if inviteRoleName == "" {
		inviteRoleName = "member"
	}
	roleName := strings.ToUpper(inviteRoleName[:1]) + inviteRoleName[1:]
	var inviteRole models.OrgRole
	if err := database.DB.Where("org_id = ? AND LOWER(name) = LOWER(?)", invite.OrgID, roleName).First(&inviteRole).Error; err == nil {
		if isOwnerRoleName(inviteRole.Name) {
			WriteJSON(w, 400, map[string]string{"error": "cannot accept Owner role invitations"})
			return
		}
		roleID = &inviteRole.ID
	}

	now := time.Now()
	member := models.OrgMember{
		ID:           uuid(),
		OrgID:        invite.OrgID,
		UserID:       userID,
		Name:         name,
		Email:        invite.Email,
		Role:         invite.Role,
		RoleID:       roleID,
		DepartmentID: invite.DepartmentID,
		Status:       "active",
		JoinedAt:     now,
		LastActiveAt: now,
	}
	if err := database.DB.Create(&member).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to join organization"})
		return
	}

	invite.Status = "accepted"
	database.DB.Save(&invite)

	logOrgActivity(invite.OrgID, "joined", "member", member.ID, member.Name, member.ID)

	WriteJSON(w, 200, member)
}

// --- Activity ---

// ListActivity — GET /api/org/activity
func ListActivity(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	// Pagination
	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	var activities []models.OrgActivity
	database.DB.Where("org_id = ?", orgID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&activities)

	var total int64
	database.DB.Model(&models.OrgActivity{}).Where("org_id = ?", orgID).Count(&total)

	WriteJSON(w, 200, map[string]any{
		"data":  activities,
		"total": total,
	})
}

// --- Org Provider Keys ---

// ListOrgProviders — GET /api/org/providers
func ListOrgProviders(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var keys []models.OrgProviderKey
	database.DB.Where("org_id = ?", orgID).Find(&keys)

	// Return provider names + masked keys (never expose full key)
	type providerEntry struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Masked   string `json:"masked_key"`
		Enforced bool   `json:"enforced"`
		SetBy    string `json:"set_by"`
	}
	entries := make([]providerEntry, len(keys))
	for i, k := range keys {
		masked := "****"
		if len(k.APIKey) > 8 {
			masked = k.APIKey[:4] + "****" + k.APIKey[len(k.APIKey)-4:]
		}
		entries[i] = providerEntry{ID: k.ID, Provider: k.Provider, Masked: masked, Enforced: k.Enforced, SetBy: k.SetBy}
	}
	WriteJSON(w, 200, entries)
}

// GetOrgProviderKey — GET /api/org/providers/{provider}/key (for operator bootstrap only)
// Returns the actual API key. Requires the caller to be an org member.
func GetOrgProviderKey(w http.ResponseWriter, r *http.Request) {
	// SECURITY: returns the PLAINTEXT provider key, so require providers.manage
	// (matches SetOrgProvider/DeleteOrgProvider) — not mere org membership.
	// Previously any member could read the org's shared LLM credentials.
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "providers.manage") {
		return
	}
	orgID := caller.OrgID

	provider := r.PathValue("provider")
	var key models.OrgProviderKey
	if err := database.DB.Where("org_id = ? AND provider = ?", orgID, provider).First(&key).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "no key for this provider"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"provider": key.Provider,
		"api_key":  key.APIKey,
		"enforced": key.Enforced,
	})
}

// SetOrgProvider — PUT /api/org/providers/{provider}
func SetOrgProvider(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "providers.manage") {
		return
	}

	providerName := r.PathValue("provider")
	var body struct {
		APIKey   string `json:"api_key"`
		Enforced *bool  `json:"enforced,omitempty"`
	}
	// `api_key` is optional when a key already exists and the caller is
	// only toggling `enforced`. Reject only when both are missing.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	trimmedKey := strings.TrimSpace(body.APIKey)

	var existing models.OrgProviderKey
	err := database.DB.Where("org_id = ? AND provider = ?", caller.OrgID, providerName).First(&existing).Error
	if err == nil {
		if trimmedKey != "" {
			existing.APIKey = trimmedKey
		}
		if body.Enforced != nil {
			existing.Enforced = *body.Enforced
		}
		existing.SetBy = caller.ID
		database.DB.Save(&existing)
		logOrgActivity(caller.OrgID, "updated", "provider_key", existing.ID, providerName, caller.ID)
		WriteJSON(w, 200, map[string]any{"status": "updated", "provider": providerName, "enforced": existing.Enforced})
		return
	}

	if trimmedKey == "" {
		WriteJSON(w, 400, map[string]string{"error": "api_key is required"})
		return
	}
	key := models.OrgProviderKey{
		ID:       uuid(),
		OrgID:    caller.OrgID,
		Provider: providerName,
		APIKey:   trimmedKey,
		Enforced: derefBool(body.Enforced, false),
		SetBy:    caller.ID,
	}
	if err := database.DB.Create(&key).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to save key"})
		return
	}

	logOrgActivity(caller.OrgID, "created", "provider_key", key.ID, providerName, caller.ID)
	WriteJSON(w, 201, map[string]any{"status": "created", "provider": providerName, "enforced": key.Enforced})
}

// DeleteOrgProvider — DELETE /api/org/providers/{provider}
func DeleteOrgProvider(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "providers.manage") {
		return
	}

	providerName := r.PathValue("provider")
	result := database.DB.Where("org_id = ? AND provider = ?", caller.OrgID, providerName).Delete(&models.OrgProviderKey{})
	if result.RowsAffected == 0 {
		WriteJSON(w, 404, map[string]string{"error": "no key for this provider"})
		return
	}

	logOrgActivity(caller.OrgID, "deleted", "provider_key", "", providerName, caller.ID)
	WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Org Settings ---

// GetOrgSettings — GET /api/org/settings
func GetOrgSettings(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var settings []models.OrgSetting
	database.DB.Where("org_id = ?", orgID).Find(&settings)

	result := make(map[string]string, len(settings))
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	WriteJSON(w, 200, result)
}

// SetOrgSetting — PUT /api/org/settings/{key}
func SetOrgSetting(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "org.edit") {
		return
	}

	key := r.PathValue("key")
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}

	var existing models.OrgSetting
	err := database.DB.Where("org_id = ? AND key = ?", caller.OrgID, key).First(&existing).Error
	if err == nil {
		existing.Value = body.Value
		database.DB.Save(&existing)
	} else {
		database.DB.Create(&models.OrgSetting{
			ID:    uuid(),
			OrgID: caller.OrgID,
			Key:   key,
			Value: body.Value,
		})
	}

	WriteJSON(w, 200, map[string]string{"status": "saved", "key": key})
}

// DeleteOrgSetting — DELETE /api/org/settings/{key}
func DeleteOrgSetting(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "org.edit") {
		return
	}

	key := r.PathValue("key")
	database.DB.Where("org_id = ? AND key = ?", caller.OrgID, key).Delete(&models.OrgSetting{})
	WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Org Projects ---

// ListOrgProjects — GET /api/org/projects
func ListOrgProjects(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	var projects []models.OrgProject
	database.DB.Where("org_id = ?", orgID).Order("name ASC").Find(&projects)
	WriteJSON(w, 200, projects)
}

// GetOrgProject — GET /api/org/projects/{id}
func GetOrgProject(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}
	WriteJSON(w, 200, project)
}

// CreateOrgProject — POST /api/org/projects
func CreateOrgProject(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.create") {
		return
	}

	var body struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		RepoURL       string `json:"repo_url"`
		DefaultBranch string `json:"default_branch"`
		Framework     string `json:"framework"`
		Visibility    string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}

	branch := strings.TrimSpace(body.DefaultBranch)
	if branch == "" {
		branch = "main"
	}

	// Visibility falls back to the org's policy — private/internal/public.
	// Unknown values are coerced to "private" to avoid surprise exposure.
	visibility := strings.ToLower(strings.TrimSpace(body.Visibility))
	if visibility == "" {
		visibility = OrgPrefString(caller.OrgID, "projects.default_visibility")
	}
	switch visibility {
	case "private", "internal", "public":
	default:
		visibility = "private"
	}

	project := models.OrgProject{
		ID:            uuid(),
		OrgID:         caller.OrgID,
		Name:          strings.TrimSpace(body.Name),
		Description:   strings.TrimSpace(body.Description),
		RepoURL:       strings.TrimSpace(body.RepoURL),
		DefaultBranch: branch,
		Framework:     strings.TrimSpace(body.Framework),
		Visibility:    visibility,
		CreatedBy:     caller.ID,
	}
	if err := database.DB.Create(&project).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to create project"})
		return
	}

	logOrgActivity(caller.OrgID, "created", "project", project.ID, project.Name, caller.ID)
	WriteJSON(w, 201, project)
}

// UpdateOrgProject — PUT /api/org/projects/{id}
func UpdateOrgProject(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.edit") {
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	var body struct {
		Name          *string `json:"name"`
		Description   *string `json:"description"`
		RepoURL       *string `json:"repo_url"`
		DefaultBranch *string `json:"default_branch"`
		Framework     *string `json:"framework"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	if body.Name != nil {
		project.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		project.Description = strings.TrimSpace(*body.Description)
	}
	if body.RepoURL != nil {
		project.RepoURL = strings.TrimSpace(*body.RepoURL)
	}
	if body.DefaultBranch != nil {
		project.DefaultBranch = strings.TrimSpace(*body.DefaultBranch)
	}
	if body.Framework != nil {
		project.Framework = strings.TrimSpace(*body.Framework)
	}

	database.DB.Save(&project)
	logOrgActivity(caller.OrgID, "updated", "project", project.ID, project.Name, caller.ID)
	WriteJSON(w, 200, project)
}

// DeleteOrgProject — DELETE /api/org/projects/{id}
func DeleteOrgProject(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.delete") {
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	database.DB.Delete(&project)
	logOrgActivity(caller.OrgID, "deleted", "project", project.ID, project.Name, caller.ID)
	WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Project Repos ---

// ListProjectRepos — GET /api/org/projects/{id}/repos
func ListProjectRepos(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	var repos []models.OrgProjectRepo
	database.DB.Where("project_id = ?", project.ID).Order("created_at ASC").Find(&repos)
	WriteJSON(w, 200, repos)
}

// AddProjectRepo — POST /api/org/projects/{id}/repos
func AddProjectRepo(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.edit") {
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	var body struct {
		Name          string `json:"name"`
		RepoURL       string `json:"repo_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.RepoURL) == "" {
		WriteJSON(w, 400, map[string]string{"error": "repo_url is required"})
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		// Derive name from repo URL: "https://github.com/org/api" → "api"
		parts := strings.Split(body.RepoURL, "/")
		name = strings.TrimSuffix(parts[len(parts)-1], ".git")
	}

	branch := strings.TrimSpace(body.DefaultBranch)
	if branch == "" {
		branch = "main"
	}

	repo := models.OrgProjectRepo{
		ID:            uuid(),
		ProjectID:     project.ID,
		Name:          name,
		RepoURL:       strings.TrimSpace(body.RepoURL),
		DefaultBranch: branch,
	}
	if err := database.DB.Create(&repo).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to add repo"})
		return
	}

	logOrgActivity(caller.OrgID, "added", "project_repo", repo.ID, project.Name+"/"+repo.Name, caller.ID)
	WriteJSON(w, 201, repo)
}

// RemoveProjectRepo — DELETE /api/org/projects/{id}/repos/{repoId}
func RemoveProjectRepo(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.edit") {
		return
	}

	id := r.PathValue("id")
	repoID := r.PathValue("repoId")

	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	result := database.DB.Where("id = ? AND project_id = ?", repoID, project.ID).Delete(&models.OrgProjectRepo{})
	if result.RowsAffected == 0 {
		WriteJSON(w, 404, map[string]string{"error": "repo not found"})
		return
	}

	logOrgActivity(caller.OrgID, "removed", "project_repo", repoID, project.Name, caller.ID)
	WriteJSON(w, 200, map[string]string{"status": "removed"})
}

// --- Project Members ---

// ListProjectMembers — GET /api/org/projects/{id}/members
func ListProjectMembers(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, orgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	var assignments []models.OrgProjectMember
	database.DB.Where("project_id = ?", project.ID).Find(&assignments)

	// Return full member objects
	memberIDs := make([]string, len(assignments))
	for i, a := range assignments {
		memberIDs[i] = a.MemberID
	}

	var members []models.OrgMember
	if len(memberIDs) > 0 {
		database.DB.Where("id IN ? AND org_id = ?", memberIDs, orgID).Find(&members)
	}

	WriteJSON(w, 200, members)
}

// AddProjectMember — POST /api/org/projects/{id}/members
func AddProjectMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.assign_members") {
		return
	}

	id := r.PathValue("id")
	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	var body struct {
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MemberID == "" {
		WriteJSON(w, 400, map[string]string{"error": "member_id is required"})
		return
	}

	// Verify member exists in org
	var member models.OrgMember
	if err := database.DB.Where("id = ? AND org_id = ?", body.MemberID, caller.OrgID).First(&member).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "member not found"})
		return
	}

	// Check not already assigned
	var existing models.OrgProjectMember
	if err := database.DB.Where("project_id = ? AND member_id = ?", project.ID, member.ID).First(&existing).Error; err == nil {
		WriteJSON(w, 400, map[string]string{"error": "member already assigned"})
		return
	}

	pm := models.OrgProjectMember{
		ProjectID:  project.ID,
		MemberID:   member.ID,
		AssignedAt: time.Now(),
	}
	database.DB.Create(&pm)

	logOrgActivity(caller.OrgID, "added_member", "project", project.ID, project.Name+" <- "+member.Name, caller.ID)
	WriteJSON(w, 201, pm)
}

// RemoveProjectMember — DELETE /api/org/projects/{id}/members/{memberId}
func RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "projects.assign_members") {
		return
	}

	id := r.PathValue("id")
	memberID := r.PathValue("memberId")

	var project models.OrgProject
	if err := database.DB.Where("id = ? AND org_id = ?", id, caller.OrgID).First(&project).Error; err != nil {
		WriteJSON(w, 404, map[string]string{"error": "project not found"})
		return
	}

	result := database.DB.Where("project_id = ? AND member_id = ?", project.ID, memberID).Delete(&models.OrgProjectMember{})
	if result.RowsAffected == 0 {
		WriteJSON(w, 404, map[string]string{"error": "member not assigned to project"})
		return
	}

	logOrgActivity(caller.OrgID, "removed_member", "project", project.ID, project.Name, caller.ID)
	WriteJSON(w, 200, map[string]string{"status": "removed"})
}

// --- Membership & Managed Settings ---

// GetMembership — GET /api/org/membership
// Returns the caller's org membership info, or 404 if not in any org.
// Used by the Construct app to auto-switch to org profile on login.
// Looks up by user_id first, then by email as fallback (for members added before UUID migration).
func GetMembership(w http.ResponseWriter, r *http.Request) {
	member := ensureMemberLinked(UserID(r), UserEmail(r))
	if member == nil {
		WriteJSON(w, 404, map[string]string{
			"error": "not in any organization",
			"code":  "not_in_org",
		})
		return
	}

	var org models.Organization
	if err := database.DB.First(&org, "id = ?", member.OrgID).Error; err != nil {
		WriteJSON(w, 404, map[string]string{
			"error": "organization not found",
			"code":  "org_not_found",
		})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"org_id":           org.ID,
		"org_name":         org.Name,
		"org_slug":         org.Slug,
		"org_icon":         org.Icon,
		"role":             member.Role,
		"member_id":        member.ID,
		"developer_status": org.DeveloperStatus,
	})
}

// GetManagedSettings — GET /api/org/managed-settings
// Returns the org's managed settings bundle (providers, mcp, skills, hooks).
// Members get manageable: false, admins get manageable: true.
func GetManagedSettings(w http.ResponseWriter, r *http.Request) {
	member := ensureMemberLinked(UserID(r), UserEmail(r))
	if member == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in any organization"})
		return
	}

	isAdmin := isOwnerRole(member) || HasPermission(member, "org.edit")

	// Fetch org provider keys (masked for non-admins)
	var providerKeys []models.OrgProviderKey
	database.DB.Where("org_id = ?", member.OrgID).Find(&providerKeys)

	providers := make([]map[string]any, len(providerKeys))
	for i, pk := range providerKeys {
		providers[i] = map[string]any{
			"id":       pk.ID,
			"provider": pk.Provider,
			"set_by":   pk.SetBy,
		}
	}

	// Fetch org settings (for MCP, skills, hooks)
	var settings []models.OrgSetting
	database.DB.Where("org_id = ?", member.OrgID).Find(&settings)

	settingsMap := make(map[string]string)
	for _, s := range settings {
		settingsMap[s.Key] = s.Value
	}

	// Fetch org name for badge display
	var org models.Organization
	database.DB.First(&org, "id = ?", member.OrgID)

	WriteJSON(w, 200, map[string]any{
		"org_id":     member.OrgID,
		"org_name":   org.Name,
		"manageable": isAdmin,
		"providers":  providers,
		"settings":   settingsMap,
	})
}

// GetOrgInsights — GET /api/org/insights
// Returns aggregated org metrics. Admin only.
func GetOrgInsights(w http.ResponseWriter, r *http.Request) {
	uid := UserID(r)
	member := getMember(uid)
	if member == nil {
		WriteJSON(w, 403, map[string]string{"error": "not in any organization"})
		return
	}
	if !requirePermission(w, member, "insights.view") {
		return
	}

	var memberCount int64
	database.DB.Model(&models.OrgMember{}).Where("org_id = ?", member.OrgID).Count(&memberCount)

	var projectCount int64
	database.DB.Model(&models.OrgProject{}).Where("org_id = ?", member.OrgID).Count(&projectCount)

	var providerCount int64
	database.DB.Model(&models.OrgProviderKey{}).Where("org_id = ?", member.OrgID).Count(&providerCount)

	var departmentCount int64
	database.DB.Model(&models.Department{}).Where("org_id = ?", member.OrgID).Count(&departmentCount)

	var teamCount int64
	database.DB.Model(&models.Team{}).Where("org_id = ?", member.OrgID).Count(&teamCount)

	var activeMembers int64
	database.DB.Model(&models.OrgMember{}).Where("org_id = ? AND status = ?", member.OrgID, "active").Count(&activeMembers)

	var pendingInvites int64
	database.DB.Model(&models.OrgInvite{}).Where("org_id = ? AND status = ?", member.OrgID, "pending").Count(&pendingInvites)

	WriteJSON(w, 200, map[string]any{
		"members":         memberCount,
		"active_members":  activeMembers,
		"projects":        projectCount,
		"providers":       providerCount,
		"departments":     departmentCount,
		"teams":           teamCount,
		"pending_invites": pendingInvites,
	})
}

// --- Org Spaces (pinned marketplace spaces) ---

// ListOrgSpaces — GET /api/org/spaces
// Returns the org's pinned space IDs. Any member can read; admins
// curate via PinOrgSpace / UnpinOrgSpace.
func ListOrgSpaces(w http.ResponseWriter, r *http.Request) {
	orgID := getOrgIDFromRequest(r)
	if orgID == "" {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	var spaces []models.OrgSpace
	database.DB.Where("org_id = ?", orgID).Order("created_at ASC").Find(&spaces)
	WriteJSON(w, 200, spaces)
}

// PinOrgSpace — POST /api/org/spaces  body: {space_id}
// Pins a marketplace space to the org. Members of this org will
// auto-install it on next login. Idempotent: pinning an already-
// pinned space returns the existing record.
func PinOrgSpace(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.edit") {
		return
	}

	var body struct {
		SpaceID string `json:"space_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	body.SpaceID = strings.TrimSpace(body.SpaceID)
	if body.SpaceID == "" {
		WriteJSON(w, 400, map[string]string{"error": "space_id required"})
		return
	}

	var existing models.OrgSpace
	if err := database.DB.Where("org_id = ? AND space_id = ?", caller.OrgID, body.SpaceID).First(&existing).Error; err == nil {
		WriteJSON(w, 200, existing)
		return
	}

	pin := models.OrgSpace{
		ID:       uuid(),
		OrgID:    caller.OrgID,
		SpaceID:  body.SpaceID,
		PinnedBy: caller.ID,
	}
	if err := database.DB.Create(&pin).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to pin space"})
		return
	}
	WriteJSON(w, 201, pin)
}

// UnpinOrgSpace — DELETE /api/org/spaces/{spaceId}
// Removes the pin. Does NOT uninstall the space from members'
// machines — they keep it locally; only the auto-sync stops.
func UnpinOrgSpace(w http.ResponseWriter, r *http.Request) {
	caller := getMemberFromRequest(r)
	if caller == nil {
		WriteJSON(w, 404, map[string]string{"error": "not in an organization"})
		return
	}
	if !requirePermission(w, caller, "members.edit") {
		return
	}
	spaceID := r.PathValue("spaceId")
	if spaceID == "" {
		WriteJSON(w, 400, map[string]string{"error": "spaceId required"})
		return
	}
	res := database.DB.Where("org_id = ? AND space_id = ?", caller.OrgID, spaceID).Delete(&models.OrgSpace{})
	if res.Error != nil {
		WriteJSON(w, 500, map[string]string{"error": "failed to unpin"})
		return
	}
	if res.RowsAffected == 0 {
		WriteJSON(w, 404, map[string]string{"error": "not pinned"})
		return
	}
	WriteJSON(w, 200, map[string]bool{"ok": true})
}
