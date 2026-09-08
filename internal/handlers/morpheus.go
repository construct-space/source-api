package handlers

import (
	"encoding/json"
	"net/http"
)

type SubmitPatternsRequest struct {
	Patterns []struct {
		AgentType       string `json:"agent_type"`
		FailureCategory string `json:"failure_category"`
		CheckFailed     string `json:"check_failed"`
		StrikeReached   int    `json:"strike_reached"`
		PlaybookUsed    string `json:"playbook_used"`
		ResolvedBy      string `json:"resolved_by"`
		Frequency       int    `json:"frequency"`
		PeriodDays      int    `json:"period_days"`
	} `json:"patterns"`
	InstallationID   string `json:"installation_id"`
	ConstructVersion string `json:"construct_version"`
}

func SubmitVerificationPatterns(w http.ResponseWriter, r *http.Request) {
	var req SubmitPatternsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if len(req.Patterns) == 0 {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "no patterns provided"})
		return
	}
	if len(req.Patterns) > 50 {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "too many patterns, max 50"})
		return
	}
	// Accept and acknowledge — DB persistence added later
	WriteJSON(w, http.StatusOK, map[string]any{"accepted": len(req.Patterns)})
}
