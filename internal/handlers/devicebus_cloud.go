package handlers

// Cloud-operator fallback for the device bus.
//
// When a user fires an assistant.ask but has no online desktop operator,
// source-api forwards the question to the cloud operator (the headless brain,
// `brain --operator`) instead of failing with 503. The cloud runs the answer
// as that user — using the asker's own bearer token — and we publish the result
// back into the hub keyed by request_id, exactly as the desktop would. The
// mobile client receives it over its existing WebSocket with no app change.
//
// Configure with OPERATOR_URL (e.g. http://srv-captain--operator:8090) + the
// internal shared secret (INTERNAL_SHARED_SECRET, or the existing
// SERVICE_API_KEY). Inert until OPERATOR_URL is set, so it's safe to ship dark.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"construct/source/internal/devicebus"
)

func cloudOperatorBase() string { return strings.TrimRight(os.Getenv("OPERATOR_URL"), "/") }

// internalSecret is the shared secret for service→operator calls. Prefers the
// canonical name, falls back to the existing source key (see auth middleware).
func internalSecret() string {
	if s := os.Getenv("INTERNAL_SHARED_SECRET"); s != "" {
		return s
	}
	return os.Getenv("SERVICE_API_KEY")
}

// bearerFromRequest pulls the caller's cat_ token (Authorization header, or the
// ?token= query param native WS clients use). The relay is bearer-authed, so on
// a real ask this is the asking user's own token — the cloud runs as them.
func bearerFromRequest(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return r.URL.Query().Get("token")
}

// tryCloudOperatorFallback kicks off a cloud answer for an offline-desktop
// assistant.ask. Returns true if it accepted the request (caller replies 204);
// false if the cloud fallback isn't available/usable (caller should 503).
//
// Fire-and-forget: the answer can take minutes, so we don't block the relay
// response — the mobile client is already listening on the bus and receives the
// assistant.complete we publish when the run finishes (same as the desktop path).
func tryCloudOperatorFallback(r *http.Request, userID string, env devicebus.Envelope) bool {
	base, secret := cloudOperatorBase(), internalSecret()
	if base == "" || secret == "" || Hub == nil {
		return false
	}
	token := bearerFromRequest(r)
	if token == "" {
		return false
	}
	var p struct {
		Text      string `json:"text"`
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if strings.TrimSpace(p.Text) == "" {
		return false
	}

	go func() {
		content, err := callCloudOperator(base, secret, token, p.Text, p.RequestID)
		if err != nil {
			content = "Your desktop is offline and the cloud assistant couldn't finish this: " + err.Error()
		}
		payload, _ := json.Marshal(map[string]any{
			"request_id":  p.RequestID,
			"content":     content,
			"stop_reason": "end_turn",
		})
		Hub.Publish(userID, devicebus.Envelope{Type: "assistant.complete", From: "cloud", Payload: payload})
	}()
	return true
}

func callCloudOperator(base, secret, token, text, requestID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"token": token, "text": text, "request_id": requestID})
	req, err := http.NewRequest("POST", base+"/internal/ask", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Internal-Secret", secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return "", fmt.Errorf("operator %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Content, nil
}
