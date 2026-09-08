// Package services provides HTTP clients for peer infra services called
// from source. All requests send the shared X-Internal-Secret header.
package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"
)

var client = &http.Client{Timeout: 3 * time.Second}

// ArchiveSpacesForOrg tells developer to archive all spaces owned by the
// given org and delete the org's Publisher row. Called from org delete
// before the Organization row itself is removed. Best-effort — returns nil
// on network errors so the caller can continue; a follow-up sweep can be
// scheduled if partial failure matters.
func ArchiveSpacesForOrg(orgID string) {
	developerURL := os.Getenv("DEVELOPER_URL")
	secret := os.Getenv("SERVICE_API_KEY")
	if developerURL == "" || secret == "" || orgID == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"org_id": orgID})
	req, _ := http.NewRequest(http.MethodPost, developerURL+"/internal/spaces/archive-for-org", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", secret)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

// OrgIsEnrolledPublisher reports whether the given org has an enrolled
// Publisher in the developer service. Returns false on any error — the
// caller should treat this as "unknown/no" and fall through to dormancy
// warnings rather than block.
func OrgIsEnrolledPublisher(orgID string) bool {
	developerURL := os.Getenv("DEVELOPER_URL")
	secret := os.Getenv("SERVICE_API_KEY")
	if developerURL == "" || secret == "" || orgID == "" {
		return false
	}
	u := developerURL + "/internal/publisher/org?org_id=" + url.QueryEscape(orgID)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("X-Internal-Secret", secret)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	// Publisher exists — developer responded 200. We don't care about fields.
	var discard map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&discard)
	return true
}
