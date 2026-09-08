package handlers

// Scheduler — universal cron primitive. Slice 1: CRUD + device registration.
// Tick loop, claim, push wake land in subsequent slices.
//
// Design: construct-app/docs/plans/2026-05-20-automations.md

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
	"construct/source/internal/scheduler"

	"gorm.io/gorm"
)

// ─── Shapes the API speaks (vs the DB models that store JSON as text) ──────

type schedulerScopeRef = models.ScopeRef

type schedulerTaskPayload struct {
	OwnerKind     string          `json:"owner_kind,omitempty"`     // defaults to 'user' = caller
	OwnerID       string          `json:"owner_id,omitempty"`       // defaults to caller's UserID
	OwnerSpace    string          `json:"owner_space"`              // required
	OwnerEntityID string          `json:"owner_entity_id,omitempty"`
	Title         string          `json:"title"`                    // required
	Schedule      json.RawMessage `json:"schedule"`                 // required
	Action        json.RawMessage `json:"action"`                   // required
	Enabled       *bool           `json:"enabled,omitempty"`        // defaults true
	NextRunAt     *time.Time      `json:"next_run_at,omitempty"`    // overrideable; otherwise derived from schedule
}

type schedulerTaskOut struct {
	ID            string          `json:"id"`
	OwnerKind     string          `json:"owner_kind"`
	OwnerID       string          `json:"owner_id"`
	OwnerSpace    string          `json:"owner_space"`
	OwnerEntityID string          `json:"owner_entity_id,omitempty"`
	Title         string          `json:"title"`
	Schedule      json.RawMessage `json:"schedule"`
	Action        json.RawMessage `json:"action"`
	Enabled       bool            `json:"enabled"`
	NextRunAt     *time.Time      `json:"next_run_at,omitempty"`
	State         json.RawMessage `json:"state,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type schedulerDevicePayload struct {
	DeviceID     string              `json:"device_id"`        // required; stable per install
	Platform     string              `json:"platform"`         // required
	PushToken    string              `json:"push_token,omitempty"`
	Scopes       []schedulerScopeRef `json:"scopes,omitempty"` // defaults to [{user, caller}]
	Capabilities map[string]any      `json:"capabilities,omitempty"`
}

type schedulerDeviceOut struct {
	ID           string              `json:"id"`
	UserID       string              `json:"user_id"`
	Platform     string              `json:"platform"`
	PushToken    string              `json:"push_token,omitempty"`
	Scopes       []schedulerScopeRef `json:"scopes"`
	Capabilities map[string]any      `json:"capabilities,omitempty"`
	LastSeenAt   time.Time           `json:"last_seen_at"`
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func newSchedulerID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:8]) + "-" + hex.EncodeToString(b[8:12]) + "-" + hex.EncodeToString(b[12:14]) + "-" + hex.EncodeToString(b[14:16])
}

func taskToOut(t *models.ScheduledTask) schedulerTaskOut {
	out := schedulerTaskOut{
		ID:            t.ID,
		OwnerKind:     t.OwnerKind,
		OwnerID:       t.OwnerID,
		OwnerSpace:    t.OwnerSpace,
		OwnerEntityID: t.OwnerEntityID,
		Title:         t.Title,
		Enabled:       t.Enabled,
		NextRunAt:     t.NextRunAt,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}
	if t.Schedule != "" {
		out.Schedule = json.RawMessage(t.Schedule)
	}
	if t.Action != "" {
		out.Action = json.RawMessage(t.Action)
	}
	if t.State != "" {
		out.State = json.RawMessage(t.State)
	}
	return out
}

func deviceToOut(d *models.SchedulerDevice) schedulerDeviceOut {
	out := schedulerDeviceOut{
		ID:         d.ID,
		UserID:     d.UserID,
		Platform:   d.Platform,
		PushToken:  d.PushToken,
		LastSeenAt: d.LastSeenAt,
	}
	if d.Scopes != "" {
		_ = json.Unmarshal([]byte(d.Scopes), &out.Scopes)
	}
	if d.Capabilities != "" {
		_ = json.Unmarshal([]byte(d.Capabilities), &out.Capabilities)
	}
	return out
}

// canActAs checks whether the caller may create/edit tasks owned by
// `ownerKind`/`ownerID`. Slice 1 rule: user scope only the caller, system
// is rejected entirely (bootstrapped server-side), org is deferred until
// the org-pulses feature lands (Level 1+).
func canActAs(callerUserID, ownerKind, ownerID string) error {
	switch ownerKind {
	case "user":
		if ownerID != callerUserID {
			return errors.New("cannot create tasks owned by another user")
		}
		return nil
	case "org":
		// TODO(org-pulses): check caller is a member of ownerID with the
		// appropriate role. Deferred per the automations plan; v1 ships
		// user-only authority.
		return errors.New("org-owned tasks not yet supported via this endpoint")
	case "system":
		return errors.New("system-owned tasks are bootstrapped server-side and cannot be created via API")
	default:
		return errors.New("unknown owner_kind")
	}
}

// taskIDFromPath extracts the {id} segment from /api/scheduler/tasks/{id}/...
func taskIDFromPath(r *http.Request) string { return r.PathValue("id") }

// deviceIDFromPath extracts the {id} segment from /api/scheduler/devices/{id}/...
func deviceIDFromPath(r *http.Request) string { return r.PathValue("id") }

// ─── Task CRUD ─────────────────────────────────────────────────────────────

// CreateScheduledTask — POST /api/scheduler/tasks
func CreateScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var p schedulerTaskPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(p.OwnerSpace) == "" || strings.TrimSpace(p.Title) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "owner_space and title are required"})
		return
	}
	if len(p.Schedule) == 0 || len(p.Action) == 0 {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "schedule and action are required"})
		return
	}

	ownerKind := p.OwnerKind
	if ownerKind == "" {
		ownerKind = "user"
	}
	ownerID := p.OwnerID
	if ownerID == "" {
		ownerID = userID
	}
	if err := canActAs(userID, ownerKind, ownerID); err != nil {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	parsed, err := scheduler.ParseSchedule(p.Schedule)
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "schedule: " + err.Error()})
		return
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}

	// Server-authoritative next-run: caller can override via NextRunAt
	// (used by "run on next tick" creates) but the default is computed
	// from the schedule.
	nextRun := p.NextRunAt
	if nextRun == nil && enabled {
		nextRun = scheduler.ComputeNextRun(parsed, time.Time{}, time.Now().UTC())
	}

	t := models.ScheduledTask{
		ID:            newSchedulerID(),
		OwnerKind:     ownerKind,
		OwnerID:       ownerID,
		OwnerSpace:    p.OwnerSpace,
		OwnerEntityID: p.OwnerEntityID,
		Title:         p.Title,
		Schedule:      string(p.Schedule),
		Action:        string(p.Action),
		Enabled:       enabled,
		NextRunAt:     nextRun,
	}
	if err := database.DB.Create(&t).Error; err != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create task"})
		return
	}
	WriteJSON(w, http.StatusCreated, taskToOut(&t))
}

// ListScheduledTasks — GET /api/scheduler/tasks
//
// Query params:
//   owner_space   filter by owner space ('pulses', 'calendar', …)
//   enabled       'true' | 'false'
//   owner_entity_id  cross-reference (e.g. all reminders for one event)
//
// Always scoped to the caller's user_id (org filtering deferred).
func ListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	q := database.DB.Where("owner_kind = ? AND owner_id = ?", "user", userID)
	if v := r.URL.Query().Get("owner_space"); v != "" {
		q = q.Where("owner_space = ?", v)
	}
	if v := r.URL.Query().Get("owner_entity_id"); v != "" {
		q = q.Where("owner_entity_id = ?", v)
	}
	switch r.URL.Query().Get("enabled") {
	case "true":
		q = q.Where("enabled = ?", true)
	case "false":
		q = q.Where("enabled = ?", false)
	}
	// due=true returns only tasks that are ready to fire and aren't
	// already claimed by another device. Clients poll this on a
	// 30–60s cadence; slice 3 (WS push) will let them skip polling
	// while the connection is live.
	if r.URL.Query().Get("due") == "true" {
		now := time.Now().UTC()
		q = q.Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
			Where("NOT EXISTS (SELECT 1 FROM scheduled_claims c WHERE c.task_id = scheduled_tasks.id AND c.expires_at > ?)", now)
	}

	var tasks []models.ScheduledTask
	if err := q.Order("created_at DESC").Find(&tasks).Error; err != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}

	out := make([]schedulerTaskOut, 0, len(tasks))
	for i := range tasks {
		out = append(out, taskToOut(&tasks[i]))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// GetScheduledTask — GET /api/scheduler/tasks/{id}
func GetScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var t models.ScheduledTask
	if err := database.DB.First(&t, "id = ?", taskIDFromPath(r)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if t.OwnerKind != "user" || t.OwnerID != userID {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not yours"})
		return
	}
	WriteJSON(w, http.StatusOK, taskToOut(&t))
}

// UpdateScheduledTask — PATCH /api/scheduler/tasks/{id}
//
// Only the owner can edit. owner_kind / owner_id / owner_space cannot be
// changed after creation (would orphan claims and confuse spaces).
func UpdateScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var t models.ScheduledTask
	if err := database.DB.First(&t, "id = ?", taskIDFromPath(r)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if t.OwnerKind != "user" || t.OwnerID != userID {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not yours"})
		return
	}

	var p schedulerTaskPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	updates := map[string]any{}
	if p.Title != "" {
		updates["title"] = p.Title
	}
	if len(p.Schedule) > 0 {
		parsed, err := scheduler.ParseSchedule(p.Schedule)
		if err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "schedule: " + err.Error()})
			return
		}
		updates["schedule"] = string(p.Schedule)
		// Schedule changed → recompute next_run_at from scratch.
		updates["next_run_at"] = scheduler.ComputeNextRun(parsed, time.Time{}, time.Now().UTC())
	}
	if len(p.Action) > 0 {
		updates["action"] = string(p.Action)
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
		// Re-enabling a previously-disabled task with no next_run_at?
		// Compute one so the next /due poll picks it up.
		if *p.Enabled && t.NextRunAt == nil {
			parsed, perr := scheduler.ParseSchedule(json.RawMessage(t.Schedule))
			if perr == nil {
				updates["next_run_at"] = scheduler.ComputeNextRun(parsed, time.Time{}, time.Now().UTC())
			}
		}
	}
	if p.NextRunAt != nil {
		updates["next_run_at"] = *p.NextRunAt
	}

	if len(updates) > 0 {
		if err := database.DB.Model(&t).Updates(updates).Error; err != nil {
			WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
		_ = database.DB.First(&t, "id = ?", t.ID).Error
	}
	WriteJSON(w, http.StatusOK, taskToOut(&t))
}

// DeleteScheduledTask — DELETE /api/scheduler/tasks/{id}
//
// Hard delete; cascades to claims via a follow-up Delete (no FK so we do it
// in app code). Owning spaces use this to clean up on entity delete
// (e.g. Calendar deletes an event → its reminder tasks vanish).
func DeleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := taskIDFromPath(r)
	var t models.ScheduledTask
	if err := database.DB.First(&t, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteJSON(w, http.StatusNoContent, nil)
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if t.OwnerKind != "user" || t.OwnerID != userID {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not yours"})
		return
	}
	if err := database.DB.Delete(&t).Error; err != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	_ = database.DB.Where("task_id = ?", id).Delete(&models.ScheduledClaim{}).Error
	w.WriteHeader(http.StatusNoContent)
}

// ─── Device registration ───────────────────────────────────────────────────

// RegisterSchedulerDevice — POST /api/scheduler/devices/register
//
// Idempotent on device_id. Updates last_seen_at, scopes, push_token,
// capabilities on every call so apps can call it on each boot.
func RegisterSchedulerDevice(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var p schedulerDevicePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(p.DeviceID) == "" || strings.TrimSpace(p.Platform) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id and platform are required"})
		return
	}

	// Default scope = the caller acting as themselves. Org scopes can be
	// added later when the device pairs with an org.
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = []schedulerScopeRef{{Kind: "user", ID: userID}}
	}
	// Validate that any user-kind scope refers to the caller; defer org
	// scope validation until org-pulses lands (membership check needed).
	for _, s := range scopes {
		if s.Kind == "user" && s.ID != userID {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cannot register device for another user's scope"})
			return
		}
	}

	scopesJSON, _ := json.Marshal(scopes)
	capsJSON := ""
	if p.Capabilities != nil {
		if raw, err := json.Marshal(p.Capabilities); err == nil {
			capsJSON = string(raw)
		}
	}

	now := time.Now().UTC()
	d := models.SchedulerDevice{
		ID:           p.DeviceID,
		UserID:       userID,
		Scopes:       string(scopesJSON),
		Platform:     p.Platform,
		PushToken:    p.PushToken,
		Capabilities: capsJSON,
		LastSeenAt:   now,
	}

	// Upsert: insert or update on conflict.
	var existing models.SchedulerDevice
	if err := database.DB.First(&existing, "id = ?", d.ID).Error; err == nil {
		// Existing record — refresh fields, preserve created_at.
		updates := map[string]any{
			"user_id":      userID,
			"scopes":       d.Scopes,
			"platform":     d.Platform,
			"push_token":   d.PushToken,
			"capabilities": d.Capabilities,
			"last_seen_at": now,
		}
		if err := database.DB.Model(&existing).Updates(updates).Error; err != nil {
			WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
		_ = database.DB.First(&existing, "id = ?", d.ID).Error
		WriteJSON(w, http.StatusOK, deviceToOut(&existing))
		return
	}
	if err := database.DB.Create(&d).Error; err != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "create failed"})
		return
	}
	WriteJSON(w, http.StatusCreated, deviceToOut(&d))
}

// HeartbeatSchedulerDevice — POST /api/scheduler/devices/{id}/heartbeat
//
// Cheap call clients fire every 60s while open. Updates last_seen_at only.
// Server uses last_seen_at + 90s grace window to decide if a device is
// "online" for routing decisions (future slice).
func HeartbeatSchedulerDevice(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := deviceIDFromPath(r)
	res := database.DB.Model(&models.SchedulerDevice{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("last_seen_at", time.Now().UTC())
	if res.Error != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	if res.RowsAffected == 0 {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "device not registered"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Claim + Report ────────────────────────────────────────────────────────

type claimPayload struct {
	DeviceID     string    `json:"device_id"`              // required
	ScheduledFor time.Time `json:"scheduled_for,omitempty"` // optional — defaults to task.next_run_at
}

type claimOut struct {
	TaskID       string    `json:"task_id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ClaimScheduledTask — POST /api/scheduler/tasks/{id}/claim
//
// Atomic claim. Returns 409 if another device beat this one (or if the
// previous claim hasn't expired yet). Successful claim is valid for
// scheduler.ClaimTTL; the reclaim loop frees it after that.
func ClaimScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var p claimPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(p.DeviceID) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id is required"})
		return
	}

	var t models.ScheduledTask
	if err := database.DB.First(&t, "id = ?", taskIDFromPath(r)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if t.OwnerKind != "user" || t.OwnerID != userID {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not yours"})
		return
	}
	if !t.Enabled {
		WriteJSON(w, http.StatusConflict, map[string]string{"error": "task is disabled"})
		return
	}
	if t.NextRunAt == nil {
		WriteJSON(w, http.StatusConflict, map[string]string{"error": "task has no scheduled run"})
		return
	}

	scheduledFor := p.ScheduledFor
	if scheduledFor.IsZero() {
		scheduledFor = *t.NextRunAt
	}

	now := time.Now().UTC()
	expiresAt := now.Add(scheduler.ClaimTTL)

	// Transactional: clear any expired claim for this slot, then try to
	// place ours. Insert fails (unique violation) if another device
	// already holds a non-expired claim.
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		tx.Where("task_id = ? AND scheduled_for = ? AND expires_at < ?", t.ID, scheduledFor, now).
			Delete(&models.ScheduledClaim{})
		return tx.Create(&models.ScheduledClaim{
			TaskID:          t.ID,
			ScheduledFor:    scheduledFor,
			ClaimedByDevice: p.DeviceID,
			ClaimedAt:       now,
			ExpiresAt:       expiresAt,
		}).Error
	})
	if err != nil {
		WriteJSON(w, http.StatusConflict, map[string]string{"error": "already claimed by another device"})
		return
	}
	WriteJSON(w, http.StatusOK, claimOut{
		TaskID:       t.ID,
		ScheduledFor: scheduledFor,
		ExpiresAt:    expiresAt,
	})
}

