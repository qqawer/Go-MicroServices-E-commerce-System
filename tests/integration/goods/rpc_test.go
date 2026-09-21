//go:build integration

package goods_test

import (
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGoodsListAndDetail(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_goods_srv")
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterGoodsServer(server, &handler.GoodsServer{}) })
	client := proto.NewGoodsClient(conn)
	list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 1 || len(list.GetData()) != 1 {
		t.Fatal("goods listing must return the seeded row")
	}
	goods := list.Data[0]
	detail, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal("goods detail failed")
	}
	for _, item := range []*proto.GoodsInfoResponse{goods, detail} {
		if item.GetCategory().GetId() != 1 || item.GetCategory().GetName() != "Test category" ||
			item.GetBrand().GetId() != 1 || item.GetBrand().GetName() != "Test brand" ||
			item.GetBrand().GetLogo() != "https://example.invalid/brand.png" {
			t.Errorf("goods %d must return category and brand details: category=%v brand=%v", item.GetId(), item.GetCategory(), item.GetBrand())
		}
		if item.GetId() != 421 || item.GetName() != "Integration goods" || item.GetCategoryId() != 1 || item.GetShopPrice() != 12.5 {
			t.Fatal("goods fields did not survive MySQL and gRPC decoding")
		}
		if len(item.GetImages()) != 1 || item.Images[0] != "https://example.invalid/goods.png" || len(item.GetDescImages()) != 0 {
			t.Fatal("goods JSON image columns did not decode correctly")
		}
	}
	if _, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 2147483647}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing goods must return NotFound, got %v", err)
	}
}

func TestGoodsListByBrand(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_goods_srv")
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterGoodsServer(server, &handler.GoodsServer{}) })
	client := proto.NewGoodsClient(conn)
	for _, test := range []struct {
		name  string
		brand int32
		total int32
	}{
		{"matching brand", 1, 1},
		{"other brand", 2147483647, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{Brand: test.brand, Pages: 1, PagePerNums: 10})
			if err != nil {
				t.Fatalf("brand filter failed: %v", err)
			}
			if list.GetTotal() != test.total || len(list.GetData()) != int(test.total) {
				t.Fatalf("brand %d: want %d goods, got total=%d rows=%d", test.brand, test.total, list.GetTotal(), len(list.GetData()))
			}
			for _, item := range list.GetData() {
				if item.GetId() != 421 || item.GetBrand().GetId() != test.brand {
					t.Fatalf("brand filter returned a nonmatching product: %v", item)
				}
			}
		})
	}
}
