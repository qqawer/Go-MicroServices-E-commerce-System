package handler

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/model"
	"grpctest/inventory_srv/model/proto"
)

// InitInv is retryable initialization, not an absolute stock adjustment.
func (i *InventoryServer) InitInv(ctx context.Context, req *proto.GoodsInventoryInfo) (*proto.GoodsInventoryInfo, error) {
	if req.GetGoodsId() <= 0 || req.GetNum() < 0 {
		return nil, status.Error(codes.InvalidArgument, "商品 ID 必须为正整数，库存不能为负数")
	}
	var inventory model.Inventory
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initial := model.InventoryInitialization{Goods: req.GoodsId, InitialStock: req.Num}
		result := tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"goods"})}).Create(&initial)
		if result.Error != nil {
			return result.Error
		}
		inserted := result.RowsAffected == 1
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&initial, req.GoodsId).Error; err != nil {
			return err
		}
		if initial.InitialStock != req.Num {
			return status.Error(codes.AlreadyExists, "该商品已使用不同数量初始化库存")
		}
		if inserted {
			inventory = model.Inventory{Goods: req.GoodsId, Stocks: req.Num}
			return tx.Create(&inventory).Error
		}
		// The metadata and stock were committed together. Never recreate missing stock on retry.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("goods = ?", req.GoodsId).First(&inventory).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.FailedPrecondition, "库存记录缺失，需要人工核对")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, inventoryDBError(ctx, err)
	}
	return &proto.GoodsInventoryInfo{GoodsId: inventory.Goods, Num: inventory.Stocks}, nil
}

// SetInv is an explicit administrator adjustment. Creation retries must use InitInv.
func (i *InventoryServer) SetInv(ctx context.Context, req *proto.GoodsInventoryInfo) (*emptypb.Empty, error) {
	if req.GetGoodsId() <= 0 || req.GetNum() < 0 {
		return nil, status.Error(codes.InvalidArgument, "商品 ID 必须为正整数，库存不能为负数")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inventory := model.Inventory{Goods: req.GoodsId, Stocks: req.Num}
		if err := tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]interface{}{
			"stocks": req.Num, "version": gorm.Expr("COALESCE(version, 0) + 1"), "update_time": gorm.Expr("NOW(3)"),
		})}).Create(&inventory).Error; err != nil {
			return err
		}
		if err := tx.Where("goods = ?", req.GoodsId).First(&inventory).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.FailedPrecondition, "库存已删除，不能直接覆盖")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, inventoryDBError(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

func (i *InventoryServer) InvDetail(ctx context.Context, req *proto.GoodsInventoryInfo) (*proto.GoodsInventoryInfo, error) {
	if req.GetGoodsId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "商品 ID 必须为正整数")
	}
	var inventory model.Inventory
	if err := global.DB.WithContext(ctx).Where("goods = ?", req.GoodsId).First(&inventory).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "库存信息不存在")
		}
		return nil, inventoryDBError(ctx, err)
	}
	return &proto.GoodsInventoryInfo{GoodsId: inventory.Goods, Num: inventory.Stocks}, nil
}

func inventoryDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	var duplicate *mysql.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		return status.Error(codes.AlreadyExists, "商品库存已存在")
	}
	zap.S().Errorw("库存数据库操作失败", "error", err)
	return status.Error(codes.Internal, "库存数据库操作失败")
}
