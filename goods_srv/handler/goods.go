package handler

import (
	"context"
	"errors"

	"fmt"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	invproto "grpctest/inventory_srv/model/proto"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

type GoodsServer struct {
	proto.UnimplementedGoodsServer
	Inventory invproto.InventoryClient
}

// ModelToResponse model转response
func ModelToResponse(goods model.Goods) proto.GoodsInfoResponse {
	return proto.GoodsInfoResponse{
		Id:              goods.ID,
		InventoryReady:  goods.InventoryReady,
		CategoryId:      goods.CategoryID,
		Name:            goods.Name,
		GoodsSn:         goods.GoodsSn,
		ClickNum:        goods.ClickNum,
		SoldNum:         goods.SoldNum,
		FavNum:          goods.FavNum,
		MarketPrice:     goods.MarketPrice,
		ShopPrice:       goods.ShopPrice,
		GoodsBrief:      goods.GoodsBrief,
		ShipFree:        goods.ShipFree,
		GoodsFrontImage: goods.GoodsFrontImage,
		IsNew:           goods.IsNew,
		IsHot:           goods.IsHot,
		OnSale:          goods.OnSale,
		DescImages:      goods.DescImages,
		Images:          goods.Images,
		Category: &proto.CategoryBriefInfoResponse{
			Id:   goods.Category.ID,
			Name: goods.Category.Name,
		},
		Brand: &proto.BrandInfoResponse{
			Id:   goods.Brands.ID,
			Name: goods.Brands.Name,
			Logo: goods.Brands.Logo,
		},
	}
}

// // 商品接口
func (s *GoodsServer) GoodsList(ctx context.Context, req *proto.GoodsFilterRequest) (*proto.GoodsListResponse, error) {
	//关键词搜索、查询新品、查询热门商品、通过价格区间筛选， 通过商品分类筛选
	goodsListResponse := &proto.GoodsListResponse{}

	var goods []model.Goods
	localDB := global.DB.Model(model.Goods{})
	if req.StoreVisibleOnly {
		localDB = localDB.WithContext(ctx).Where("on_sale = ? AND inventory_ready = ?", true, true)
	}
	if req.KeyWords != "" {
		//搜索
		localDB = localDB.Where("name LIKE ?", "%"+req.KeyWords+"%")
	}
	if req.IsHot {
		localDB = localDB.Where(model.Goods{IsHot: true})
	}
	if req.IsNew {
		localDB = localDB.Where(model.Goods{IsNew: true})
	}

	if req.PriceMin > 0 {
		localDB = localDB.Where("shop_price>=?", req.PriceMin)
	}
	if req.PriceMax > 0 {
		localDB = localDB.Where("shop_price<=?", req.PriceMax)
	}

	if req.Brand > 0 {
		localDB = localDB.Where("brands_id=?", req.Brand)
	}

	//通过category去查询商品
	var subQuery string
	if req.TopCategory > 0 {
		var category model.Category
		categoryDB := global.DB
		if req.StoreVisibleOnly {
			categoryDB = categoryDB.WithContext(ctx)
		}
		result := categoryDB.First(&category, req.TopCategory)
		if req.StoreVisibleOnly && result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, storeGoodsListError(ctx, result.Error)
		}
		if result.RowsAffected == 0 {
			return nil, status.Errorf(codes.NotFound, "商品分类不存在")
		}

		if category.Level == 1 {
			subQuery = fmt.Sprintf("select id from category where parent_category_id in (select id from category WHERE parent_category_id=%d)", req.TopCategory)
		} else if category.Level == 2 {
			subQuery = fmt.Sprintf("select id from category WHERE parent_category_id=%d", req.TopCategory)
		} else if category.Level == 3 {
			subQuery = fmt.Sprintf("select id from category WHERE id=%d", req.TopCategory)
		}

		localDB = localDB.Where(fmt.Sprintf("category_id in (%s)", subQuery))
	}

	var count int64
	if result := localDB.Count(&count); req.StoreVisibleOnly && result.Error != nil {
		return nil, storeGoodsListError(ctx, result.Error)
	}
	goodsListResponse.Total = int32(count)

	result := localDB.Preload("Category").Preload("Brands").Scopes(Paginate(int(req.Pages), int(req.PagePerNums))).Find(&goods)
	if result.Error != nil {
		if req.StoreVisibleOnly {
			return nil, storeGoodsListError(ctx, result.Error)
		}
		return nil, result.Error
	}

	for _, good := range goods {
		goodsInfoResponse := ModelToResponse(good)
		goodsListResponse.Data = append(goodsListResponse.Data, &goodsInfoResponse)
	}

	return goodsListResponse, nil
}

func storeGoodsListError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	zap.S().Errorw("查询商品列表失败", "error", err)
	return status.Error(codes.Internal, "查询商品列表失败")
}

// BatchGetGoods 查询多个商品信息
func (s *GoodsServer) BatchGetGoods(ctx context.Context, req *proto.BatchGoodsIdInfo) (*proto.GoodsListResponse, error) {
	if req == nil || len(req.Id) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "一次最多查询 1000 个商品")
	}
	for _, id := range req.Id {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "商品 ID 无效")
		}
	}
	goodsListResponse := &proto.GoodsListResponse{Data: make([]*proto.GoodsInfoResponse, 0)}
	// GORM's Where(emptySlice) removes the predicate and would return all goods.
	if len(req.Id) == 0 {
		return goodsListResponse, nil
	}
	var goods []model.Goods
	result := global.DB.WithContext(ctx).Where("id IN ?", req.Id).Find(&goods)
	if result.Error != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Internal, "批量查询商品失败")
	}
	for _, good := range goods {
		goodsInfoResponse := ModelToResponse(good)
		goodsListResponse.Data = append(goodsListResponse.Data, &goodsInfoResponse)
	}
	goodsListResponse.Total = int32(result.RowsAffected)
	return goodsListResponse, nil
}

// DeleteGoods 删除商品
func (s *GoodsServer) DeleteGoods(ctx context.Context, req *proto.DeleteGoodsInfo) (*emptypb.Empty, error) {
	if result := global.DB.Delete(&model.Goods{}, req.Id); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "商品不存在")
	}
	return &emptypb.Empty{}, nil
}

// GetGoodsDetail 根据id查询商品
func (s *GoodsServer) GetGoodsDetail(ctx context.Context, req *proto.GoodInfoRequest) (*proto.GoodsInfoResponse, error) {
	var goods model.Goods

	err := global.DB.WithContext(ctx).Preload("Category").Preload("Brands").First(&goods, req.Id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "商品不存在")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		zap.S().Errorw("查询商品详情失败", "error", err)
		return nil, status.Error(codes.Internal, "查询商品详情失败")
	}
	goodsInfoResponse := ModelToResponse(goods)
	return &goodsInfoResponse, nil
}
