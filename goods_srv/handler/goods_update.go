package handler

import (
	"context"
	"errors"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpdateGoods replaces product details. Sale states belong to UpdateGoodsStatus;
// the legacy CreateGoodsInfo state/stock fields are not used by this operation.
func (s *GoodsServer) UpdateGoods(ctx context.Context, req *proto.CreateGoodsInfo) (*emptypb.Empty, error) {
	if req.GetId() <= 0 || req.GetCategoryId() <= 0 || req.GetBrandId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "商品、分类和品牌 ID 必须为正整数")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockGoodsForUpdate(tx, req.Id); err != nil {
			return err
		}
		if err := tx.Select("id").First(&model.Category{}, req.CategoryId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.InvalidArgument, "商品分类不存在")
			}
			return err
		}
		if err := tx.Select("id").First(&model.Brands{}, req.BrandId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.InvalidArgument, "品牌不存在")
			}
			return err
		}
		// Explicit columns preserve states, counters and stock, and never save
		// category/brand associations. A map also writes false and empty values.
		return tx.Model(&model.Goods{}).Where("id = ?", req.Id).Updates(map[string]interface{}{
			"category_id": req.CategoryId, "brands_id": req.BrandId,
			"name": req.Name, "goods_sn": req.GoodsSn,
			"market_price": req.MarketPrice, "shop_price": req.ShopPrice,
			"goods_brief": req.GoodsBrief, "ship_free": req.ShipFree,
			"images":            model.GormList(append([]string{}, req.Images...)),
			"desc_images":       model.GormList(append([]string{}, req.DescImages...)),
			"goods_front_image": req.GoodsFrontImage,
		}).Error
	})
	return goodsUpdateResult(ctx, err)
}

// UpdateGoodsStatus writes only present fields, including explicit false.
func (s *GoodsServer) UpdateGoodsStatus(ctx context.Context, req *proto.UpdateGoodsStatusRequest) (*emptypb.Empty, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "商品 ID 必须为正整数")
	}
	fields := make(map[string]interface{})
	if req.IsNew != nil {
		fields["is_new"] = *req.IsNew
	}
	if req.IsHot != nil {
		fields["is_hot"] = *req.IsHot
	}
	if req.OnSale != nil {
		fields["on_sale"] = *req.OnSale
	}
	if len(fields) == 0 {
		return nil, status.Error(codes.InvalidArgument, "至少提交一个商品状态")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockGoodsForUpdate(tx, req.Id); err != nil {
			return err
		}
		return tx.Model(&model.Goods{}).Where("id = ?", req.Id).Updates(fields).Error
	})
	return goodsUpdateResult(ctx, err)
}

func lockGoodsForUpdate(tx *gorm.DB, id int32) error {
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&model.Goods{}, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "商品不存在")
	}
	return err
}

func goodsUpdateResult(ctx context.Context, err error) (*emptypb.Empty, error) {
	if err == nil {
		return &emptypb.Empty{}, nil
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		return nil, err
	}
	zap.S().Errorw("更新商品失败", "error", err)
	return nil, status.Error(codes.Internal, "更新商品失败")
}
