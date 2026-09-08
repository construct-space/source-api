package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"construct/source/internal/database"
	"construct/source/internal/models"
)

// ── Public read ──────────────────────────────────────────────────────────

// GetFeed returns the home-page top-strip layout. Public — no auth. If the
// feed_items table has no active rows we fall back to a hardcoded seed so a
// fresh environment never shows a blank strip.
func GetFeed(w http.ResponseWriter, r *http.Request) {
	rows, err := loadActiveFeed()
	if err == nil && len(rows) > 0 {
		WriteJSON(w, http.StatusOK, map[string]any{"layout": rowsToLayout(rows)})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"layout": seedLayout()})
}

// PostFeedItem stays here for backwards compat with existing callers but
// is no longer wired to the public mux — admin mutations go through the
// /api/admin/feed-items endpoints below.
func PostFeedItem(w http.ResponseWriter, r *http.Request) {
	var item struct {
		Type  string `json:"type"`
		Title string `json:"title"`
		Body  string `json:"body"`
		URL   string `json:"url"`
		Icon  string `json:"icon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	if item.Title == "" || item.Body == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "title and body required"})
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"status": "ok"})
}

// ── Admin CRUD (gateway-secret gated) ────────────────────────────────────
//
// The gateway forwards Oracle's requests with X-Internal-Secret set. We
// also accept a direct Bearer token via the existing auth middleware in
// main.go; the routes themselves are wrapped in auth(...) there.

// AdminListFeedItems returns every feed item including inactive ones,
// ordered by position. Used by Oracle's feed admin page.
func AdminListFeedItems(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var rows []models.FeedItem
	if err := database.DB.Order("position ASC, created_at ASC").Find(&rows).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "load failed: " + err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, feedItemToMap(row))
	}
	WriteJSON(w, 200, map[string]any{"items": items})
}

// AdminCreateFeedItem inserts a new block. If position is 0 it's appended
// to the end so new entries are predictable.
func AdminCreateFeedItem(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	item, err := decodeFeedItem(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if item.ID == "" {
		item.ID = uuid()
	}
	if item.Position == 0 {
		var maxPos int
		database.DB.Model(&models.FeedItem{}).Select("COALESCE(MAX(position), 0)").Scan(&maxPos)
		item.Position = maxPos + 1
	}
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	if err := database.DB.Create(&item).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "create failed: " + err.Error()})
		return
	}
	WriteJSON(w, 201, map[string]any{"item": feedItemToMap(item)})
}

// AdminUpdateFeedItem partial-updates a block by id.
func AdminUpdateFeedItem(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}
	incoming, err := decodeFeedItem(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	var existing models.FeedItem
	if err := database.DB.First(&existing, "id = ?", id).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	// Only overwrite fields the caller actually sent; keep id/timestamps.
	existing.Type = incoming.Type
	existing.Label = incoming.Label
	existing.Title = incoming.Title
	existing.Body = incoming.Body
	existing.Route = incoming.Route
	existing.URL = incoming.URL
	existing.Icon = incoming.Icon
	existing.Items = incoming.Items
	existing.Cols = incoming.Cols
	existing.Position = incoming.Position
	existing.Active = incoming.Active
	existing.UpdatedAt = time.Now().UTC()
	if err := database.DB.Save(&existing).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "save failed: " + err.Error()})
		return
	}
	WriteJSON(w, 200, map[string]any{"item": feedItemToMap(existing)})
}

// AdminDeleteFeedItem removes a block by id.
func AdminDeleteFeedItem(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}
	if err := database.DB.Delete(&models.FeedItem{}, "id = ?", id).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "delete failed: " + err.Error()})
		return
	}
	WriteJSON(w, 200, map[string]any{"status": "deleted"})
}

// AdminReorderFeedItems replaces the position column for every id in
// order. Any id not listed keeps its previous position. Atomic: all
// updates in one transaction.
func AdminReorderFeedItems(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
		WriteJSON(w, 400, map[string]any{"error": "ids required"})
		return
	}
	tx := database.DB.Begin()
	for i, id := range body.IDs {
		if err := tx.Model(&models.FeedItem{}).Where("id = ?", id).
			Update("position", i+1).Error; err != nil {
			tx.Rollback()
			WriteJSON(w, 500, map[string]any{"error": "reorder failed: " + err.Error()})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "commit failed: " + err.Error()})
		return
	}
	WriteJSON(w, 200, map[string]any{"status": "ok"})
}

// ── Helpers ──────────────────────────────────────────────────────────────

// decodeFeedItem parses a single FeedItem from the request body, converting
// the `items` JSON array (changelog bullets) into the stored-as-JSON-string
// column. Fails on any obviously-bad input but otherwise permissive.
func decodeFeedItem(r *http.Request) (models.FeedItem, error) {
	var raw struct {
		ID       string   `json:"id"`
		Type     string   `json:"type"`
		Label    string   `json:"label"`
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Route    string   `json:"route"`
		URL      string   `json:"url"`
		Icon     string   `json:"icon"`
		Items    []string `json:"items"`
		Cols     int      `json:"cols"`
		Position int      `json:"position"`
		Active   *bool    `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return models.FeedItem{}, err
	}
	raw.Type = strings.TrimSpace(raw.Type)
	if raw.Type == "" {
		return models.FeedItem{}, jsonError("type is required")
	}
	if raw.Cols <= 0 {
		raw.Cols = 3
	}
	if raw.Cols > 9 {
		raw.Cols = 9
	}
	itemsJSON := ""
	if len(raw.Items) > 0 {
		b, _ := json.Marshal(raw.Items)
		itemsJSON = string(b)
	}
	active := true
	if raw.Active != nil {
		active = *raw.Active
	}
	return models.FeedItem{
		ID:       raw.ID,
		Type:     raw.Type,
		Label:    raw.Label,
		Title:    raw.Title,
		Body:     raw.Body,
		Route:    raw.Route,
		URL:      raw.URL,
		Icon:     raw.Icon,
		Items:    itemsJSON,
		Cols:     raw.Cols,
		Position: raw.Position,
		Active:   active,
	}, nil
}

