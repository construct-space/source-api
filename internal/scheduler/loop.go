package scheduler

// Background goroutine that deletes expired claims. Devices that crashed
// or went offline mid-run leave behind a claim that would otherwise
// block other devices from picking up the same task. Reclaiming on a
// minute timer keeps tasks fluid without per-request bookkeeping.

import (
	"context"
	"log"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// ClaimTTL is the lifetime of a successful claim. After this the
// reclaim loop frees the slot for another device. Pulse runs that
// take longer than this (which would be unusual for the
// "summarize + notify" workload) get reclaimed and may double-fire.
// Keep recipe execution well under ClaimTTL.
const ClaimTTL = 60 * time.Second

const reclaimInterval = 60 * time.Second

// StartReclaimLoop runs until ctx is canceled. Idempotent if already
// started (caller's responsibility to call once at boot).
func StartReclaimLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(reclaimInterval)
		defer ticker.Stop()
		// Do one pass immediately so a freshly-booted source doesn't
		// wait 60s before clearing stale claims left over from a crash.
		reclaimExpiredClaims()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reclaimExpiredClaims()
			}
		}
	}()
}

func reclaimExpiredClaims() {
	res := database.DB.Where("expires_at < ?", time.Now().UTC()).
		Delete(&models.ScheduledClaim{})
	if res.Error != nil {
		log.Printf("[scheduler] reclaim failed: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[scheduler] reclaimed %d expired claim(s)", res.RowsAffected)
	}
}
