package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)
type GormList []string

func(g GormList)Value()(driver.Value,error){
	return json.Marshal(g)
}

//实现sql.Scanner接口，Scan将value扫描到g
func(g *GormList)Scan(value interface{})error{
	return json.Unmarshal(value.([]byte),&g)
}

type BaseModel struct {
	ID        int32     `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"column:add_time"`
	UpdatedAt time.Time `gorm:"column:update_time"`
	DeletedAt gorm.DeletedAt
	IsDeleted bool
}