type jsonError string

func (e jsonError) Error() string { return string(e) }

func loadActiveFeed() ([]models.FeedItem, error) {
	var rows []models.FeedItem
	err := database.DB.Where("active = ?", true).
		Order("position ASC, created_at ASC").
		Find(&rows).Error
	return rows, err
}

// rowsToLayout converts DB rows to the client-facing block shape. Mirrors
// seedLayout's output so the consumer doesn't need to branch on source.
func rowsToLayout(rows []models.FeedItem) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, blockFromItem(row))
	}
	return out
}

// blockFromItem shapes a FeedItem into the {type,title,body,items,icon,cols…}
// structure the frontend expects. Empty strings are omitted so the JSON
// stays tidy.
func blockFromItem(row models.FeedItem) map[string]any {
	block := map[string]any{
		"type": row.Type,
		"cols": row.Cols,
	}
	if row.Label != "" {
		block["label"] = row.Label
	}
	if row.Title != "" {
		block["title"] = row.Title
	}
	if row.Body != "" {
		block["body"] = row.Body
	}
	if row.Route != "" {
		block["route"] = row.Route
	}
	if row.URL != "" {
		block["url"] = row.URL
	}
	if row.Icon != "" {
		block["icon"] = row.Icon
	}
	if row.Items != "" {
		var items []string
		if err := json.Unmarshal([]byte(row.Items), &items); err == nil && len(items) > 0 {
			block["items"] = items
		}
	}
	return block
}

// feedItemToMap is the admin-side shape — includes id/position/active for
// editing. Items is returned as an array for UI convenience.
func feedItemToMap(row models.FeedItem) map[string]any {
	items := []string{}
	if row.Items != "" {
		_ = json.Unmarshal([]byte(row.Items), &items)
	}
	return map[string]any{
		"id":         row.ID,
		"type":       row.Type,
		"label":      row.Label,
		"title":      row.Title,
		"body":       row.Body,
		"route":      row.Route,
		"url":        row.URL,
		"icon":       row.Icon,
		"items":      items,
		"cols":       row.Cols,
		"position":   row.Position,
		"active":     row.Active,
		"created_at": row.CreatedAt,
		"updated_at": row.UpdatedAt,
	}
}

// seedLayout is the original hardcoded block list, kept as a fallback for
// empty-table environments (local dev, fresh install, disaster recovery).
func seedLayout() []map[string]any {
	return []map[string]any{
		{"type": "info", "label": "Projects", "body": "Manage org projects, clone repos, and assign members", "icon": "folder-kanban", "cols": 3},
		{"type": "info", "label": "Chat", "body": "Brainstorm ideas with AI", "icon": "message-circle", "cols": 3},
		{"type": "info", "label": "Spaces", "body": "Coder, Architect, Editor, and more", "icon": "grid-2x2", "cols": 3},
		{"type": "changelog", "title": "What's New", "icon": "zap", "items": []string{
			"Custom roles & permissions",
			"Org project management",
			"Permission-based access control",
		}, "cols": 4},
		{"type": "tip", "title": "Getting Started", "body": "Use Chat to brainstorm, Architect to plan, and Coder to build. Drop a folder on the dock icon to open an existing project.", "icon": "book-open", "cols": 5},
	}
}
