package models

import "time"

// ─── Scheduler ────────────────────────────────────────────────────────────────
//
// Universal cron primitive for the Construct host. Used by:
//   - pulses-space (recurring user-created tasks)
//   - calendar (event reminders)
//   - mail (snooze, send-later)
//   - system (update check, log rotation, token refresh)
//
// One table, one tick loop, one place to look. Designed in
// docs/plans/2026-05-20-automations.md (construct-app repo).

// ScheduledTask is a single owned schedule. The runner ticks across rows
// where enabled = true and next_run_at <= now(), claims one via the
// SKIP-LOCKED-style pattern in scheduled_claims, and pushes a wake to the
// best matching online device.
//
// The `Schedule` and `Action` fields are JSON blobs the owning space
// interprets; this service doesn't introspect them.
type ScheduledTask struct {
	ID string `json:"id" gorm:"primarykey;size:36"`

	// Owner identity — generic over user / org / system so org pulses and
	// future shared automations land without schema migration.
	OwnerKind     string `json:"owner_kind" gorm:"size:16;index:idx_sched_owner;not null"`        // 'user' | 'org' | 'system'
	OwnerID       string `json:"owner_id" gorm:"size:36;index:idx_sched_owner;not null"`           // user uuid / org uuid / 'system'
	OwnerSpace    string `json:"owner_space" gorm:"size:64;index;not null"`                        // 'pulses' | 'calendar' | 'mail' | 'system'
	OwnerEntityID string `json:"owner_entity_id,omitempty" gorm:"size:128;index"`                  // optional FK into the owning space (pulse id, event id, …)

	Title    string `json:"title" gorm:"size:255;not null"`
	Schedule string `json:"-" gorm:"type:text;not null"`                                          // serialized Schedule JSON; exposed via ScheduleJSON below
	Action   string `json:"-" gorm:"type:text;not null"`                                          // serialized Action JSON

	Enabled    bool       `json:"enabled" gorm:"not null;default:true;index"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty" gorm:"index:idx_sched_due"`                 // computed when enabled flips on / after each fire
	State      string     `json:"-" gorm:"type:text"`                                                // serialized PulseState-shaped blob; opaque to source

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ScheduledTask) TableName() string { return "scheduled_tasks" }

// ScheduledClaim atomically reserves a single (task, scheduled_for) pair
// for a specific device. Multiple devices may race; the unique PK ensures
// exactly one wins. Expired claims are reclaimed on the next tick.
type ScheduledClaim struct {
	TaskID          string    `json:"task_id" gorm:"primarykey;size:36"`
	ScheduledFor    time.Time `json:"scheduled_for" gorm:"primarykey"`
	ClaimedByDevice string    `json:"claimed_by_device" gorm:"size:64;not null"`
	ClaimedAt       time.Time `json:"claimed_at" gorm:"not null"`
	ExpiresAt       time.Time `json:"expires_at" gorm:"index;not null"`
}

func (ScheduledClaim) TableName() string { return "scheduled_claims" }

// SchedulerDevice is a Construct install (desktop/mobile/org-node) that
// participates in the scheduler. Registered on app boot, heartbeats every
// 60s, claims due tasks for the scopes its user belongs to.
//
// `Scopes` is a JSON array of {kind, id} pairs — same user may participate
// in personal + N orgs from one device.
type SchedulerDevice struct {
	ID           string    `json:"id" gorm:"primarykey;size:64"`
	UserID       string    `json:"user_id" gorm:"size:36;index;not null"`
	Scopes       string    `json:"-" gorm:"type:text;not null"`                                  // serialized []ScopeRef
	Platform     string    `json:"platform" gorm:"size:32;not null"`                              // 'macos' | 'linux' | 'windows' | 'ios' | 'android'
	PushToken    string    `json:"push_token,omitempty" gorm:"size:512"`
	Capabilities string    `json:"-" gorm:"type:text"`                                            // serialized capability map (has_lm_studio, …)
	LastSeenAt   time.Time `json:"last_seen_at" gorm:"index;not null"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (SchedulerDevice) TableName() string { return "scheduler_devices" }

// ScopeRef is the in-memory shape of an entry in SchedulerDevice.Scopes.
// Persisted via JSON inside the Scopes text column.
type ScopeRef struct {
	Kind string `json:"kind"` // 'user' | 'org'
	ID   string `json:"id"`
}
