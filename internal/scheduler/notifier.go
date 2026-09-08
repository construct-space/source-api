package scheduler

// Notifier — scans for due tasks every few seconds and publishes
// scheduler.claim_now envelopes to the user's subscribed devices via
// the device-bus hub. Devices race to claim via the existing
// /api/scheduler/tasks/:id/claim endpoint (atomic, returns 409 to
// losers). The bus is purely a wake mechanism; correctness rests on
// the claim semantics.
//
// Polling falls back when devices aren't connected to the bus —
// /api/scheduler/tasks?due=true returns the same tasks the notifier
// would push, so a desktop that just woke up catches up without
// needing a wake event.

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/devicebus"
	"construct/source/internal/models"
)

const notifyInterval = 5 * time.Second

// claimNowPayload mirrors what devices need to claim. The bus message
// is purely a hint — devices still call /api/scheduler/tasks?due=true
// when reconnecting, so missing a wake is recoverable.
type claimNowPayload struct {
	TaskID       string    `json:"task_id"`
	OwnerSpace   string    `json:"owner_space"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Action       json.RawMessage `json:"action"`
}

// recentlyNotified deduplicates within a single source process: a task
// that's due now and on the next tick (4s later) should only generate
// one wake event until either the device claims it or 60s passes
// (which roughly matches ClaimTTL — by then the claim has expired
// anyway and re-notifying is desirable).
type recentlyNotified struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (r *recentlyNotified) seen(taskID string, at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.at[taskID]
	if ok && time.Since(last) < 60*time.Second {
		return true
	}
	r.at[taskID] = at
	// Opportunistic GC — clear entries older than 5 min.
	if len(r.at) > 1024 {
		cutoff := time.Now().Add(-5 * time.Minute)
		for id, t := range r.at {
			if t.Before(cutoff) {
				delete(r.at, id)
			}
		}
	}
	return false
}

// StartNotifyLoop scans for due tasks every notifyInterval and publishes
// scheduler.claim_now envelopes via hub. Caller is responsible for
// passing the same hub instance the WS handler uses.
func StartNotifyLoop(ctx context.Context, hub *devicebus.Hub) {
	dedup := &recentlyNotified{at: make(map[string]time.Time)}
	go func() {
		ticker := time.NewTicker(notifyInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				notifyDueTasks(hub, dedup)
			}
		}
	}()
}

func notifyDueTasks(hub *devicebus.Hub, dedup *recentlyNotified) {
	now := time.Now().UTC()

	// Fetch all enabled tasks due now with no live claim. Mirrors the
	// SQL behind ?due=true on the list endpoint; cap the batch so a
	// stampede of overdue tasks doesn't pin the DB.
	var tasks []models.ScheduledTask
	err := database.DB.
		Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Where("NOT EXISTS (SELECT 1 FROM scheduled_claims c WHERE c.task_id = scheduled_tasks.id AND c.expires_at > ?)", now).
		Limit(200).
		Find(&tasks).Error
	if err != nil {
		log.Printf("[scheduler] notify scan failed: %v", err)
		return
	}

	for i := range tasks {
		t := &tasks[i]
		if dedup.seen(t.ID, now) {
			continue
		}
		payload, _ := json.Marshal(claimNowPayload{
			TaskID:       t.ID,
			OwnerSpace:   t.OwnerSpace,
			ScheduledFor: derefTime(t.NextRunAt),
			Action:       json.RawMessage(t.Action),
		})
		hub.Publish(t.OwnerID, devicebus.Envelope{
			Type:    "scheduler.claim_now",
			From:    "source",
			Payload: payload,
		})
	}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
