package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Inventory 库存
type Inventory struct {
	BaseModel
	Goods   int32 `gorm:"type:int comment '商品id';index"`
	Stocks  int32 `gorm:"type:int comment '商品库存'"`
	Version int32 `gorm:"type:int"` //分布式乐观锁，用来判断并发情况下库存是否扣减一致
}

// GoodsDetail 商品详细
type GoodsDetail struct {
	Goods int32 //商品id
	Num   int32 //商品数量
}

// GoodsDetailList gorm自定义模型
type GoodsDetailList []GoodsDetail

func (g GoodsDetailList) Value() (driver.Value, error) {
	return json.Marshal(g)
}

func (g *GoodsDetailList) Scan(value interface{}) error {
	var data []byte
	switch value := value.(type) {
	case []byte:
		data = value
	case string:
		data = []byte(value)
	default:
		return fmt.Errorf("invalid stock detail type %T", value)
	}
	return json.Unmarshal(data, g)
}

// StockSellDetail 库存扣减细节 记录扣减库存历史记录
type StockSellDetail struct {
	OrderSn string          `gorm:"type:varchar(200);primaryKey"` //订单号uuid，平台自己生成的订单号
	Status  int32           `gorm:"type:tinyint"`                 // 0 事务内认领，1 已扣减，2 已归还，3 提前取消
	Detail  GoodsDetailList `gorm:"type:json"`
}

func (StockSellDetail) TableName() string {
	return "stockselldetail"
}
