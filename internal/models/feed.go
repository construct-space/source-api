package models

import "time"

// FeedItem is a single block on the construct-app home page top strip.
// Served publicly via GET /api/feed; written by staff via Oracle using
// gateway-secret-gated /api/admin/feed-items endpoints.
//
// Shape mirrors the frontend's `FeedBlockData` so the backend can round-trip
// a whole layout without additional transformation. `Items` is a JSON column
// because it's only ever used by the `changelog` block type as a bullet list.
type FeedItem struct {
	ID        string    `json:"id" gorm:"primaryKey;size:36"`
	Type      string    `json:"type" gorm:"size:32;not null"`    // action | announcement | changelog | tip | update | info
	Label     string    `json:"label,omitempty" gorm:"size:120"` // used by 'action' blocks
	Title     string    `json:"title,omitempty" gorm:"size:255"`
	Body      string    `json:"body,omitempty" gorm:"type:text"`
	Route     string    `json:"route,omitempty" gorm:"size:255"` // in-app navigation target
	URL       string    `json:"url,omitempty" gorm:"size:500"`   // external link
	Icon      string    `json:"icon,omitempty" gorm:"size:64"`
	Items     string    `json:"-" gorm:"type:text"` // serialized []string; exposed via ItemsList below
	Cols      int       `json:"cols" gorm:"not null;default:3"` // 1–9; top strip is 12 cols, first 3 reserved
	Position  int       `json:"position" gorm:"not null;default:0;index"`
	Active    bool      `json:"active" gorm:"not null;default:true"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (FeedItem) TableName() string { return "feed_items" }
