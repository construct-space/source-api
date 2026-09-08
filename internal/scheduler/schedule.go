package scheduler

// Schedule semantics — turn a Schedule JSON blob into the next firing
// time. Server-authoritative; devices never decide what "due" means.
//
// Shape mirrors construct-app/docs/plans/2026-05-20-automations.md.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Schedule is the canonical shape stored in ScheduledTask.Schedule (JSON).
type Schedule struct {
	Kind         string     `json:"kind"`                     // 'interval' | 'daily' | 'weekly' | 'once'
	EveryMinutes int        `json:"every_minutes,omitempty"`  // for 'interval'
	Time         string     `json:"time,omitempty"`           // 'HH:mm' for 'daily' / 'weekly'
	Weekdays     []int      `json:"weekdays,omitempty"`       // 0=Sun … 6=Sat for 'weekly'
	At           *time.Time `json:"at,omitempty"`             // for 'once' — ISO8601 instant
	Timezone     string     `json:"timezone,omitempty"`       // IANA, e.g. 'Europe/Tirane'; empty = UTC
}

// ParseSchedule decodes a JSON blob and validates the shape. Used at task
// create/update time so bad schedules are rejected at the API boundary,
// not at first tick.
func ParseSchedule(raw json.RawMessage) (Schedule, error) {
	var s Schedule
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("invalid JSON: %w", err)
	}
	switch s.Kind {
	case "interval":
		if s.EveryMinutes <= 0 {
			return s, errors.New("interval requires every_minutes > 0")
		}
		// Cap at 1 year so a typo doesn't park a task forever.
		if s.EveryMinutes > 525_600 {
			return s, errors.New("every_minutes too large")
		}
	case "daily":
		if _, _, err := parseHHMM(s.Time); err != nil {
			return s, fmt.Errorf("daily: %w", err)
		}
	case "weekly":
		if _, _, err := parseHHMM(s.Time); err != nil {
			return s, fmt.Errorf("weekly: %w", err)
		}
		if len(s.Weekdays) == 0 {
			return s, errors.New("weekly requires at least one weekday")
		}
		seen := map[int]bool{}
		for _, d := range s.Weekdays {
			if d < 0 || d > 6 {
				return s, errors.New("weekday must be 0–6 (Sun..Sat)")
			}
			if seen[d] {
				return s, errors.New("duplicate weekday")
			}
			seen[d] = true
		}
	case "once":
		if s.At == nil {
			return s, errors.New("once requires at (ISO8601)")
		}
	default:
		return s, fmt.Errorf("unknown schedule kind: %q", s.Kind)
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return s, fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
		}
	}
	return s, nil
}

// ComputeNextRun returns the next firing time after `now`. For interval
// schedules `lastFiredAt` is used as the base when non-zero; for daily /
// weekly the wall-clock time-of-day in the schedule's timezone governs.
//
// Returns nil when no future run exists (e.g. a 'once' schedule that's
// already past). Caller should null out NextRunAt and disable the task.
func ComputeNextRun(s Schedule, lastFiredAt time.Time, now time.Time) *time.Time {
	loc := loadTZ(s.Timezone)
	switch s.Kind {
	case "interval":
		// Most-recent-only policy (see automations plan, notification
		// policy → wake / missed-run replay): when waking after a long
		// gap we always advance from `now`, not from the long-stale
		// lastFiredAt. This collapses missed ticks instead of firing
		// catch-up runs.
		base := lastFiredAt
		if base.IsZero() || now.Sub(base) > time.Duration(s.EveryMinutes)*time.Minute*2 {
			base = now
		}
		next := base.Add(time.Duration(s.EveryMinutes) * time.Minute).UTC()
		return &next
	case "daily":
		h, m, _ := parseHHMM(s.Time)
		return nextDailyAt(now.In(loc), h, m)
	case "weekly":
		h, m, _ := parseHHMM(s.Time)
		return nextWeeklyAt(now.In(loc), s.Weekdays, h, m)
	case "once":
		if s.At == nil {
			return nil
		}
		if !lastFiredAt.IsZero() {
			return nil // one-shot already fired
		}
		t := s.At.UTC()
		if !t.After(now) {
			// 'at' is in the past at create time — caller decides
			// whether to fire it immediately (we still return it so
			// the next /due poll picks it up).
		}
		return &t
	}
	return nil
}

// ─── internals ──────────────────────────────────────────────────────────

func loadTZ(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

func parseHHMM(s string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, 0, errors.New("time must be HH:mm")
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, errors.New("time out of range")
	}
	return h, m, nil
}

func nextDailyAt(now time.Time, h, m int) *time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if today.After(now) {
		t := today.UTC()
		return &t
	}
	t := today.AddDate(0, 0, 1).UTC()
	return &t
}

func nextWeeklyAt(now time.Time, weekdays []int, h, m int) *time.Time {
	for offset := 0; offset < 8; offset++ {
		candidate := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location()).
			AddDate(0, 0, offset)
		wd := int(candidate.Weekday())
		if !containsInt(weekdays, wd) {
			continue
		}
		if candidate.After(now) {
			t := candidate.UTC()
			return &t
		}
	}
	return nil // unreachable given weekday validation
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
