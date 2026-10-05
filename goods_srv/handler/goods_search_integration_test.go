package handler_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Called by the isolated MySQL/ES integration test, using its temporary resources.
func verifyStoreSearch(t *testing.T, db *gorm.DB, esRequest func(string, string, []byte) (int, []byte)) {
	t.Helper()
	for _, c := range []model.Category{
		{BaseModel: model.BaseModel{ID: 2}, Name: "数码", Level: 2, ParentCategoryID: 1},
		{BaseModel: model.BaseModel{ID: 3}, Name: "手机", Level: 3, ParentCategoryID: 2},
	} {
		if err := db.Omit("ParentCategory", "SubCategory").Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Omit("ParentCategoryID").Create(&model.Category{BaseModel: model.BaseModel{ID: 4}, Name: "其他", Level: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Brands{BaseModel: model.BaseModel{ID: 2}, Name: "其他品牌"}).Error; err != nil {
		t.Fatal(err)
	}
	for i := int32(1); i <= 16; i++ {
		g := model.Goods{BaseModel: model.BaseModel{ID: 1000 + i}, CategoryID: 3, BrandsID: 1,
			Name: "青苹果手机", GoodsBrief: "轻便拍照设备", GoodsSn: fmt.Sprint(i), ShopPrice: 12.5,
			OnSale: true, InventoryReady: true, IsNew: true, IsHot: true, Images: model.GormList{}, DescImages: model.GormList{}}
		switch i {
		case 13:
			g.Name, g.GoodsBrief, g.ShopPrice, g.CategoryID, g.BrandsID = "午后茶点", "搭配青苹果的饼干", 5.99, 4, 2
		case 14:
			g.Name, g.GoodsBrief, g.ShopPrice, g.CategoryID, g.IsNew, g.IsHot = "红富士苹果", "产地直达", 44.9, 1, false, false
		case 15:
			g.OnSale = false
		case 16:
			g.InventoryReady = false
		}
		if err := db.Omit("Category", "Brands").Create(&g).Error; err != nil {
			t.Fatal(err)
		}
	}
	if code, body := esRequest(http.MethodPost, "/"+global.EsIndex+"/_refresh", nil); code != 200 {
		t.Fatalf("refresh: %d %s", code, body)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	proto.RegisterGoodsServer(server, &handler.GoodsServer{})
	go server.Serve(lis)
	defer server.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := proto.NewGoodsClient(conn)
	query := func(req *proto.GoodsFilterRequest, total int32, ids ...int32) {
		t.Helper()
		req.StoreVisibleOnly = true
		result, err := client.GoodsList(context.Background(), req)
		if err != nil || result.Total != total || !result.SearchIndex || len(result.Data) != len(ids) {
			t.Fatalf("query %+v: got %v %v, want total=%d IDs=%v", req, result, err, total, ids)
		}
		for i, id := range ids {
			if result.Data[i].Id != id || !result.Data[i].OnSale || !result.Data[i].InventoryReady {
				t.Fatalf("unexpected order or visibility: %v", result)
			}
		}
	}
	query(&proto.GoodsFilterRequest{KeyWords: "饼干"}, 1, 1013)
	query(&proto.GoodsFilterRequest{KeyWords: "红富士"}, 1, 1014)
	query(&proto.GoodsFilterRequest{KeyWords: "火星探测器"}, 0)
	query(&proto.GoodsFilterRequest{KeyWords: "轻便拍照", PagePerNums: 5}, 12, 1001, 1002, 1003, 1004, 1005)
	query(&proto.GoodsFilterRequest{KeyWords: "轻便拍照", PagePerNums: 5, Pages: 2}, 12, 1006, 1007, 1008, 1009, 1010)
	query(&proto.GoodsFilterRequest{KeyWords: "轻便拍照", PagePerNums: 5, Pages: 3}, 12, 1011, 1012)
	query(&proto.GoodsFilterRequest{KeyWords: "轻便拍照", PagePerNums: 5, Pages: 4}, 12)
	query(&proto.GoodsFilterRequest{TopCategory: 1, Brand: 1, PriceMin: 10, PriceMax: 20, IsNew: true, IsHot: true, PagePerNums: 100}, 12, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1008, 1009, 1010, 1011, 1012)
	query(&proto.GoodsFilterRequest{TopCategory: 4, Brand: 2, PriceMax: 6}, 1, 1013)
	query(&proto.GoodsFilterRequest{PriceMin: 45}, 0)
	legacy, err := client.GoodsList(context.Background(), &proto.GoodsFilterRequest{KeyWords: "饼干"})
	if err != nil || legacy.SearchIndex || legacy.Total != 0 {
		t.Fatalf("management query unexpectedly changed: %v %v", legacy, err)
	}
	t.Log("PASS real gRPC search: Chinese name/brief, descendants, brand/price/flags, stable pages, empty/last page; management retains SQL")

	// Deliberately bypass hooks to create stale ES candidates. Every structured
	// constraint must still be checked against MySQL, not trusted from the index.
	for i, fields := range []map[string]any{
		{"on_sale": false}, {"inventory_ready": false}, {"shop_price": 99},
		{"brands_id": 2}, {"category_id": 4}, {"is_new": false}, {"is_hot": false},
	} {
		if err := db.Model(&model.Goods{}).Where("id = ?", 1001+i).UpdateColumns(fields).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Delete(&model.Goods{BaseModel: model.BaseModel{ID: 1008}}).Error; err != nil {
		t.Fatal(err)
	}
	query(&proto.GoodsFilterRequest{TopCategory: 1, Brand: 1, PriceMin: 10, PriceMax: 20, IsNew: true, IsHot: true, PagePerNums: 100}, 12, 1009, 1010, 1011, 1012)
	query(&proto.GoodsFilterRequest{KeyWords: "轻便拍照", PagePerNums: 2}, 12)
	// Current price is returned even while the old indexed price still matches.
	r, err := client.GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true, KeyWords: "轻便拍照", PagePerNums: 5})
	if err != nil || len(r.Data) == 0 || r.Data[0].Id != 1003 || r.Data[0].ShopPrice != 99 {
		t.Fatalf("current SQL price missing: %v %v", r, err)
	}
	t.Log("PASS stale index: reject off-sale, unready, deleted and mismatched price/brand/category/flags; keep ES total and current SQL price")

	oldIndex := global.EsIndex
	global.EsIndex += "-missing"
	_, err = client.GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true, KeyWords: "苹果"})
	global.EsIndex = oldIndex
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("ES failure silently fell back: %v", err)
	}
	t.Log("PASS ES unavailable: gRPC returns Unavailable without LIKE fallback")
}
