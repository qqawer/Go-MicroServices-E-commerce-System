package model

import "time"

// GoodsCreation survives RPC timeouts and process restarts. Keep these records
// for as long as clients can retry their creation request.
type GoodsCreation struct {
	RequestKey      string `gorm:"primaryKey"`
	RequestHash     string
	GoodsID         *int32
	InitialStock    int32
	RequestedOnSale bool
	CreatedAt       time.Time
}
