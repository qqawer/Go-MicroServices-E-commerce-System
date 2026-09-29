//go:build integration

package order_test

import (
	"fmt"
	"testing"
	"time"

	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
)

func TestOrderHistoryPaginationAndOwnership(t *testing.T) {
	db := testdb.Open(t, "grpctest_orders_srv")
	previous := global.DB
	global.DB = db
	t.Cleanup(func() {
		db.Unscoped().Where("user IN ?", []int32{901201, 901202}).Delete(&model.OrderInfo{})
		global.DB = previous
	})
	ctx, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterOrderServer(s, &handler.OrderServer{}) })
	client := proto.NewOrderClient(conn)
	cents := int64(4490)
	var ownIDs []int32
	for n := 0; n < 13; n++ {
		owner := int32(901201)
		if n == 11 {
			owner = 901202
		}
		row := model.OrderInfo{User: owner, OrderSn: fmt.Sprintf("history-test-%d", n), Status: "TRADE_SUCCESS", PayType: "alipay", TotalCents: &cents}
		// Tied timestamps must not destabilize pagination.
		row.CreatedAt = time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if n == 12 {
			if err := db.Delete(&row).Error; err != nil {
				t.Fatal(err)
			}
		} else if owner == 901201 {
			ownIDs = append(ownIDs, row.ID)
		}
	}
	var seen []int32
	for page := int32(1); page <= 3; page++ {
		rsp, err := client.OrderList(ctx, &proto.OrderFilterRequest{UserId: 901201, Pages: page, PagePerNums: 10})
		want := []int{10, 1, 0}[page-1]
		if err != nil || rsp.GetTotal() != 11 || len(rsp.GetData()) != want {
			t.Fatalf("page %d: %v %v", page, rsp, err)
		}
		for _, row := range rsp.Data {
			if row.UserId != 901201 || row.TotalCents != 4490 {
				t.Fatal("foreign order or wrong amount", row)
			}
			seen = append(seen, row.Id)
		}
	}
	for n, id := range seen {
		if id != ownIDs[len(ownIDs)-1-n] {
			t.Fatal("unstable pagination", seen)
		}
	}
	rsp, err := client.OrderList(ctx, &proto.OrderFilterRequest{UserId: 901202})
	if err != nil || rsp.GetTotal() != 1 || len(rsp.GetData()) != 1 || rsp.Data[0].UserId != 901202 {
		t.Fatalf("second owner: %v %v", rsp, err)
	}
}
