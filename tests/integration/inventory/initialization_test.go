//go:build integration

package inventory_test

import (
	"testing"

	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
	"grpctest/inventory_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInitInventoryRetryDoesNotResetStock(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_inventory_srv").Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	global.DB = db
	t.Cleanup(func() { db.Rollback(); global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterInventoryServer(server, &handler.InventoryServer{}) })
	client := proto.NewInventoryClient(conn)
	req := &proto.GoodsInventoryInfo{GoodsId: 10000001, Num: 100}
	first, err := client.InitInv(ctx, req)
	if err != nil || first.GetNum() != 100 {
		t.Fatalf("initialize inventory: %v, %v", first, err)
	}
	if _, err := client.SetInv(ctx, &proto.GoodsInventoryInfo{GoodsId: req.GoodsId, Num: 90}); err != nil {
		t.Fatal(err)
	}
	retry, err := client.InitInv(ctx, req)
	if err != nil || retry.GetNum() != 90 {
		t.Fatalf("retry must preserve current stock 90: %v, %v", retry, err)
	}
	_, err = client.InitInv(ctx, &proto.GoodsInventoryInfo{GoodsId: req.GoodsId, Num: 101})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("different initial stock must conflict: %v", err)
	}
	got, err := client.InvDetail(ctx, req)
	if err != nil || got.GetNum() != 90 {
		t.Fatalf("conflicting retry changed stock: %v, %v", got, err)
	}
}

func TestInventoryInitializationRejectsLegacyAndInvalidStock(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_inventory_srv").Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	global.DB = db
	t.Cleanup(func() { db.Rollback(); global.DB = previous })
	ctx, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterInventoryServer(s, &handler.InventoryServer{}) })
	client := proto.NewInventoryClient(conn)
	for _, req := range []*proto.GoodsInventoryInfo{{GoodsId: 0, Num: 1}, {GoodsId: 999, Num: -1}} {
		if _, err := client.InitInv(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid initialization: %v", err)
		}
		if _, err := client.SetInv(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid adjustment: %v", err)
		}
	}
	zero, err := client.InitInv(ctx, &proto.GoodsInventoryInfo{GoodsId: 10000002, Num: 0})
	if err != nil || zero.GetNum() != 0 {
		t.Fatalf("zero stock: %v %v", zero, err)
	}
	_, err = client.InitInv(ctx, &proto.GoodsInventoryInfo{GoodsId: 421, Num: 999})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("must not claim or overwrite legacy stock: %v", err)
	}
	stock, err := client.InvDetail(ctx, &proto.GoodsInventoryInfo{GoodsId: 421})
	if err != nil || stock.GetNum() != 100 {
		t.Fatalf("legacy stock changed: %v %v", stock, err)
	}
	var count int64
	if err := db.Table("inventory_initialization").Where("goods=421").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed initialization left metadata")
	}
}
