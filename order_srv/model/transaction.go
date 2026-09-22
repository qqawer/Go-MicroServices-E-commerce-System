package model

import "time"

const (
	OrderPreparing int32 = iota
	OrderCommitted
	OrderCancelled
)

// OrderTransaction fences a late creation after RocketMQ has decided to cancel.
type OrderTransaction struct {
	OrderSn       string `gorm:"primaryKey"`
	State         int32
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UserID        int32
	RequestKey    *string
	RequestHash   string
	Snapshot      *string `gorm:"type:json"`
	ExpiresAt     *time.Time
	StockReleased bool
}

func (OrderTransaction) TableName() string { return "order_transaction" }

// OrderEvent is written in the same transaction as the business state change.
type OrderEvent struct {
	ID         int64 `gorm:"primaryKey"`
	OrderSn    string
	Topic      string
	Body       string `gorm:"type:json"`
	DelayLevel int
	Published  bool
	CreatedAt  time.Time
}

func (OrderEvent) TableName() string { return "order_event" }
