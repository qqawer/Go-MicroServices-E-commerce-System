package model

import "time"

// InventoryInitialization preserves the original request separately from mutable stock.
type InventoryInitialization struct {
	Goods        int32 `gorm:"primaryKey;autoIncrement:false"`
	InitialStock int32
	CreatedAt    time.Time
}
