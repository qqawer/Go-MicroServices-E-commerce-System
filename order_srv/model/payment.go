package model

import "time"

// OrderPayment binds exactly one channel transaction to one local order. It is
// committed under the order row lock together with the order status transition.
type OrderPayment struct {
	ID           int64  `gorm:"primaryKey"`
	OrderSn      string `gorm:"uniqueIndex:uq_order_payment_order"`
	TradeNo      string `gorm:"uniqueIndex:uq_order_payment_trade"`
	AppID        string
	SellerID     string
	AmountCents  int64
	Status       string
	NotifyID     string
	PaidAt       *time.Time
	Review       bool
	ReviewReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (OrderPayment) TableName() string { return "order_payment" }
