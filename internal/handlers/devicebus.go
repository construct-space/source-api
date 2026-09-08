package handlers

// Device bus — WebSocket fan-out keyed by user_id. Mobile and desktop
// subscribe via /api/device-bus/ws; either side (or source itself, via
// the scheduler notifier) publishes envelopes via the in-process hub.
//
// Envelope types in play today:
//   assistant.ask          phone → desktop (slice 4 migration target)
//   assistant.chunk        desktop → asker (slice 4 migration target)
//   assistant.complete     desktop → asker (slice 4 migration target)
//   desktop.navigate       phone → desktop companion handoff
//   desktop.open_url       phone → desktop companion handoff
//   desktop.space.run      phone → desktop companion action
//   scheduler.claim_now    source → device (this slice)
//   scheduler.task_updated source → all devices (this slice)
//
// Slice 3 builds the bus + scheduler events. Slice 4 migrates the
// assistant.* relay traffic off api/delivery so delivery can go back
// to being "email + push notifications" only.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"construct/source/internal/devicebus"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hub is the process-wide device-bus. Initialized once in main.go and
// shared with the scheduler notifier.
var Hub *devicebus.Hub

const maxRelayBodyBytes = 256 << 10

var allowedRelayTypes = map[string]struct{}{
	"assistant.ask":      {},
	"assistant.chunk":    {},
	"assistant.complete": {},
	"desktop.navigate":   {},
	"desktop.open_url":   {},
	"desktop.space.run":  {},
}

func isDesktopCommand(eventType string) bool {
	switch eventType {
	case "desktop.navigate", "desktop.open_url", "desktop.space.run":
		return true
	default:
		return false
	}
}

func isOperatorOutput(eventType string) bool {
	return eventType == "assistant.chunk" || eventType == "assistant.complete"
}

// WSDeviceBus — GET /api/device-bus/ws?token=cat_...
//
// Authentication via query token: browser/Tauri WebSocket constructors
// cannot set Authorization headers, so the device-bus WS endpoint
// accepts the same cat_* token via query param. Header is preferred
// when present (native clients).
//
// Upgrade flow:
//  1. Auth middleware extracts user_id from token.
//  2. Subscribe to hub. Send {type: "hello", conn_id} so the client
//     can echo conn_id back when publishing via the relay (avoids
//     self-echo via PublishExcept).
//  3. Reader goroutine handles inbound "register" frames (operator
//     identifies itself for relay routing).
//  4. Writer loop drains hub events; ping every 25s; tear down on
//     error so client backoff reconnects.
func WSDeviceBus(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	if Hub == nil {
		WriteJSON(w, 503, map[string]string{"error": "bus unavailable"})
		return
	}

	// Accept any origin — same reasoning as delivery's WSStreamNotifications:
	// native clients send Origin: null or app-specific values; the bearer
	// token check above is the actual auth gate.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	connID, ch, unsub := Hub.Subscribe(userID)
	defer unsub()

	// Hello frame: client uses conn_id in X-Sender-Conn-Id when posting
	// to the relay so its own publishes don't echo back.
	_ = wsjson.Write(ctx, c, map[string]any{
		"type":    "hello",
		"conn_id": connID,
	})

	// Reader: process register frames + keep ws library's pong machinery
	// alive. Cancels ctx on close/error so the writer loop exits.
	go func() {
		defer cancel()
		for {
			var inbound struct {
				Type string `json:"type"`
				Kind string `json:"kind"`
			}
			if err := wsjson.Read(ctx, c, &inbound); err != nil {
				return
			}
			if inbound.Type == "register" && inbound.Kind == "operator" {
				Hub.MarkAsOperator(userID, connID)
			}
		}
	}()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case env, open := <-ch:
			if !open {
				return
			}
			if err := wsjson.Write(ctx, c, env); err != nil {
				return
			}
		case <-heartbeat.C:
			pingCtx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pingCtx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// RelayPublish — POST /api/device-bus/relay
//
// Authenticated caller publishes an envelope to their own user_id's
// subscribers. Used by mobile to send assistant.ask to desktop and by
// desktop to stream assistant.chunk / complete back. Use
// X-Sender-Conn-Id to prevent self-echo when publisher is also a
// subscriber on the same hub.
//
// Operator-presence gate: assistant.ask requires the user's operator
// (desktop brain) to be online. Returns 503 with a clear error
// otherwise so the UI can render "your laptop is offline" without
// users wondering why their question vanished.
func RelayPublish(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	if Hub == nil {
		WriteJSON(w, 503, map[string]string{"error": "bus unavailable"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRelayBodyBytes)
	var env devicebus.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid JSON body"})
		return
	}
	if env.Type == "" {
		WriteJSON(w, 400, map[string]string{"error": "type is required"})
		return
	}
	if _, ok := allowedRelayTypes[env.Type]; !ok {
		WriteJSON(w, 400, map[string]string{"error": "unsupported relay type"})
		return
	}

	senderConnID := r.Header.Get("X-Sender-Conn-Id")
	if isOperatorOutput(env.Type) && !Hub.IsOperator(userID, senderConnID) {
		WriteJSON(w, 403, map[string]string{"error": "operator connection required"})
		return
	}

	if (env.Type == "assistant.ask" || isDesktopCommand(env.Type)) && !Hub.HasOperator(userID) {
		// Desktop offline: try the cloud operator before giving up. It answers
		// assistant asks asynchronously; desktop control commands must wait for
		// the real workstation because the cloud operator has no UI to control.
		if env.Type == "assistant.ask" && tryCloudOperatorFallback(r, userID, env) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		WriteJSON(w, 503, map[string]string{"error": "no operator online"})
		return
	}

	Hub.PublishExcept(userID, env, senderConnID)
	w.WriteHeader(http.StatusNoContent)
}

// OperatorStatus — GET /api/device-bus/operator/status
//
// Reports whether the caller has at least one connected device that
// has identified itself as the operator (the desktop instance running
// brain). Mobile UI uses this to render "your laptop is online" /
// "your laptop is offline" before letting the user fire an
// assistant.ask. Cheap call — answers from the in-process hub, no
// DB lookup.
func OperatorStatus(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r)
	if userID == "" {
		WriteJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	online := false
	if Hub != nil {
		online = Hub.HasOperator(userID)
	}
	WriteJSON(w, 200, map[string]bool{"online": online})
}
