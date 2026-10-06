package model

// EsGoods is the search copy of one product. Prices are integer cents; a nil
// InventoryReady means the old SQL snapshot does not know that state.
type EsGoods struct {
	ID             int32  `json:"id"`
	Name           string `json:"name"`
	Brief          string `json:"brief"`
	CategoryID     int32  `json:"category_id"`
	BrandID        int32  `json:"brand_id"`
	PriceCents     int64  `json:"price_cents"`
	OnSale         bool   `json:"on_sale"`
	InventoryReady *bool  `json:"inventory_ready"`
	ShipFree       bool   `json:"ship_free"`
	IsNew          bool   `json:"is_new"`
	IsHot          bool   `json:"is_hot"`
}

func (EsGoods) GetMapping() string {
	return `{
  "settings": {"number_of_shards": 1, "number_of_replicas": 0},
  "mappings": {
    "_meta": {"purpose": "grpctest-goods-hooks-v1"},
    "dynamic": "strict",
    "properties": {
      "id": {"type": "long"},
      "name": {"type": "text", "analyzer": "ik_max_word", "fields": {"raw": {"type": "keyword"}}},
      "brief": {"type": "text", "analyzer": "ik_max_word"},
      "category_id": {"type": "long"},
      "brand_id": {"type": "long"},
      "price_cents": {"type": "long", "coerce": false},
      "on_sale": {"type": "boolean"},
      "inventory_ready": {"type": "boolean"},
      "ship_free": {"type": "boolean"},
      "is_new": {"type": "boolean"},
      "is_hot": {"type": "boolean"}
    }
  }
}`
}
