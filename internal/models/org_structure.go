package models

import "time"

// Department represents an organizational unit.
type Department struct {
	ID          string    `json:"id" gorm:"primarykey;size:36"`
	OrgID       string    `json:"org_id" gorm:"index;size:36;not null"`
	Name        string    `json:"name" gorm:"size:255;not null"`
	Description string    `json:"description" gorm:"type:text"`
	Code        string    `json:"code" gorm:"size:20"`
	HeadID      *string   `json:"head_id" gorm:"size:36"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Department) TableName() string { return "departments" }

// Team belongs to a department.
type Team struct {
	ID           string    `json:"id" gorm:"primarykey;size:36"`
	OrgID        string    `json:"org_id" gorm:"index;size:36;not null"`
	Name         string    `json:"name" gorm:"size:255;not null"`
	Description  string    `json:"description" gorm:"type:text"`
	DepartmentID *string   `json:"department_id" gorm:"size:36"`
	LeadID       *string   `json:"lead_id" gorm:"size:36"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (Team) TableName() string { return "teams" }

// TeamMember links a member to a team.
type TeamMember struct {
	ID       uint      `json:"id" gorm:"primarykey"`
	TeamID   string    `json:"team_id" gorm:"index;size:36;not null"`
	MemberID string    `json:"member_id" gorm:"index;size:36;not null"`
	JoinedAt time.Time `json:"joined_at"`
}

func (TeamMember) TableName() string { return "team_members" }

// OrgInvite is a pending invitation to join an org.
type OrgInvite struct {
	ID           string    `json:"id" gorm:"primarykey;size:36"`
	OrgID        string    `json:"org_id" gorm:"index;size:36;not null"`
	Email        string    `json:"email" gorm:"size:255;not null"`
	Role         string    `json:"role" gorm:"size:50;not null;default:member"`
	DepartmentID *string   `json:"department_id" gorm:"size:36"`
	InvitedBy    string    `json:"invited_by" gorm:"size:36;not null"`
	Token        string    `json:"token" gorm:"size:36;uniqueIndex;not null"`
	Code         string    `json:"code" gorm:"size:6;uniqueIndex"`
	Status       string    `json:"status" gorm:"size:20;not null;default:pending"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (OrgInvite) TableName() string { return "org_invites" }
