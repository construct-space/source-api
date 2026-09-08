package models

import "time"

type VerificationPattern struct {
	AgentType        string    `json:"agent_type"`
	FailureCategory  string    `json:"failure_category"`
	CheckFailed      string    `json:"check_failed"`
	StrikeReached    int       `json:"strike_reached"`
	PlaybookUsed     string    `json:"playbook_used"`
	ResolvedBy       string    `json:"resolved_by"`
	Frequency        int       `json:"frequency"`
	PeriodDays       int       `json:"period_days"`
	InstallationID   string    `json:"installation_id"`
	ConstructVersion string    `json:"construct_version"`
	CreatedAt        time.Time `json:"created_at"`
}
