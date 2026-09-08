package models

import "time"

// Organization represents a team/company workspace.
type Organization struct {
	ID              string    `json:"id" gorm:"primarykey;size:36"`
	Name            string    `json:"name" gorm:"size:255;not null"`
	Slug            string    `json:"slug" gorm:"size:255;uniqueIndex;not null"`
	Icon            string    `json:"icon" gorm:"size:255"`
	OwnerID         string    `json:"owner_id" gorm:"size:36;index;not null"`
	DeveloperStatus string    `json:"developer_status" gorm:"size:20;default:none"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (Organization) TableName() string { return "organizations" }

// OrgMember links a user to an organization with a role.
type OrgMember struct {
	ID           string    `json:"id" gorm:"primarykey;size:36"`
	OrgID        string    `json:"org_id" gorm:"index;size:36;not null"`
	UserID       string    `json:"user_id" gorm:"size:36;index"`
	Name         string    `json:"name" gorm:"size:255;not null"`
	Email        string    `json:"email" gorm:"size:255;not null"`
	Avatar       string    `json:"avatar" gorm:"size:500"`
	Title        string    `json:"title" gorm:"size:255"`
	Phone        string    `json:"phone" gorm:"size:50"`
	Bio          string    `json:"bio" gorm:"type:text"`
	Role         string    `json:"role" gorm:"size:50;not null;default:member"`
	RoleID       *string   `json:"role_id" gorm:"size:36"`
	Status       string    `json:"status" gorm:"size:20;not null;default:active"`
	DepartmentID *string   `json:"department_id" gorm:"size:36"`
	JoinedAt     time.Time `json:"joined_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (OrgMember) TableName() string { return "org_members" }
