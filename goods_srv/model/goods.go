package model


type Category struct {
	BaseModel
	Name string `gorm:"column:name;type:varchar(20)" json:"name"`
	ParentCategoryID int32 `json:"parent"`
	ParentCategory *Category `json:"-"`
	SubCategory []*Category `gorm:"foreignKey:ParentCategoryID;references:ID" json:"sub_category"`
	Level int32 `gorm:"column:level; type:int;not null;default:1" json:"level"`
	IsTab bool `gorm:"column:is_tab; not null;default:false" json:"is_tab"`
	URL string `gorm:"column:url" json:"-"` // Existing required column; not exposed by the category API.
}

type Brands struct{
	BaseModel
	Name string `gorm:"type:varchar(20);not null"`
	Logo string `gorm:"type:varchar(200);default:'';not null"`
}

type GoodsCategoryBrand struct{
	BaseModel
	CategoryID int32 `gorm:"type:int;index:idx_category_brand,unique"`
	Category Category
	BrandsID int32 `gorm:"type:int;index:idx_category_brand,unique"`
	Brands Brands
}

type Banner struct{
	BaseModel
	Image string `gorm:"type:varchar(200);not null"`
	Url string `gorm:"type:varchar(200);not null"`
	Index int32 `gorm:"type:int;not null;default:1"`
}

type Goods struct{
	BaseModel

	CategoryID int32 `gorm:"type:int;not null"`
	Category Category
	BrandsID int32 `gorm:"type:int;not null"`
	Brands Brands

	
	OnSale bool `gorm:"type:bool;not null;default:false;comment:'是否上架'"`
	ShipFree bool `gorm:"type:bool;not null;default:false;comment:'是否包邮'"`
	IsNew bool `gorm:"type:bool;not null;default:false;comment:'是否新品'"`
	IsHot bool `gorm:"type:bool;not null;default:false;comment:'是否热门'"`


	Name string `gorm:"type:varchar(50);not null;comment:'商品名称'"`
	GoodsSn string `gorm:"type:varchar(50);not null;comment:'商品唯一标识'"`
	ClickNum int32 `gorm:"type:int;not null;default:0;comment:'点击量'"`
	SoldNum int32 `gorm:"type:int;not null;default:0;comment:'销售量'"`
	FavNum int32 `gorm:"type:int;not null;default:0;comment:'收藏量'"`
	MarketPrice float32 `gorm:"not null;comment:'活动价格'"`
	ShopPrice float32 `gorm:"not null;comment:'平时价格'"`
	GoodsBrief string `gorm:"type:varchar(100);not null;comment:'商品简介'"`
	Images GormList `gorm:"type:varchar(1000);not null;comment:'商品图片'"`
	DescImages GormList `gorm:"type:varchar(1000);not null;comment:'商品详情图片'"`
	GoodsFrontImage string `gorm:"type:varchar(200);not null;comment:'商品封面图片'"`
}
