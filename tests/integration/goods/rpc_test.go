//go:build integration

package goods_test

import (
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
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
	if goods.GetCategory().GetName() != "Test category" || goods.GetBrand().GetName() != "Test brand" {
		t.Fatal("goods listing did not load category and brand associations")
	}
	detail, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal("goods detail failed")
	}
	// Detail association loading is a separate known business bug; this baseline
	// checks persisted scalar fields and the existing JSON column decoding only.
	for _, item := range []*proto.GoodsInfoResponse{goods, detail} {
		if item.GetId() != 421 || item.GetName() != "Integration goods" || item.GetCategoryId() != 1 || item.GetShopPrice() != 12.5 {
			t.Fatal("goods fields did not survive MySQL and gRPC decoding")
		}
		if len(item.GetImages()) != 1 || item.Images[0] != "https://example.invalid/goods.png" || len(item.GetDescImages()) != 0 {
			t.Fatal("goods JSON image columns did not decode correctly")
		}
	}
}
