package handlers

// Admin endpoints — cross-org read/moderation gated by X-Internal-Secret.
// Intended for Oracle, which proxies these under /api/source/*.

import (
	"net/http"
	"strconv"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// GET /api/admin/orgs?q=&page=&limit=
func AdminListOrgs(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	q := r.URL.Query().Get("q")
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	limit := parseIntDefault(r.URL.Query().Get("limit"), 20)
	if limit > 200 {
		limit = 200
	}
	offset := (page - 1) * limit

	db := database.DB.Model(&models.Organization{})
	if q != "" {
		like := "%" + q + "%"
		db = db.Where("name LIKE ? OR slug LIKE ?", like, like)
	}

	var total int64
	db.Count(&total)

	var orgs []models.Organization
	db.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orgs)

	// Enrich each row with member/project/team counts so the list renders a
	// meaningful at-a-glance view without N+1 detail calls from the client.
	items := make([]map[string]any, 0, len(orgs))
	for _, o := range orgs {
		var memberCount, projectCount, teamCount, pendingInviteCount int64
		database.DB.Model(&models.OrgMember{}).Where("org_id = ?", o.ID).Count(&memberCount)
		database.DB.Model(&models.OrgProject{}).Where("org_id = ?", o.ID).Count(&projectCount)
		database.DB.Model(&models.Team{}).Where("org_id = ?", o.ID).Count(&teamCount)
		database.DB.Model(&models.OrgInvite{}).Where("org_id = ? AND status = ?", o.ID, "pending").Count(&pendingInviteCount)
		items = append(items, map[string]any{
			"id":                    o.ID,
			"name":                  o.Name,
			"slug":                  o.Slug,
			"icon":                  o.Icon,
			"owner_id":              o.OwnerID,
			"developer_status":      o.DeveloperStatus,
			"member_count":          memberCount,
			"project_count":         projectCount,
			"team_count":            teamCount,
			"pending_invite_count":  pendingInviteCount,
			"created_at":            o.CreatedAt.Format(time.RFC3339),
			"updated_at":            o.UpdatedAt.Format(time.RFC3339),
		})
	}

	WriteJSON(w, 200, map[string]any{
		"orgs":  items,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GET /api/admin/orgs/{id}
func AdminGetOrg(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	var org models.Organization
	if err := database.DB.First(&org, "id = ?", id).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "organization not found"})
		return
	}

	var memberCount, projectCount, teamCount, pendingInviteCount int64
	database.DB.Model(&models.OrgMember{}).Where("org_id = ?", org.ID).Count(&memberCount)
	database.DB.Model(&models.OrgProject{}).Where("org_id = ?", org.ID).Count(&projectCount)
	database.DB.Model(&models.Team{}).Where("org_id = ?", org.ID).Count(&teamCount)
	database.DB.Model(&models.OrgInvite{}).Where("org_id = ? AND status = ?", org.ID, "pending").Count(&pendingInviteCount)

	var owner models.OrgMember
	var ownerInfo map[string]any
	if err := database.DB.Where("org_id = ? AND user_id = ?", org.ID, org.OwnerID).First(&owner).Error; err == nil {
		ownerInfo = map[string]any{
			"id":    owner.ID,
			"name":  owner.Name,
			"email": owner.Email,
		}
	}

	WriteJSON(w, 200, map[string]any{
		"data":                 org,
		"owner":                ownerInfo,
		"member_count":         memberCount,
		"project_count":        projectCount,
		"team_count":           teamCount,
		"pending_invite_count": pendingInviteCount,
	})
}

// GET /api/admin/orgs/{id}/members
func AdminListOrgMembers(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	var members []models.OrgMember
	if err := database.DB.Where("org_id = ?", id).Order("joined_at DESC").Find(&members).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "failed to list members"})
		return
	}
	WriteJSON(w, 200, map[string]any{"members": members, "total": len(members)})
}

// GET /api/admin/orgs/{id}/projects
func AdminListOrgProjects(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	var projects []models.OrgProject
	if err := database.DB.Where("org_id = ?", id).Order("updated_at DESC").Find(&projects).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "failed to list projects"})
		return
	}
	// Enrich with member + repo counts.
	items := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		var memberCount, repoCount int64
		database.DB.Model(&models.OrgProjectMember{}).Where("project_id = ?", p.ID).Count(&memberCount)
		database.DB.Model(&models.OrgProjectRepo{}).Where("project_id = ?", p.ID).Count(&repoCount)
		items = append(items, map[string]any{
			"id":             p.ID,
			"org_id":         p.OrgID,
			"name":           p.Name,
			"description":    p.Description,
			"repo_url":       p.RepoURL,
			"default_branch": p.DefaultBranch,
			"framework":      p.Framework,
			"visibility":     p.Visibility,
			"created_by":     p.CreatedBy,
			"member_count":   memberCount,
			"repo_count":     repoCount,
			"created_at":     p.CreatedAt.Format(time.RFC3339),
			"updated_at":     p.UpdatedAt.Format(time.RFC3339),
		})
	}
	WriteJSON(w, 200, map[string]any{"projects": items, "total": len(items)})
}

// GET /api/admin/orgs/{id}/teams
func AdminListOrgTeams(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	var teams []models.Team
	if err := database.DB.Where("org_id = ?", id).Order("created_at DESC").Find(&teams).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "failed to list teams"})
		return
	}
	items := make([]map[string]any, 0, len(teams))
	for _, t := range teams {
		var memberCount int64
		database.DB.Model(&models.TeamMember{}).Where("team_id = ?", t.ID).Count(&memberCount)
		items = append(items, map[string]any{
			"id":            t.ID,
			"org_id":        t.OrgID,
			"name":          t.Name,
			"description":   t.Description,
			"department_id": t.DepartmentID,
			"lead_id":       t.LeadID,
			"member_count":  memberCount,
			"created_at":    t.CreatedAt.Format(time.RFC3339),
			"updated_at":    t.UpdatedAt.Format(time.RFC3339),
		})
	}
	WriteJSON(w, 200, map[string]any{"teams": items, "total": len(items)})
}

// GET /api/admin/orgs/{id}/invites?status=pending|accepted|revoked
func AdminListOrgInvites(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	db := database.DB.Where("org_id = ?", id)
	if status := r.URL.Query().Get("status"); status != "" {
		db = db.Where("status = ?", status)
	}
	var invites []models.OrgInvite
	if err := db.Order("created_at DESC").Find(&invites).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "failed to list invites"})
		return
	}
	WriteJSON(w, 200, map[string]any{"invites": invites, "total": len(invites)})
}

// PUT /api/admin/org-invites/{id}/revoke
// Admin revoke bypasses the same-org auth that the normal revoke endpoint
// requires — Oracle staff may not be a member of the target org.
func AdminRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	var invite models.OrgInvite
	if err := database.DB.First(&invite, "id = ?", id).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "invite not found"})
		return
	}
	if invite.Status != "pending" {
		WriteJSON(w, 400, map[string]any{"error": "invite is not pending"})
		return
	}
	if err := database.DB.Model(&invite).Update("status", "revoked").Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "failed to revoke"})
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}
