//go:build integration

package inventory_test

import (
	"testing"

	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
	"grpctest/inventory_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
)

func TestInventoryDetail(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_inventory_srv")
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterInventoryServer(server, &handler.InventoryServer{}) })
	stock, err := proto.NewInventoryClient(conn).InvDetail(ctx, &proto.GoodsInventoryInfo{GoodsId: 421})
	if err != nil || stock.GetGoodsId() != 421 || stock.GetNum() != 100 {
		t.Fatal("inventory detail did not return the seeded stock")
	}
}
