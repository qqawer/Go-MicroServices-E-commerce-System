//go:build integration

package order_test

import (
	"testing"

	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
)

func TestCartReadMergeUpdateAndDelete(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_orders_srv")
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterOrderServer(s, &handler.OrderServer{}) })
	client := proto.NewOrderClient(conn)
	assertCart := func(user, count, nums int32, checked bool) {
		t.Helper()
		cart, err := client.CartItemList(ctx, &proto.UserInfo{Id: user})
		if err != nil || cart.GetTotal() != count || len(cart.GetData()) != int(count) {
			t.Fatalf("cart user=%d: %v %v", user, cart, err)
		}
		if count == 1 {
			item := cart.Data[0]
			if item.UserId != user || item.GoodsId != 421 || item.Nums != nums || item.Checked != checked {
				t.Fatalf("unexpected cart: %v", item)
			}
		}
	}
	assertCart(1, 1, 2, true)
	merged, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: pb.Int32(1)})
	if err != nil || merged.GetId() != 1 {
		t.Fatalf("merge: %v %v", merged, err)
	}
	assertCart(1, 1, 3, true)
	other, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 2, GoodsId: 421, Nums: pb.Int32(5)})
	if err != nil || other.GetId() == 1 {
		t.Fatal("second user's cart must be independent", err)
	}
	// Row ID 1 differs from product ID 421. Update only the submitted field.
	for _, req := range []*proto.CartItemRequest{
		{Id: 1, UserId: 1, Nums: pb.Int32(4)},
		{Id: 1, UserId: 1, Checked: pb.Bool(false)},
		{Id: 1, UserId: 1, Checked: pb.Bool(false)}, // unchanged value is still success
		{Id: 1, UserId: 1, Nums: pb.Int32(2), Checked: pb.Bool(true)},
	} {
		if _, err := client.UpdateCartItem(ctx, req); err != nil {
			t.Fatalf("patch: %v", err)
		}
		switch {
		case req.Checked == nil:
			assertCart(1, 1, 4, true)
		case req.Nums == nil:
			assertCart(1, 1, 4, false)
		default:
			assertCart(1, 1, 2, true)
		}
		assertCart(2, 1, 5, false)
	}
	// Other users' rows and product IDs are not interchangeable with our cart ID.
	for _, id := range []int32{other.Id, 421} {
		if _, err := client.UpdateCartItem(ctx, &proto.CartItemRequest{Id: id, UserId: 1, Nums: pb.Int32(9)}); status.Code(err) != codes.NotFound {
			t.Fatalf("foreign/missing update: %v", err)
		}
		if _, err := client.DeleteCartItem(ctx, &proto.CartItemRequest{Id: id, UserId: 1}); status.Code(err) != codes.NotFound {
			t.Fatalf("foreign/missing delete: %v", err)
		}
	}
	assertCart(1, 1, 2, true)
	assertCart(2, 1, 5, false)
	if _, err := client.CartItemList(ctx, &proto.UserInfo{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("zero user must not expose every cart", err)
	}
	if _, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: pb.Int32(2147483647)}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("quantity overflow must fail", err)
	}
	assertCart(1, 1, 2, true)
	if _, err := client.DeleteCartItem(ctx, &proto.CartItemRequest{Id: 1, UserId: 1}); err != nil {
		t.Fatal(err)
	}
	assertCart(1, 0, 0, false)
	assertCart(2, 1, 5, false)
	if _, err := client.UpdateCartItem(ctx, &proto.CartItemRequest{Id: 1, UserId: 1, Nums: pb.Int32(3)}); status.Code(err) != codes.NotFound {
		t.Fatal("deleted row was recreated", err)
	}
	if _, err := client.DeleteCartItem(ctx, &proto.CartItemRequest{Id: 1, UserId: 1}); status.Code(err) != codes.NotFound {
		t.Fatal("deleted row is still visible", err)
	}
	created, err := client.CreateCarItem(ctx, &proto.CartItemRequest{UserId: 1, GoodsId: 421, Nums: pb.Int32(2)})
	if err != nil || created.GetId() <= 1 {
		t.Fatal("new row must be created after deletion", err)
	}
	assertCart(1, 1, 2, false)
}
