package models

import "time"

// OrgActivity logs actions within an organization.
type OrgActivity struct {
	ID           string    `json:"id" gorm:"primarykey;size:36"`
	OrgID        string    `json:"org_id" gorm:"index;size:36;not null"`
	Action       string    `json:"action" gorm:"size:50;not null"`
	ResourceType string    `json:"resource_type" gorm:"size:50;not null"`
	ResourceID   string    `json:"resource_id" gorm:"size:36;not null"`
	ResourceName string    `json:"resource_name" gorm:"size:255"`
	PerformedBy  string    `json:"performed_by" gorm:"size:36;not null"`
	Changes      string    `json:"changes" gorm:"type:text"`
	CreatedAt    time.Time `json:"created_at"`
}

func (OrgActivity) TableName() string { return "org_activities" }

// OrgProviderKey stores an org-level API key for an AI provider.
//
// Enforced: when true, this org key overrides any personal key or env
// var on the member's machine — admins use this to guarantee all usage
// bills to the org account. When false, the key is a fallback that only
// kicks in when no personal/env key is configured.
type OrgProviderKey struct {
	ID        string    `json:"id" gorm:"primarykey;size:36"`
	OrgID     string    `json:"org_id" gorm:"uniqueIndex:idx_org_provider;size:36;not null"`
	Provider  string    `json:"provider" gorm:"uniqueIndex:idx_org_provider;size:50;not null"`
	APIKey    string    `json:"-" gorm:"size:500;not null"`
	Enforced  bool      `json:"enforced" gorm:"not null;default:false"`
	SetBy     string    `json:"set_by" gorm:"size:36;not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (OrgProviderKey) TableName() string { return "org_provider_keys" }

// OrgSetting stores org-level configuration.
type OrgSetting struct {
	ID    string `json:"id" gorm:"primarykey;size:36"`
	OrgID string `json:"org_id" gorm:"uniqueIndex:idx_org_setting;size:36;not null"`
	Key   string `json:"key" gorm:"uniqueIndex:idx_org_setting;size:100;not null"`
	Value string `json:"value" gorm:"type:text"`
}

func (OrgSetting) TableName() string { return "org_settings" }

// OrgProject is a shared project visible to org members.
type OrgProject struct {
	ID            string    `json:"id" gorm:"primarykey;size:36"`
	OrgID         string    `json:"org_id" gorm:"uniqueIndex:idx_org_project_name;size:36;not null"`
	Name          string    `json:"name" gorm:"uniqueIndex:idx_org_project_name;size:255;not null"`
	Description   string    `json:"description" gorm:"type:text"`
	RepoURL       string    `json:"repo_url" gorm:"size:500"`
	DefaultBranch string    `json:"default_branch" gorm:"size:100;default:main"`
	Framework     string    `json:"framework" gorm:"size:50"`
	// Visibility controls who can see the project within the org. Values:
	// "private" (creator + added members), "internal" (any org member),
	// "public" (anyone with the link, once that surface exists). Default
	// comes from the org's projects.default_visibility preference.
	Visibility    string    `json:"visibility" gorm:"size:20;default:private"`
	CreatedBy     string    `json:"created_by" gorm:"size:36;not null"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (OrgProject) TableName() string { return "org_projects" }

// OrgProjectMember links a member to an org project.
type OrgProjectMember struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	ProjectID string    `json:"project_id" gorm:"uniqueIndex:idx_project_member;size:36;not null"`
	MemberID  string    `json:"member_id" gorm:"uniqueIndex:idx_project_member;size:36;not null"`
	AssignedAt time.Time `json:"assigned_at"`
}

func (OrgProjectMember) TableName() string { return "org_project_members" }

// OrgProjectRepo links a repository to an org project.
type OrgProjectRepo struct {
	ID            string    `json:"id" gorm:"primarykey;size:36"`
	ProjectID     string    `json:"project_id" gorm:"index;size:36;not null"`
	Name          string    `json:"name" gorm:"size:100;not null"`
	RepoURL       string    `json:"repo_url" gorm:"size:500;not null"`
	DefaultBranch string    `json:"default_branch" gorm:"size:100;default:main"`
	CreatedAt     time.Time `json:"created_at"`
}

func (OrgProjectRepo) TableName() string { return "org_project_repos" }

// OrgSpace pins a marketplace space to an organization. Members of
// the org get the pinned spaces auto-installed at login. Always
// tracks the latest published version — there is no version pinning
// in v1. Removing a pin doesn't uninstall the space from members'
// machines; it just stops the auto-sync.
type OrgSpace struct {
	ID        string    `json:"id" gorm:"primarykey;size:36"`
	OrgID     string    `json:"org_id" gorm:"uniqueIndex:idx_org_space;size:36;not null"`
	SpaceID   string    `json:"space_id" gorm:"uniqueIndex:idx_org_space;size:100;not null"`
	PinnedBy  string    `json:"pinned_by" gorm:"size:36;not null"`
	CreatedAt time.Time `json:"created_at"`
}

func (OrgSpace) TableName() string { return "org_spaces" }
