package model

type UserMessage struct {
	BaseModel
	UserID      int32  `gorm:"column:user_id"`
	MessageType int32  `gorm:"column:message_type"`
	Subject     string `gorm:"column:subject"`
	Message     string `gorm:"column:message"`
}

func (UserMessage) TableName() string { return "user_message" }
