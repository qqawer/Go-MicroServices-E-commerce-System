package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	invproto "grpctest/inventory_srv/model/proto"
)

var creationKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// CreateGoods commits its intent before contacting inventory. A pending response
// always has an ID and InventoryReady=false; retry with the same key and body.
func (s *GoodsServer) CreateGoods(ctx context.Context, req *proto.CreateGoodsInfo) (*proto.GoodsInfoResponse, error) {
	if !creationKey.MatchString(req.GetRequestKey()) || req.GetId() != 0 || req.GetStocks() < 0 || req.GetCategoryId() <= 0 || req.GetBrandId() <= 0 || req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "创建请求编号、商品信息或初始库存不合法")
	}
	body := gproto.Clone(req).(*proto.CreateGoodsInfo)
	body.RequestKey = ""
	encoded, err := (gproto.MarshalOptions{Deterministic: true}).Marshal(body)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "商品信息无法编码")
	}
	digest := sha256.Sum256(encoded)
	requestHash := hex.EncodeToString(digest[:])
	var goods model.Goods
	var creation model.GoodsCreation
	err = global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		creation = model.GoodsCreation{RequestKey: req.RequestKey, RequestHash: requestHash, InitialStock: req.Stocks, RequestedOnSale: req.OnSale}
		// The no-op duplicate update locks the existing request, without replacing
		// its hash, product ID or original requested stock.
		if err := tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"request_key"})}).Create(&creation).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_key = ?", req.RequestKey).First(&creation).Error; err != nil {
			return err
		}
		if creation.RequestHash != requestHash {
			return status.Error(codes.AlreadyExists, "同一创建请求编号不能用于不同商品内容")
		}
		if creation.GoodsID != nil {
			if err := tx.First(&goods, *creation.GoodsID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return status.Error(codes.FailedPrecondition, "原请求的商品已删除，不能重新创建")
				}
				return err
			}
			return nil
		}
		if err := tx.First(&model.Category{}, req.CategoryId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.InvalidArgument, "商品分类不存在")
			}
			return err
		}
		if err := tx.First(&model.Brands{}, req.BrandId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.InvalidArgument, "品牌不存在")
			}
			return err
		}
		goods = model.Goods{CategoryID: req.CategoryId, BrandsID: req.BrandId, Name: req.Name, GoodsSn: req.GoodsSn,
			MarketPrice: req.MarketPrice, ShopPrice: req.ShopPrice, GoodsBrief: req.GoodsBrief, ShipFree: req.ShipFree,
			Images: append(model.GormList{}, req.Images...), DescImages: append(model.GormList{}, req.DescImages...),
			GoodsFrontImage: req.GoodsFrontImage, IsNew: req.IsNew, IsHot: req.IsHot, OnSale: false, InventoryReady: false}
		if err := tx.Omit(clause.Associations).Create(&goods).Error; err != nil {
			return err
		}
		return tx.Model(&creation).Update("goods_id", goods.ID).Error
	})
	if err != nil {
		return nil, goodsCreationError(ctx, err)
	}
	if !goods.InventoryReady {
		if s.Inventory == nil {
			return creationResponse(goods), nil
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = s.Inventory.InitInv(callCtx, &invproto.GoodsInventoryInfo{GoodsId: goods.ID, Num: creation.InitialStock})
		cancel()
		if err != nil {
			zap.S().Warnw("商品等待库存初始化", "goods_id", goods.ID, "code", status.Code(err))
			return creationResponse(goods), nil
		}
		// Only the first confirmation can publish the product. Later retries must
		// not restore its original on_sale value after an administrator edits it.
		err = global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&goods, goods.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return status.Error(codes.FailedPrecondition, "商品已删除")
				}
				return err
			}
			if goods.InventoryReady {
				return nil
			}
			if err := tx.Model(&goods).Updates(map[string]interface{}{"inventory_ready": true, "on_sale": creation.RequestedOnSale}).Error; err != nil {
				return err
			}
			goods.InventoryReady, goods.OnSale = true, creation.RequestedOnSale
			return nil
		})
		if err != nil {
			return nil, goodsCreationError(ctx, err)
		}
	}
	return creationResponse(goods), nil
}

func creationResponse(goods model.Goods) *proto.GoodsInfoResponse {
	response := ModelToResponse(goods)
	return &response
}

func goodsCreationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	zap.S().Errorw("创建商品失败", "error", err)
	return status.Error(codes.Internal, "创建商品失败，请使用原请求编号重试")
}

func (s *GoodsServer) GetGoodsInventory(ctx context.Context, req *proto.GoodInfoRequest) (*proto.GoodsInventoryResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "商品 ID 必须为正整数")
	}
	var goods model.Goods
	if err := global.DB.WithContext(ctx).Select("id", "inventory_ready").First(&goods, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "商品不存在")
		}
		return nil, goodsCreationError(ctx, err)
	}
	response := &proto.GoodsInventoryResponse{GoodsId: goods.ID, Ready: goods.InventoryReady}
	if !goods.InventoryReady {
		return response, nil
	}
	if s.Inventory == nil {
		return nil, status.Error(codes.Unavailable, "库存服务不可用")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stock, err := s.Inventory.InvDetail(callCtx, &invproto.GoodsInventoryInfo{GoodsId: goods.ID})
	if err != nil {
		return nil, err
	}
	response.Stocks = stock.Num
	return response, nil
}
