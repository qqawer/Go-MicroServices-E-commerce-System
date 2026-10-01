package handler

import (
	"context"
	"errors"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//BannerList 获取轮播图列表
func (g *GoodsServer) BannerList(ctx context.Context, req *emptypb.Empty) (*proto.BannerListResponse, error) {
	bannerListResponse := proto.BannerListResponse{}

	var banners []model.Banner
	result := global.DB.WithContext(ctx).Order(clause.OrderBy{Columns: []clause.OrderByColumn{
		{Column: clause.Column{Name: "index"}},
		{Column: clause.Column{Name: "id"}},
	}}).Find(&banners)
	if result.Error != nil {
		return nil, catalogDBError(ctx, result.Error, "查询轮播图失败")
	}
	bannerListResponse.Total = int32(result.RowsAffected)

	var bannerReponses []*proto.BannerResponse
	for _, banner := range banners {
		bannerReponses = append(bannerReponses, &proto.BannerResponse{
			Id:    banner.ID,
			Image: banner.Image,
			Index: banner.Index,
			Url:   banner.Url,
		})
	}

	bannerListResponse.Data = bannerReponses

	return &bannerListResponse, nil
}

//CreateBanner 添加轮播图
func (g *GoodsServer) CreateBanner(ctx context.Context, req *proto.BannerRequest) (*proto.BannerResponse, error) {
	db := global.DB.WithContext(ctx)
	if req.Id != 0 {
		err := db.First(&model.Banner{}, req.Id).Error
		if err == nil {
			return nil, status.Errorf(codes.InvalidArgument, "轮播图已存在")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, catalogDBError(ctx, err, "查询轮播图失败")
		}
	}
	var Banner model.Banner
	Banner.ID = req.Id
	Banner.Index = req.Index
	Banner.Image = req.Image
	Banner.Url = req.Url
	if result := db.Create(&Banner); result.Error != nil {
		return nil, catalogDBError(ctx, result.Error, "创建轮播图失败")
	}
	return &proto.BannerResponse{
		Id:    Banner.ID,
		Index: Banner.Index,
		Image: Banner.Image,
		Url:   Banner.Url,
	}, nil
}

//DeleteBanner 删除轮播图
func (g *GoodsServer) DeleteBanner(ctx context.Context, req *proto.BannerRequest) (*emptypb.Empty, error) {
	result := global.DB.WithContext(ctx).Delete(&model.Banner{}, req.Id)
	if result.Error != nil {
		return nil, catalogDBError(ctx, result.Error, "删除轮播图结果未确认，请查询后重试")
	}
	if result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "轮播图不存在")
	}
	return &emptypb.Empty{}, nil
}

//UpdateBanner 更新轮播图
func (g *GoodsServer) UpdateBanner(ctx context.Context, req *proto.BannerRequest) (*emptypb.Empty, error) {
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var banner model.Banner
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&banner, req.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Errorf(codes.NotFound, "轮播图不存在")
			}
			return err
		}
		var fields []string
		if req.Image != "" {
			banner.Image = req.Image
			fields = append(fields, "image")
		}
		if req.Index >= 0 {
			banner.Index = req.Index
			fields = append(fields, "index")
		}
		if req.Url != "" {
			banner.Url = req.Url
			fields = append(fields, "url")
		}
		if len(fields) > 0 {
			return tx.Model(&banner).Select(fields).Updates(&banner).Error
		}
		return nil
	})
	if err != nil {
		return nil, catalogDBError(ctx, err, "更新轮播图失败")
	}
	return &emptypb.Empty{}, nil
}
