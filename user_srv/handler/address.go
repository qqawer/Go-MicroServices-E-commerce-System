package handler

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AddressServer struct {
	proto.UnimplementedAddressBookServer
}

var addressMobilePattern = regexp.MustCompile(`^1[0-9]{10}$`)

func addressDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "用户或收货地址不存在")
	}
	return status.Error(codes.Internal, "收货地址操作失败")
}

func addressResponse(address model.Address) *proto.Address {
	return &proto.Address{Id: address.ID, UserId: address.UserID, Name: address.Name, Mobile: address.Mobile,
		Province: address.Province, City: address.City, District: address.District, Address: address.Address}
}

func validAddress(address model.Address) error {
	for _, field := range []struct {
		value string
		max   int
	}{{address.Name, 20}, {address.Province, 20}, {address.City, 20}, {address.District, 20}, {address.Address, 100}} {
		if !utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "" || utf8.RuneCountInString(field.value) > field.max {
			return status.Error(codes.InvalidArgument, "收货人或地址字段无效")
		}
	}
	if !addressMobilePattern.MatchString(address.Mobile) {
		return status.Error(codes.InvalidArgument, "收货手机号无效")
	}
	full := strings.Join([]string{address.Province, address.City, address.District, address.Address}, " ")
	if utf8.RuneCountInString(full) > 100 {
		return status.Error(codes.InvalidArgument, "省市区与详细地址合计不能超过 100 个字符")
	}
	return nil
}

func (s *AddressServer) ListAddresses(ctx context.Context, req *proto.AddressListRequest) (*proto.AddressListResponse, error) {
	if req == nil || req.UserId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	pn, size := req.Pn, req.Psize
	if pn == 0 {
		pn = 1
	}
	if size == 0 {
		size = 10
	}
	offset := (int64(pn) - 1) * int64(size)
	if pn < 1 || size < 1 || size > 100 || offset > math.MaxInt32 {
		return nil, status.Error(codes.InvalidArgument, "分页参数无效")
	}
	var total int64
	var addresses []model.Address
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Address{}).Where("user_id = ?", req.UserId).Count(&total).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", req.UserId).Order("id DESC").Offset(int(offset)).Limit(int(size)).Find(&addresses).Error
	})
	if err != nil {
		return nil, addressDBError(ctx, err)
	}
	if total > math.MaxInt32 {
		return nil, status.Error(codes.ResourceExhausted, "地址数量超出支持范围")
	}
	rsp := &proto.AddressListResponse{Total: int32(total), Data: make([]*proto.Address, 0, len(addresses))}
	for _, address := range addresses {
		rsp.Data = append(rsp.Data, addressResponse(address))
	}
	return rsp, nil
}

func (s *AddressServer) GetAddress(ctx context.Context, req *proto.AddressLookup) (*proto.Address, error) {
	if req == nil || req.UserId <= 0 || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户或地址 ID 无效")
	}
	var address model.Address
	if err := global.DB.WithContext(ctx).Where("id = ? AND user_id = ?", req.Id, req.UserId).First(&address).Error; err != nil {
		return nil, addressDBError(ctx, err)
	}
	return addressResponse(address), nil
}

func (s *AddressServer) CreateAddress(ctx context.Context, req *proto.CreateAddressRequest) (*proto.Address, error) {
	if req == nil || req.UserId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	address := model.Address{UserID: req.UserId, Name: strings.TrimSpace(req.Name), Mobile: strings.TrimSpace(req.Mobile),
		Province: strings.TrimSpace(req.Province), City: strings.TrimSpace(req.City), District: strings.TrimSpace(req.District), Address: strings.TrimSpace(req.Address)}
	if err := validAddress(address); err != nil {
		return nil, err
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&user, req.UserId).Error; err != nil {
			return err
		}
		return tx.Create(&address).Error
	})
	if err != nil {
		return nil, addressDBError(ctx, err)
	}
	return addressResponse(address), nil
}

func (s *AddressServer) UpdateAddress(ctx context.Context, req *proto.UpdateAddressRequest) (*proto.Address, error) {
	if req == nil || req.UserId <= 0 || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户或地址 ID 无效")
	}
	updates := make(map[string]interface{})
	for name, value := range map[string]*string{"name": req.Name, "mobile": req.Mobile, "province": req.Province,
		"city": req.City, "district": req.District, "address": req.Address} {
		if value != nil {
			if strings.TrimSpace(*value) == "" {
				return nil, status.Error(codes.InvalidArgument, "地址字段不能为空")
			}
			updates[name] = strings.TrimSpace(*value)
		}
	}
	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "至少提交一个地址字段")
	}
	var address model.Address
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", req.Id, req.UserId).First(&address).Error; err != nil {
			return err
		}
		for name, target := range map[string]*string{"name": &address.Name, "mobile": &address.Mobile, "province": &address.Province,
			"city": &address.City, "district": &address.District, "address": &address.Address} {
			if value, exists := updates[name]; exists {
				*target = value.(string)
			}
		}
		if err := validAddress(address); err != nil {
			return err
		}
		return tx.Model(&model.Address{}).Where("id = ? AND user_id = ?", req.Id, req.UserId).Updates(updates).Error
	})
	if err != nil {
		if status.Code(err) == codes.InvalidArgument {
			return nil, err
		}
		return nil, addressDBError(ctx, err)
	}
	return addressResponse(address), nil
}

func (s *AddressServer) DeleteAddress(ctx context.Context, req *proto.AddressLookup) (*emptypb.Empty, error) {
	if req == nil || req.UserId <= 0 || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户或地址 ID 无效")
	}
	result := global.DB.WithContext(ctx).Where("id = ? AND user_id = ?", req.Id, req.UserId).Delete(&model.Address{})
	if result.Error != nil {
		return nil, addressDBError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "地址不存在")
	}
	return &emptypb.Empty{}, nil
}
