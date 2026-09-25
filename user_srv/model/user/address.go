package model

type Address struct {
	BaseModel
	UserID   int32  `gorm:"column:user_id"`
	Name     string `gorm:"column:name"`
	Mobile   string `gorm:"column:mobile"`
	Province string `gorm:"column:province"`
	City     string `gorm:"column:city"`
	District string `gorm:"column:district"`
	Address  string `gorm:"column:address"`
}

func (Address) TableName() string { return "user_address" }
