package handler

import (
	"context"
	"math"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

func reviewedRequest() *proto.OrderRequest {
	r := selectedAddressRequest()
	r.CheckoutItems = []*proto.CheckoutItem{{CartId: 8, GoodsId: 421, Nums: 2, UnitCents: 4490}, {CartId: 7, GoodsId: 422, Nums: 1, UnitCents: 1250}}
	return r
}

func TestReviewedCreationValidationAndLegacyMethods(t *testing.T) {
	if err := validateReviewedCreation(reviewedRequest()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.CheckoutItems = nil },
		func(r *proto.OrderRequest) { r.CheckoutItems = make([]*proto.CheckoutItem, 1001) },
		func(r *proto.OrderRequest) { r.CheckoutItems[0] = nil },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].CartId = 0 },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].GoodsId = -1 },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].Nums = 0 },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].UnitCents = 0 },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].UnitCents = 100_000_000_001 },
		func(r *proto.OrderRequest) {
			r.CheckoutItems[0].UnitCents, r.CheckoutItems[0].Nums = 100_000_000_000, math.MaxInt32
		},
		func(r *proto.OrderRequest) { r.CheckoutItems[1].CartId = r.CheckoutItems[0].CartId },
		func(r *proto.OrderRequest) { r.Name = "raw shipping" },
		func(r *proto.OrderRequest) { r.AddressId = 0 },
	} {
		r := reviewedRequest()
		change(r)
		if status.Code(validateReviewedCreation(r)) != codes.InvalidArgument {
			t.Fatal("invalid reviewed request accepted", r)
		}
	}
	if status.Code(validateReviewedCreation(nil)) != codes.InvalidArgument {
		t.Fatal("nil request accepted")
	}
	boundary := reviewedRequest()
	boundary.CheckoutItems = make([]*proto.CheckoutItem, 1000)
	for i := range boundary.CheckoutItems {
		boundary.CheckoutItems[i] = &proto.CheckoutItem{CartId: int32(i + 1), GoodsId: math.MaxInt32, Nums: 1, UnitCents: 1000}
	}
	if err := validateReviewedCreation(boundary); err != nil {
		t.Fatal("valid field bounds rejected", err)
	}
	boundary.CheckoutItems = []*proto.CheckoutItem{{CartId: math.MaxInt32, GoodsId: math.MaxInt32, Nums: 10, UnitCents: 100_000_000_000}}
	if err := validateReviewedCreation(boundary); err != nil {
		t.Fatal("total limit rejected", err)
	}
	boundary.CheckoutItems = append(boundary.CheckoutItems, &proto.CheckoutItem{CartId: 1, GoodsId: 1, Nums: 1, UnitCents: 1})
	if status.Code(validateReviewedCreation(boundary)) != codes.InvalidArgument {
		t.Fatal("sum exceeding total limit accepted")
	}
	if status.Code(validateAddressCreation(reviewedRequest())) != codes.InvalidArgument {
		t.Fatal("legacy address method ignored checkout items")
	}
	raw := &proto.OrderRequest{UserId: 41, RequestKey: "raw", Name: "测试", Address: "地址", Mobile: "13000000001", CheckoutItems: reviewedRequest().CheckoutItems}
	if status.Code(validateCreation(raw)) != codes.InvalidArgument {
		t.Fatal("legacy raw method ignored checkout items")
	}
}

func TestReviewedHashCanonicalAndLegacyBytesUnchanged(t *testing.T) {
	const oldAddressHash = "f4f68ace8db8ffa8300e4713d4d10b83aa7ea04c22c4c86db1146f88dd046fc0"
	if requestHash(selectedAddressRequest()) != oldAddressHash {
		t.Fatal("legacy address hash changed")
	}
	r := reviewedRequest()
	original := pb.Clone(r)
	hash := requestHash(r)
	if !pb.Equal(r, original) || hash == oldAddressHash {
		t.Fatal("hash mutated request or reused legacy identity")
	}
	reordered := pb.Clone(r).(*proto.OrderRequest)
	reordered.CheckoutItems[0], reordered.CheckoutItems[1] = reordered.CheckoutItems[1], reordered.CheckoutItems[0]
	if requestHash(reordered) != hash {
		t.Fatal("item order changed identity")
	}
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.UserId++ },
		func(r *proto.OrderRequest) { r.AddressId++ },
		func(r *proto.OrderRequest) { r.Post += "字" },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].CartId++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].GoodsId++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].Nums++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].UnitCents++ },
	} {
		changed := pb.Clone(r).(*proto.OrderRequest)
		change(changed)
		if requestHash(changed) == hash {
			t.Fatal("changed reviewed body retained identity")
		}
	}
}

func TestReviewedCartRequiresCompleteRowSet(t *testing.T) {
	items := reviewedRequest().CheckoutItems
	valid := []model.ShoppingCart{{BaseModel: model.BaseModel{ID: 7}, Goods: 422, Nums: 1}, {BaseModel: model.BaseModel{ID: 8}, Goods: 421, Nums: 2}}
	prices, err := reviewedCartPrices(items, valid)
	if err != nil || prices[421] != 4490 || prices[422] != 1250 {
		t.Fatal(prices, err)
	}
	for _, rows := range [][]model.ShoppingCart{
		nil, valid[:1], append(append([]model.ShoppingCart{}, valid...), valid[0]),
		{{BaseModel: model.BaseModel{ID: 9}, Goods: 422, Nums: 1}, valid[1]},
		{{BaseModel: model.BaseModel{ID: 7}, Goods: 421, Nums: 1}, valid[1]},
		{{BaseModel: model.BaseModel{ID: 7}, Goods: 422, Nums: 2}, valid[1]},
	} {
		_, err := reviewedCartPrices(items, rows)
		if status.Code(err) != codes.Aborted || status.Convert(err).Message() != "结算商品已变化，请重新确认" {
			t.Fatal("changed rows accepted", rows, err)
		}
	}
}

func TestReviewedIntentReplayDoesNotReadCurrentAddressCartOrGoods(t *testing.T) {
	mock := cartMock(t)
	useAddressClient(t, nil)
	r := reviewedRequest()
	mock.ExpectQuery("SELECT .* FROM `order_transaction`").WithArgs(r.UserId, r.RequestKey, 1).
		WillReturnRows(sqlmock.NewRows([]string{"order_sn", "user_id", "request_key", "request_hash"}).AddRow("stored-reviewed-order", r.UserId, r.RequestKey, requestHash(r)))
	r.CheckoutItems[0], r.CheckoutItems[1] = r.CheckoutItems[1], r.CheckoutItems[0]
	stored, err := prepareCreation(context.Background(), r)
	if err != nil || stored.OrderSn != "stored-reviewed-order" {
		t.Fatal(stored, err)
	}
}

func TestCheckoutUnitCentsPreservesFloat32DecimalRules(t *testing.T) {
	for price, want := range map[float32]int64{44.9: 4490, 0.01: 1, 12.5: 1250, 1e9: 100_000_000_000} {
		got, err := checkoutUnitCents(price)
		if err != nil || got != want {
			t.Fatal(price, got, err)
		}
	}
	for _, price := range []float32{0, -1, 1.001, float32(math.NaN()), float32(math.Inf(1)), 1e10} {
		if _, err := checkoutUnitCents(price); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("invalid price accepted", price, err)
		}
	}
}
