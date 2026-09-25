package handler

import (
	"context"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

// CreateOrderFromAddress has its own method so mixed-version deployments cannot
// accept a selector that an older CreateOrder implementation silently ignores.
func (o *OrderServer) CreateOrderFromAddress(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoResponse, error) {
	return o.createOrder(ctx, req, true)
}

func validateAddressCreation(req *proto.OrderRequest) error {
	if req.GetUserId() <= 0 || !requestKeyPattern.MatchString(req.GetRequestKey()) || req.GetAddressId() <= 0 {
		return status.Error(codes.InvalidArgument, "用户、地址或 Idempotency-Key 无效")
	}
	if req.Name != "" || req.Address != "" || req.Mobile != "" || req.Id != 0 || utf8.RuneCountInString(req.Post) > 20 {
		return status.Error(codes.InvalidArgument, "地址簿下单仅接受 addressId 和留言，不可混入收货信息")
	}
	return nil
}

// Only a new intent resolves an address. The winner's persisted snapshot owns the
// shipping details even if the address is edited, deleted, or later unavailable.
func creationShipping(ctx context.Context, req *proto.OrderRequest) (model.OrderInfo, error) {
	shipping := model.OrderInfo{User: req.UserId, Address: req.Address, SignerName: req.Name, SingerMobile: req.Mobile, Post: req.Post}
	if req.AddressId == 0 {
		return shipping, nil
	}
	if global.AddressBookClient == nil {
		return shipping, status.Error(codes.FailedPrecondition, "地址服务尚未就绪")
	}
	address, err := global.AddressBookClient.GetAddress(ctx, &proto.AddressLookup{UserId: req.UserId, Id: req.AddressId})
	if err != nil {
		return shipping, err
	}
	if address == nil || address.Id != req.AddressId || address.UserId != req.UserId {
		return shipping, status.Error(codes.NotFound, "收货地址不存在")
	}
	for _, part := range []string{address.Name, address.Province, address.City, address.District, address.Address} {
		if strings.TrimSpace(part) == "" {
			return shipping, status.Error(codes.FailedPrecondition, "收货地址不完整")
		}
	}
	shipping.SignerName = address.Name
	shipping.SingerMobile = address.Mobile
	shipping.Address = strings.Join([]string{address.Province, address.City, address.District, address.Address}, " ")
	if err := validateShipping(shipping.SignerName, shipping.Address, shipping.SingerMobile, shipping.Post); err != nil {
		return shipping, err
	}
	return shipping, nil
}
