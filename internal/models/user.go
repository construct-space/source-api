package models

import "time"

// ProviderKey stores a user's API key for an AI provider.
type ProviderKey struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	UserID    string    `json:"user_id" gorm:"size:36;index;not null"`
	Provider  string    `json:"provider" gorm:"size:50;not null"`
	APIKey    string    `json:"-" gorm:"size:500;not null"` // never serialized
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ProviderKey) TableName() string { return "provider_keys" }

// Preference stores a user preference as key/value.
type Preference struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	UserID    string    `json:"user_id" gorm:"size:36;index;not null"`
	Key       string    `json:"key" gorm:"size:255;not null"`
	Value     string    `json:"value" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Preference) TableName() string { return "preferences" }

// OrgPreference stores an org-scoped settings value. Uniqueness is on
// (org_id, key). Writes require the `org.settings.edit` permission;
// reads are scoped to members of the org.
type OrgPreference struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	OrgID     string    `json:"org_id" gorm:"size:36;index:idx_org_pref_org_key,unique;not null"`
	Key       string    `json:"key" gorm:"size:255;index:idx_org_pref_org_key,unique;not null"`
	Value     string    `json:"value" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (OrgPreference) TableName() string { return "org_preferences" }
