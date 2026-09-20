//go:build integration

package order_test

import (
	"testing"

	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
)

func TestCartReadMergeUpdateAndDelete(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_orders_srv")
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterOrderServer(server, &handler.OrderServer{}) })
	client := proto.NewOrderClient(conn)
	assertCart := func(user, count, nums int32, checked bool) {
		t.Helper()
		cart, err := client.CartItemList(ctx, &proto.UserInfo{Id: user})
		if err != nil || cart.GetTotal() != count || len(cart.GetData()) != int(count) {
			t.Fatal("cart row count mismatch")
		}
		if count == 1 {
			item := cart.Data[0]
			if item.GetUserId() != user || item.GetGoodsId() != 421 || item.GetNums() != nums || item.GetChecked() != checked {
				t.Fatal("cart fields were not persisted correctly")
			}
		}
	}
	assertCart(1, 1, 2, true)
	merged, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: 1})
	if err != nil || merged.GetId() != 1 {
		t.Fatal("adding an existing item must retain the cart row")
	}
	assertCart(1, 1, 3, true)
	// Send both fields; partial-update semantics belong to the later cart fixes.
	if _, err := client.UpdateCartItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: 4, Checked: false}); err != nil {
		t.Fatal("cart update failed")
	}
	assertCart(1, 1, 4, false)
	if _, err := client.DeleteCartItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421}); err != nil {
		t.Fatal("cart delete failed")
	}
	assertCart(1, 0, 0, false)
	created, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: 2})
	if err != nil || created.GetId() <= 1 {
		t.Fatal("new cart row was not created after deletion")
	}
	assertCart(1, 1, 2, false)
}
