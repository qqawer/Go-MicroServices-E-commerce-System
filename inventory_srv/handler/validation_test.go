package handler

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/model/proto"
	"testing"
)

func TestInvalidStockRequestsDoNotReachDatabase(t *testing.T) {
	previous := global.DB
	global.DB = nil
	t.Cleanup(func() { global.DB = previous })
	server := &InventoryServer{}
	for _, req := range []*proto.SellInfo{
		nil, {}, {OrderSn: "space not allowed"}, {OrderSn: "valid"},
		{OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{nil}},
		{OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 1, Num: 0}}},
		{OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: -1, Num: 2}}},
		{OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 1, Num: -2}}},
		{OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 1, Num: 2147483647}, {GoodsId: 1, Num: 1}}},
	} {
		if _, err := server.Sell(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Sell accepted invalid request %v: %v", req, err)
		}
	}
	for _, req := range []*proto.SellInfo{nil, {}, {OrderSn: "valid", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 1, Num: -1}}}} {
		if _, err := server.Reback(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Reback accepted invalid request %v: %v", req, err)
		}
	}
	if _, err := server.GetStockOperation(context.Background(), &proto.StockOperationRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
