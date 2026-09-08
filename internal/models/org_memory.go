package models

import "time"

// OrgMemory is the organization's shared agent memory — one row per org, a
// single markdown document the agent (and members) curate together. Parallel
// to the per-user / per-project memory the brain keeps locally, but shared
// across all members of the org.
type OrgMemory struct {
	OrgID     string    `json:"org_id" gorm:"primarykey;size:36"`
	Content   string    `json:"content" gorm:"type:text"`
	UpdatedBy string    `json:"updated_by" gorm:"size:36"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (OrgMemory) TableName() string { return "org_memory" }
