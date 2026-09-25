package handler

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	goodsproto "grpctest/goods_srv/model/proto"
	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FavoritesServer keeps ownership in the user database and reads live product
// information through the existing goods service. No product counter is written.
type FavoritesServer struct {
	proto.UnimplementedFavoritesServer
	Goods goodsproto.GoodsClient
}

func favoriteDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "用户不存在")
	}
	return status.Error(codes.Internal, "收藏操作失败")
}

func validFavoriteKey(req *proto.FavoriteKey) error {
	if req == nil || req.UserId <= 0 || req.GoodsId <= 0 {
		return status.Error(codes.InvalidArgument, "用户或商品 ID 无效")
	}
	return nil
}

func favoriteGoodsError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if status.Code(err) == codes.DeadlineExceeded {
		return status.Error(codes.DeadlineExceeded, "查询商品超时")
	}
	return status.Error(codes.Unavailable, "商品服务暂不可用")
}

func (s *FavoritesServer) GetFavoriteStatus(ctx context.Context, req *proto.FavoriteKey) (*proto.FavoriteState, error) {
	if err := validFavoriteKey(req); err != nil {
		return nil, err
	}
	var favorite model.Favorite
	err := global.DB.WithContext(ctx).Select("id").Where("user_id = ? AND goods_id = ?", req.UserId, req.GoodsId).Take(&favorite).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, favoriteDBError(ctx, err)
	}
	return &proto.FavoriteState{UserId: req.UserId, GoodsId: req.GoodsId, Favorited: err == nil}, nil
}

func (s *FavoritesServer) AddFavorite(ctx context.Context, req *proto.FavoriteKey) (*proto.FavoriteState, error) {
	state, err := s.GetFavoriteStatus(ctx, req)
	if err != nil || state.Favorited {
		return state, err
	}
	if s.Goods == nil {
		return nil, status.Error(codes.Unavailable, "商品服务暂不可用")
	}
	goodsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	goods, err := s.Goods.GetGoodsDetail(goodsCtx, &goodsproto.GoodInfoRequest{Id: req.GoodsId})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		if status.Code(err) == codes.NotFound {
			return nil, status.Error(codes.NotFound, "商品不存在")
		}
		return nil, favoriteGoodsError(ctx, err)
	}
	if goods == nil || goods.Id != req.GoodsId {
		return nil, status.Error(codes.Internal, "商品服务返回无效数据")
	}
	err = global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&owner, req.UserId).Error; err != nil {
			return err
		}
		// The database's unique (user_id, goods_id) key resolves concurrent adds.
		// A duplicate must keep its original creation time and ordering.
		favorite := model.Favorite{UserID: req.UserId, GoodsID: req.GoodsId}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&favorite).Error
	})
	if err != nil {
		return nil, favoriteDBError(ctx, err)
	}
	state.Favorited = true
	return state, nil
}

func (s *FavoritesServer) RemoveFavorite(ctx context.Context, req *proto.FavoriteKey) (*emptypb.Empty, error) {
	if err := validFavoriteKey(req); err != nil {
		return nil, err
	}
	if err := global.DB.WithContext(ctx).Where("user_id = ? AND goods_id = ?", req.UserId, req.GoodsId).Delete(&model.Favorite{}).Error; err != nil {
		return nil, favoriteDBError(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

func (s *FavoritesServer) ListFavorites(ctx context.Context, req *proto.FavoriteListRequest) (*proto.FavoriteListResponse, error) {
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
	var favorites []model.Favorite
	var total int64
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Favorite{}).Where("user_id = ?", req.UserId).Count(&total).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", req.UserId).Order("id DESC").Offset(int(offset)).Limit(int(size)).Find(&favorites).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, favoriteDBError(ctx, err)
	}
	if total > math.MaxInt32 {
		return nil, status.Error(codes.ResourceExhausted, "收藏数量超出支持范围")
	}
	response := &proto.FavoriteListResponse{Total: int32(total), Data: make([]*proto.Favorite, 0, len(favorites))}
	if len(favorites) == 0 {
		return response, nil
	}
	if s.Goods == nil {
		return nil, status.Error(codes.Unavailable, "商品服务暂不可用")
	}
	ids := make([]int32, 0, len(favorites))
	wanted := make(map[int32]bool, len(favorites))
	for _, favorite := range favorites {
		if !wanted[favorite.GoodsID] {
			wanted[favorite.GoodsID] = true
			ids = append(ids, favorite.GoodsID)
		}
	}
	// Never keep a database transaction open while waiting for another service.
	goodsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	batch, err := s.Goods.BatchGetGoods(goodsCtx, &goodsproto.BatchGoodsIdInfo{Id: ids})
	if err != nil {
		return nil, favoriteGoodsError(ctx, err)
	}
	if batch == nil {
		return nil, status.Error(codes.Internal, "商品服务返回无效数据")
	}
	byID := make(map[int32]*proto.FavoriteGoods, len(batch.Data))
	for _, goods := range batch.Data {
		if goods == nil || goods.Id <= 0 || !wanted[goods.Id] || byID[goods.Id] != nil {
			return nil, status.Error(codes.Internal, "商品服务返回无效数据")
		}
		byID[goods.Id] = &proto.FavoriteGoods{Id: goods.Id, Name: goods.Name, ShopPrice: goods.ShopPrice,
			GoodsFrontImage: goods.GoodsFrontImage, OnSale: goods.OnSale}
	}
	for _, favorite := range favorites {
		goods := byID[favorite.GoodsID]
		response.Data = append(response.Data, &proto.Favorite{UserId: favorite.UserID, GoodsId: favorite.GoodsID,
			CreatedAt: favorite.CreatedAt.Unix(), Goods: goods, GoodsMissing: goods == nil})
	}
	return response, nil
}