type reportPayload struct {
	DeviceID string          `json:"device_id"`         // required
	Outcome  string          `json:"outcome"`           // 'success' | 'quiet' | 'error'
	Error    string          `json:"error,omitempty"`   // when outcome=error
	State    json.RawMessage `json:"state,omitempty"`   // opaque blob the space stores on the task
}

// ReportScheduledTask — POST /api/scheduler/tasks/{id}/report
//
// Device completed execution. Server merges state, computes the next
// firing time, releases the claim. For one-shot schedules whose target
// time has passed the task auto-disables.
//
// Pause-on-failure: after 5 consecutive errors the task is disabled and
// the caller is expected to surface a "needs attention" notification.
func ReportScheduledTask(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var p reportPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(p.DeviceID) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id is required"})
		return
	}
	switch p.Outcome {
	case "success", "quiet", "error":
	default:
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "outcome must be success | quiet | error"})
		return
	}

	var t models.ScheduledTask
	if err := database.DB.First(&t, "id = ?", taskIDFromPath(r)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if t.OwnerKind != "user" || t.OwnerID != userID {
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not yours"})
		return
	}

	// Verify the caller actually holds the active claim. Anyone could
	// otherwise report on someone else's in-flight run.
	var claim models.ScheduledClaim
	if err := database.DB.
		Where("task_id = ? AND claimed_by_device = ? AND expires_at > ?", t.ID, p.DeviceID, time.Now().UTC()).
		First(&claim).Error; err != nil {
		WriteJSON(w, http.StatusConflict, map[string]string{"error": "no active claim for this device"})
		return
	}

	// Merge incoming state, then bump counters from the outcome.
	merged := mergeState(t.State, p.State, p.Outcome, p.Error)

	parsed, _ := scheduler.ParseSchedule(json.RawMessage(t.Schedule))
	now := time.Now().UTC()
	nextRun := scheduler.ComputeNextRun(parsed, now, now)

	updates := map[string]any{
		"state":       merged,
		"next_run_at": nextRun,
	}
	// One-shot schedules with no future run auto-disable.
	if nextRun == nil {
		updates["enabled"] = false
	}
	// Pause-on-failure after 5 consecutive errors.
	if consecutiveErrorsFromState(merged) >= 5 {
		updates["enabled"] = false
	}

	if err := database.DB.Model(&t).Updates(updates).Error; err != nil {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	database.DB.Where("task_id = ? AND scheduled_for = ?", t.ID, claim.ScheduledFor).
		Delete(&models.ScheduledClaim{})
	_ = database.DB.First(&t, "id = ?", t.ID).Error
	WriteJSON(w, http.StatusOK, taskToOut(&t))
}

// mergeState combines the device's incoming state blob with bookkeeping
// fields the server controls (lastRunAt, outcome counters). The blob
// stays opaque otherwise; the owning space defines its own shape.
func mergeState(existing string, incoming json.RawMessage, outcome, errMsg string) string {
	out := map[string]any{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &out)
	}
	if len(incoming) > 0 {
		var in map[string]any
		if err := json.Unmarshal(incoming, &in); err == nil {
			for k, v := range in {
				out[k] = v
			}
		}
	}
	now := time.Now().UTC()
	out["lastRunAt"] = now
	out["lastOutcome"] = outcome
	if outcome == "success" || outcome == "quiet" {
		out["lastSuccessAt"] = now
		out["consecutiveErrors"] = 0
		// Don't drop a prior error message — caller's choice to
		// surface "last error" alongside "last success".
	}
	if outcome == "error" {
		cur, _ := out["consecutiveErrors"].(float64)
		out["consecutiveErrors"] = int(cur) + 1
		if errMsg != "" {
			out["lastError"] = errMsg
		}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

func consecutiveErrorsFromState(state string) int {
	if state == "" {
		return 0
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		return 0
	}
	switch v := s["consecutiveErrors"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
