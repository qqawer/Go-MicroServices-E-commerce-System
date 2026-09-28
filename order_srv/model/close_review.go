package model

import "time"

// OrderCloseReview records an unresolved closure separately from payment evidence.
// It never invents a channel transaction or authorizes inventory release.
type OrderCloseReview struct {
	OrderSn         string     `gorm:"primaryKey" json:"order_sn"`
	Reason          string     `json:"reason"`
	Attempts        int64      `json:"attempts"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	LastCheckedAt   time.Time  `json:"last_checked_at"`
	NextCheckAt     time.Time  `json:"next_check_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	Resolution      string     `json:"resolution,omitempty"`
	RequestID       string     `json:"request_id,omitempty"`
	Operator        string     `json:"operator,omitempty"`
	RequestHash     string     `json:"-"`
	AppID           string     `json:"-"`
	SellerID        string     `json:"-"`
	ActionStartedAt *time.Time `json:"action_started_at,omitempty"`
	ActionExpiresAt *time.Time `json:"action_expires_at,omitempty"`
}

func (OrderCloseReview) TableName() string { return "order_close_review" }
