package handler

import (
	"context"
	"errors"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//Paginate 将数据进行分页
func Paginate(page, pageSize int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if page == 0 {
			page = 1
		}

		switch {
		case pageSize > 100:
			pageSize = 100
		case pageSize <= 0:
			pageSize = 10
		}

		offset := (page - 1) * pageSize
		return db.Offset(offset).Limit(pageSize)
	}
}
func ModelToRsponse(brand model.Brands) proto.BrandInfoResponse {
	return proto.BrandInfoResponse{
		Id:   brand.ID,
		Name: brand.Name,
		Logo: brand.Logo,
	}
}
// 品牌

//BrandList 获取品牌列表
func (g *GoodsServer) BrandList(ctx context.Context, req *proto.BrandFilterRequest) (*proto.BrandListResponse, error) {
	db := global.DB.WithContext(ctx)
	var total int64
	if err := db.Model(&model.Brands{}).Count(&total).Error; err != nil {
		return nil, catalogDBError(ctx, err, "查询品牌失败")
	}
	var brands []model.Brands
	if err := db.Order("id").Scopes(Paginate(int(req.Pages), int(req.PagePerNums))).Find(&brands).Error; err != nil {
		return nil, catalogDBError(ctx, err, "查询品牌失败")
	}
	brandInfo := make([]*proto.BrandInfoResponse, 0, len(brands))
	for _, brand := range brands {
		brandInfo = append(brandInfo, &proto.BrandInfoResponse{Id: brand.ID, Name: brand.Name, Logo: brand.Logo})
	}
	return &proto.BrandListResponse{Total: int32(total), Data: brandInfo}, nil
}

//CreateBrand 新增品牌
func (g *GoodsServer) CreateBrand(ctx context.Context, req *proto.BrandRequest) (*proto.BrandInfoResponse, error) {
	db := global.DB.WithContext(ctx)
	err := db.Where("name=?", req.Name).First(&model.Brands{}).Error
	if err == nil {
		return nil, status.Error(codes.AlreadyExists, "品牌名称已存在")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, catalogDBError(ctx, err, "查询品牌失败")
	}

	//if res := global.DB.First(&model.Brands{}); res.RowsAffected == 1 {
	//	return nil, status.Errorf(codes.InvalidArgument, "品牌也存在")
	//}

	var brand model.Brands
	brand.Name = req.Name
	brand.Logo = req.Logo

	result := db.Create(&brand)
	if result.Error != nil {
		return nil, brandDBError(ctx, result.Error, "创建品牌失败")
	}
	BrandResponse := ModelToRsponse(brand)
	return &BrandResponse, nil
}

//DeleteBrand 删除品牌
func (g *GoodsServer) DeleteBrand(ctx context.Context, req *proto.BrandRequest) (*emptypb.Empty, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "品牌 ID 必须为正整数")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&model.Brands{}, req.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.NotFound, "品牌不存在")
			}
			return err
		}
		if err := rejectCatalogReferences(tx.Model(&model.Goods{}).Where("brands_id = ?", req.Id), "品牌仍被商品使用，请先迁移或删除商品"); err != nil {
			return err
		}
		if err := rejectCatalogReferences(tx.Model(&model.GoodsCategoryBrand{}).Where("brands_id = ?", req.Id), "品牌仍有关联分类，请先解除关联"); err != nil {
			return err
		}
		return tx.Delete(&model.Brands{}, req.Id).Error
	})
	if err != nil {
		return nil, catalogDBError(ctx, err, "删除品牌结果未确认，请查询后重试")
	}
	return &emptypb.Empty{}, nil
}

//UpdateBrand 更新品牌
func (g *GoodsServer) UpdateBrand(ctx context.Context, req *proto.BrandRequest) (*emptypb.Empty, error) {
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var brand model.Brands
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&brand, req.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Errorf(codes.NotFound, "品牌不存在")
			}
			return err
		}

		var fields []string
		if req.Name != "" {
			brand.Name = req.Name
			fields = append(fields, "name")
		}

		if req.Logo != "" {
			brand.Logo = req.Logo
			fields = append(fields, "logo")
		}

		if len(fields) > 0 {
			return tx.Model(&brand).Select(fields).Updates(&brand).Error
		}
		return nil
	})
	if err != nil {
		return nil, brandDBError(ctx, err, "更新品牌失败")
	}
	return &emptypb.Empty{}, nil
}

func brandDBError(ctx context.Context, err error, message string) error {
	var mysqlError *mysql.MySQLError
	if ctx.Err() == nil && errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return status.Error(codes.AlreadyExists, "品牌名称已存在")
	}
	return catalogDBError(ctx, err, message)
}
