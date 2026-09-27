//go:build integration

package goods_test

import (
	"fmt"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"gorm.io/gorm/clause"
)

func TestGoodsListStoreVisibility(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_goods_srv").Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	global.DB = db
	t.Cleanup(func() { db.Rollback(); global.DB = previous })
	states := []struct{ sale, ready bool }{{true, true}, {true, true}, {true, true}, {false, true}, {true, false}, {false, false}}
	for index, state := range states {
		goods := model.Goods{
			BaseModel: model.BaseModel{ID: int32(91001 + index)},
			Name:      fmt.Sprintf("Store visibility fixture %d", index), GoodsSn: fmt.Sprintf("STORE-VISIBILITY-%d", index),
			CategoryID: 1, BrandsID: 1, OnSale: state.sale, InventoryReady: state.ready,
			LegacyStocks: 0, MarketPrice: 20, ShopPrice: 12.5, GoodsBrief: "Store visibility fixture",
			Images: model.GormList{"https://example.invalid/store.png"}, DescImages: model.GormList{},
			GoodsFrontImage: "https://example.invalid/store.png",
		}
		if err := db.Omit(clause.Associations).Create(&goods).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterGoodsServer(server, &handler.GoodsServer{}) })
	client := proto.NewGoodsClient(conn)
	for index, length := range []int{2, 1, 0} {
		list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{
			StoreVisibleOnly: true, KeyWords: "Store visibility fixture", Brand: 1, PriceMin: 10, PriceMax: 15,
			Pages: int32(index + 1), PagePerNums: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if list.Total != 3 || len(list.Data) != length {
			t.Fatalf("visible page %d: total=%d rows=%d", index+1, list.Total, len(list.Data))
		}
		for _, goods := range list.Data {
			if goods.Id < 91001 || goods.Id > 91003 || !goods.OnSale || !goods.InventoryReady ||
				goods.Category.GetName() != "Test category" || goods.Brand.GetName() != "Test brand" {
				t.Fatalf("store returned a hidden product or lost associations: %v", goods)
			}
		}
	}
	list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{KeyWords: "Store visibility fixture", Pages: 1, PagePerNums: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 6 || len(list.Data) != 6 {
		t.Fatalf("default request must preserve all six fixture rows: %v", list)
	}
	for _, goods := range list.Data {
		index := goods.Id - 91001
		if index < 0 || index >= int32(len(states)) || goods.OnSale != states[index].sale || goods.InventoryReady != states[index].ready {
			t.Fatalf("default request changed product visibility state: %v", goods)
		}
	}
}
