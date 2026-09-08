package models

import (
	"strings"
	"time"
)

// OrgRole defines a named role with permissions within an organization.
type OrgRole struct {
	ID          string    `json:"id" gorm:"primarykey;size:36"`
	OrgID       string    `json:"org_id" gorm:"uniqueIndex:idx_org_role_name;size:36;not null"`
	Name        string    `json:"name" gorm:"uniqueIndex:idx_org_role_name;size:100;not null"`
	Description string    `json:"description" gorm:"type:text"`
	IsBuiltin   bool      `json:"is_builtin" gorm:"default:false"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (OrgRole) TableName() string { return "org_roles" }

// OrgRolePermission links a permission string to a role.
type OrgRolePermission struct {
	ID         string `json:"id" gorm:"primarykey;size:36"`
	RoleID     string `json:"role_id" gorm:"uniqueIndex:idx_role_permission;size:36;not null"`
	Permission string `json:"permission" gorm:"uniqueIndex:idx_role_permission;size:200;not null"`
}

func (OrgRolePermission) TableName() string { return "org_role_permissions" }

// RoleWithPermissions is a role joined with its permission strings.
type RoleWithPermissions struct {
	OrgRole
	Permissions []string `json:"permissions" gorm:"-"`
}

// PermissionDef describes a single grantable permission.
type PermissionDef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// AllPermissions is the canonical list of permissions.
var AllPermissions = []PermissionDef{
	// Organization
	{ID: "org.view", Label: "View Organization", Group: "Organization"},
	{ID: "org.edit", Label: "Edit Organization", Group: "Organization"},
	{ID: "org.settings.edit", Label: "Edit Organization Settings", Group: "Organization"},
	{ID: "org.delete", Label: "Delete Organization", Group: "Organization"},
	{ID: "org.transfer", Label: "Transfer Ownership", Group: "Organization"},

	// Members
	{ID: "members.view", Label: "View Members", Group: "Members"},
	{ID: "members.invite", Label: "Invite Members", Group: "Members"},
	{ID: "members.edit", Label: "Edit Members", Group: "Members"},
	{ID: "members.remove", Label: "Remove Members", Group: "Members"},

	// Roles
	{ID: "roles.view", Label: "View Roles", Group: "Roles"},
	{ID: "roles.create", Label: "Create Roles", Group: "Roles"},
	{ID: "roles.edit", Label: "Edit Roles", Group: "Roles"},
	{ID: "roles.delete", Label: "Delete Roles", Group: "Roles"},

	// Departments
	{ID: "departments.view", Label: "View Departments", Group: "Departments"},
	{ID: "departments.create", Label: "Create Departments", Group: "Departments"},
	{ID: "departments.edit", Label: "Edit Departments", Group: "Departments"},
	{ID: "departments.delete", Label: "Delete Departments", Group: "Departments"},

	// Projects
	{ID: "projects.view", Label: "View Projects", Group: "Projects"},
	{ID: "projects.create", Label: "Create Projects", Group: "Projects"},
	{ID: "projects.edit", Label: "Edit Projects", Group: "Projects"},
	{ID: "projects.delete", Label: "Delete Projects", Group: "Projects"},
	{ID: "projects.assign_members", Label: "Assign Members to Projects", Group: "Projects"},
	{ID: "projects.code", Label: "Write Code", Group: "Projects"},

	// Providers
	{ID: "providers.view", Label: "View Providers", Group: "AI"},
	{ID: "providers.manage", Label: "Manage Provider Keys", Group: "AI"},

	// MCP
	{ID: "mcp.view", Label: "View MCP Servers", Group: "AI"},
	{ID: "mcp.manage", Label: "Manage MCP Servers", Group: "AI"},

	// Skills
	{ID: "skills.view", Label: "View Skills", Group: "AI"},
	{ID: "skills.manage", Label: "Manage Skills", Group: "AI"},

	// Hooks
	{ID: "hooks.view", Label: "View Hooks", Group: "AI"},
	{ID: "hooks.manage", Label: "Manage Hooks", Group: "AI"},

	// Activity & Insights
	{ID: "activity.view", Label: "View Activity Log", Group: "Insights"},
	{ID: "insights.view", Label: "View Insights", Group: "Insights"},

	// Spaces (generic — per-space permissions use spaces.{id}.access pattern)
	{ID: "spaces.coder.access", Label: "Access Coder", Group: "Spaces"},
	{ID: "spaces.editor.access", Label: "Access Editor", Group: "Spaces"},
	{ID: "spaces.architect.access", Label: "Access Architect", Group: "Spaces"},
	{ID: "spaces.brainstorm.access", Label: "Access Brainstorm", Group: "Spaces"},
	{ID: "spaces.project.access", Label: "Access Project", Group: "Spaces"},
	{ID: "spaces.deployment.access", Label: "Access Deployment", Group: "Spaces"},
	{ID: "spaces.development.access", Label: "Access Development", Group: "Spaces"},

	// Developer (conditionally seeded when org enrolls as publisher)
	{ID: "developer.publish", Label: "Publish Spaces", Group: "Developer"},
	{ID: "developer.transfer", Label: "Transfer Space Ownership", Group: "Developer"},
	{ID: "developer.cli", Label: "Generate CLI Tokens", Group: "Developer"},
}

// BuiltinRoleDef defines a built-in role template used to seed new orgs.
type BuiltinRoleDef struct {
	Name        string
	Description string
	Permissions []string // "*" means all permissions
}

// BuiltinRoles is the set of roles seeded when an org is created.
var BuiltinRoles = []BuiltinRoleDef{
	{
		Name:        "Owner",
		Description: "Full control over the organization. Can transfer ownership and delete the org.",
		Permissions: []string{"*"},
	},
	{
		Name:        "Admin",
		Description: "Manage members, settings, and all resources. Cannot delete org or transfer ownership.",
		Permissions: []string{
			"org.view", "org.edit", "org.settings.edit",
			"members.*",
			"roles.*",
			"departments.*",
			"projects.*",
			"providers.*",
			"mcp.*",
			"skills.*",
			"hooks.*",
			"activity.view",
			"insights.view",
			"spaces.*",
		},
	},
	{
		Name:        "PM",
		Description: "Project management. Can create and manage projects, view members and activity.",
		Permissions: []string{
			"org.view",
			"members.view",
			"roles.view",
			"departments.view",
			"projects.*",
			"providers.view",
			"activity.view",
			"insights.view",
			"spaces.project.access",
			"spaces.brainstorm.access",
		},
	},
	{
		Name:        "Member",
		Description: "Standard member with basic read access.",
		Permissions: []string{
			"org.view",
			"members.view",
			"departments.view",
			"projects.view",
			"providers.view",
			"activity.view",
			"spaces.brainstorm.access",
		},
	},
}

// DeveloperRoleDef is seeded into an org's role catalog only when the org
// enrolls as a publisher (see infra/developer). Removed when the org
// un-enrolls. Not included in BuiltinRoles so new orgs don't get it by default.
var DeveloperRoleDef = BuiltinRoleDef{
	Name:        "Developer",
	Description: "Build, publish, and transfer spaces under the org publisher. Only available while the org is enrolled as a developer.",
	Permissions: []string{
		"org.view",
		"members.view",
		"projects.view",
		"spaces.architect.access",
		"spaces.coder.access",
		"spaces.editor.access",
		"spaces.deployment.access",
		"spaces.development.access",
		"developer.publish",
		"developer.transfer",
		"developer.cli",
	},
}

// MatchPermission checks if a granted permission matches a required one.
// Supports wildcards: "projects.*" matches "projects.create", "*" matches everything.
func MatchPermission(granted, required string) bool {
	if granted == "*" {
		return true
	}
	if granted == required {
		return true
	}
	// Wildcard: "projects.*" matches "projects.create", "projects.view", etc.
	if prefix, ok := strings.CutSuffix(granted, ".*"); ok {
		if strings.HasPrefix(required, prefix+".") {
			return true
		}
	}
	return false
}

// ExpandPermissions resolves wildcards against AllPermissions to produce explicit IDs.
func ExpandPermissions(perms []string) []string {
	var result []string
	seen := map[string]bool{}

	for _, p := range perms {
		if p == "*" || strings.HasSuffix(p, ".*") {
			for _, def := range AllPermissions {
				if MatchPermission(p, def.ID) && !seen[def.ID] {
					result = append(result, def.ID)
					seen[def.ID] = true
				}
			}
		} else if !seen[p] {
			result = append(result, p)
			seen[p] = true
		}
	}

	return result
}
