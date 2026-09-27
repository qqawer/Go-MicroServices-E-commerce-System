package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

// A separate RPC prevents older servers from silently discarding the buyer's
// confirmation and ordering whatever happens to be checked at request time.
func (o *OrderServer) CreateReviewedOrderFromAddress(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoResponse, error) {
	return o.createOrder(ctx, req, reviewedCreation)
}

func validateReviewedCreation(req *proto.OrderRequest) error {
	if err := validateAddressSelector(req); err != nil {
		return err
	}
	if len(req.CheckoutItems) < 1 || len(req.CheckoutItems) > 1000 {
		return status.Error(codes.InvalidArgument, "请确认 1 至 1000 条购物车记录")
	}
	seen := make(map[int32]bool, len(req.CheckoutItems))
	var total int64
	for _, item := range req.CheckoutItems {
		if item.GetCartId() <= 0 || item.GetGoodsId() <= 0 || item.GetNums() <= 0 ||
			item.GetUnitCents() <= 0 || item.GetUnitCents() > 100_000_000_000 || seen[item.GetCartId()] {
			return status.Error(codes.InvalidArgument, "结算商品清单无效")
		}
		// Divide before multiplying: both individual fields fit their protocol
		// bounds, but a malicious quantity/price product can overflow int64.
		if item.UnitCents > (1_000_000_000_000-total)/int64(item.Nums) {
			return status.Error(codes.InvalidArgument, "订单总金额超出范围")
		}
		total += item.UnitCents * int64(item.Nums)
		seen[item.CartId] = true
	}
	return nil
}

func checkoutChanged() error {
	return status.Error(codes.Aborted, "结算商品已变化，请重新确认")
}

// Compare the exact row set before creating an intent. Only reviewed requests
// reach this helper; legacy creation retains its existing cart semantics.
func reviewedCartPrices(items []*proto.CheckoutItem, cart []model.ShoppingCart) (map[int32]int64, error) {
	if len(cart) != len(items) {
		return nil, checkoutChanged()
	}
	expected := make(map[int32]*proto.CheckoutItem, len(items))
	prices := make(map[int32]int64, len(items))
	for _, item := range items {
		expected[item.CartId] = item
		if cents, exists := prices[item.GoodsId]; exists && cents != item.UnitCents {
			return nil, checkoutChanged()
		}
		prices[item.GoodsId] = item.UnitCents
	}
	for _, row := range cart {
		item, exists := expected[row.ID]
		if !exists || row.Goods != item.GoodsId || row.Nums != item.Nums {
			return nil, checkoutChanged()
		}
		delete(expected, row.ID)
	}
	if len(expected) != 0 {
		return nil, checkoutChanged()
	}
	return prices, nil
}
