// Package devicebus is an in-process pub/sub keyed by user_id, fronting
// the device-bus WebSocket. Phone publishes "assistant.ask", subscribed
// desktop receives it and answers via "assistant.chunk" published back
// through the same hub. Source's scheduler also publishes here
// ("scheduler.claim_now") when a task becomes due.
//
// Single-container only — if source ever scales horizontally, swap
// Publish to also broadcast over Postgres LISTEN/NOTIFY. The public
// API of this package would not change.
//
// Patterned on api/delivery/internal/sse/hub.go. The relay shape lives
// in source now so the device-bus and its main publisher (the
// scheduler) share a process; delivery keeps its actual job (email
// + push notifications) and the relay traffic migrates over in a
// follow-up rollout (slice 4).
package devicebus

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Envelope is the wire shape exchanged on the bus. Type identifies the
// flavor of event; Payload is opaque per-type JSON. From is optional
// metadata the originating subscriber may set (e.g. "mobile" / "desktop")
// so consumers can render origin in the UI.
type Envelope struct {
	Type    string          `json:"type"` // 'assistant.ask' | 'scheduler.claim_now' | …
	From    string          `json:"from,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type subscriber struct {
	id         string // monotonic per-process ID, exposed as connID
	ch         chan Envelope
	isOperator bool // set after a {type:"register",kind:"operator"} hello
}

type Hub struct {
	mu     sync.RWMutex
	subs   map[string]map[*subscriber]struct{}
	nextID uint64
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscriber]struct{})}
}

// Subscribe registers a new connection for userID. Returns the
// connection's id (used as exceptConnID in PublishExcept so a sender
// doesn't echo back to itself), a buffered event channel, and an
// unsubscribe func. Channel buffer is 16; if it fills, events are
// dropped for that one subscriber — caller should reconnect to
// resync.
func (h *Hub) Subscribe(userID string) (string, <-chan Envelope, func()) {
	h.mu.Lock()
	h.nextID++
	s := &subscriber{
		id: fmt.Sprintf("c%d", h.nextID),
		ch: make(chan Envelope, 16),
	}
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[*subscriber]struct{})
	}
	h.subs[userID][s] = struct{}{}
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		if m, ok := h.subs[userID]; ok {
			delete(m, s)
			if len(m) == 0 {
				delete(h.subs, userID)
			}
		}
		h.mu.Unlock()
		close(s.ch)
	}
	return s.id, s.ch, unsub
}

// Publish fans an envelope to every subscriber of userID.
func (h *Hub) Publish(userID string, env Envelope) {
	h.PublishExcept(userID, env, "")
}

// PublishExcept fans an envelope to every subscriber of userID *except*
// the one matching exceptConnID. Used when the publisher is also a
// subscriber (e.g. desktop publishes assistant.chunk back through
// the bus while keeping its own WS open) and we don't want a
// self-echo.
func (h *Hub) PublishExcept(userID string, env Envelope, exceptConnID string) {
	h.mu.RLock()
	subs := h.subs[userID]
	targets := make([]*subscriber, 0, len(subs))
	for s := range subs {
		if exceptConnID != "" && s.id == exceptConnID {
			continue
		}
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case s.ch <- env:
		default:
			// Buffer full — drop. Slow client will resync on reconnect.
		}
	}
}

// MarkAsOperator tags an existing subscription as the user's operator
// (i.e. the desktop instance that runs brain). The WS handler calls
// this after the client sends {type:"register", kind:"operator"} in
// its hello frame. Used to gate assistant.ask publishes — if no
// operator is online the relay returns 409 rather than silently
// fanning to nothing.
func (h *Hub) MarkAsOperator(userID, connID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[userID] {
		if s.id == connID {
			s.isOperator = true
			return true
		}
	}
	return false
}

// HasOperator reports whether userID has at least one subscription
// that has identified itself as the operator.
func (h *Hub) HasOperator(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[userID] {
		if s.isOperator {
			return true
		}
	}
	return false
}

// IsOperator reports whether connID is one of userID's registered operator
// subscriptions. Relay handlers use this to prevent another signed-in client
// from spoofing desktop-only assistant output events.
func (h *Hub) IsOperator(userID, connID string) bool {
	if connID == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[userID] {
		if s.id == connID {
			return s.isOperator
		}
	}
	return false
}
