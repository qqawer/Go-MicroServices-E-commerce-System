package model

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	BaseModel
	Mobile string `gorm:"index:idx_mobile;unique;not null;type:varchar(100) comment '用户电话号码，唯一标识'"`
	Password string `gorm:"not null;type:varchar(100) comment '用户密码，在数据库中加密存储'"`
	NickName string `gorm:"type:varchar(20) comment '用户名'"`
	Birthday *time.Time `gorm:"type:datetime comment '用户生日，使用指针避免零值，会转化为null'"`
	Gender string `gorm:"column:gender;default:male;type:varchar(6) comment 'female表示女, male表示男'"`
	Role int `gorm:"column:role;default:1;type:int comment '1表示普通用户，2 表示管理员'"`
}

type BaseModel struct {
	ID       int32 `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"column:add_time"`
	UpdatedAt time.Time `gorm:"column:update_time"`
	DeletedAt gorm.DeletedAt
	IsDeleted bool
}
	