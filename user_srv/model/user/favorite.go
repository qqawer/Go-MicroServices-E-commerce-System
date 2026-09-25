package model

import "time"

// Favorites are a set of user/product pairs. Removing a pair is a hard delete,
// so the same product can be favorited again without a soft-delete uniqueness trap.
type Favorite struct {
	ID        int32     `gorm:"primaryKey"`
	UserID    int32     `gorm:"column:user_id"`
	GoodsID   int32     `gorm:"column:goods_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (Favorite) TableName() string { return "user_favorite" }
