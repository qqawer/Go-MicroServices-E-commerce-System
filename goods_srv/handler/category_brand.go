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

// 品牌分类
// CategoryBrandList 品牌分类列表
func (g *GoodsServer) CategoryBrandList(c context.Context, req *proto.CategoryBrandFilterRequest) (*proto.CategoryBrandListResponse, error) {
	var categoryBrands []model.GoodsCategoryBrand
	var categoryBrandListResponse proto.CategoryBrandListResponse

	db := global.DB.WithContext(c)
	var total int64
	if err := db.Model(&model.GoodsCategoryBrand{}).Count(&total).Error; err != nil {
		return nil, categoryDBError(c, err, "查询分类品牌关联失败")
	}
	categoryBrandListResponse.Total = int32(total)
	if err := db.Preload("Category").Preload("Brands").Order("id").Scopes(Paginate(int(req.Pages), int(req.PagePerNums))).Find(&categoryBrands).Error; err != nil {
		return nil, categoryDBError(c, err, "查询分类品牌关联失败")
	}

	var CategoryBrandResponse []*proto.CategoryBrandResponse
	for _, categoryBrand := range categoryBrands {
		CategoryBrandResponse = append(CategoryBrandResponse, &proto.CategoryBrandResponse{
			Id: categoryBrand.ID,
			Brand: &proto.BrandInfoResponse{
				Id:   categoryBrand.Brands.ID,
				Name: categoryBrand.Brands.Name,
				Logo: categoryBrand.Brands.Logo,
			},
			Category: &proto.CategoryInfoResponse{
				Id:             categoryBrand.Category.ID,
				Name:           categoryBrand.Category.Name,
				Level:          categoryBrand.Category.Level,
				IsTab:          categoryBrand.Category.IsTab,
				ParentCategory: categoryBrand.Category.ParentCategoryID,
			},
		})
	}
	categoryBrandListResponse.Data = CategoryBrandResponse

	return &categoryBrandListResponse, nil
}

// GetCategoryBrandList 根据分类获品牌信息
func (g *GoodsServer) GetCategoryBrandList(c context.Context, req *proto.CategoryInfoRequest) (*proto.BrandListResponse, error) {
	var category model.Category
	var brandListResponse proto.BrandListResponse

	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "分类 ID 必须为正整数")
	}
	db := global.DB.WithContext(c)
	if err := db.Select("id").First(&category, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.InvalidArgument, "商品分类不存在")
		}
		return nil, categoryDBError(c, err, "查询分类失败")
	}
	var categoryBrands []model.GoodsCategoryBrand
	if err := db.Preload("Brands").Where("category_id = ?", req.Id).Order("id").Find(&categoryBrands).Error; err != nil {
		return nil, categoryDBError(c, err, "查询分类品牌关联失败")
	}
	brandListResponse.Total = int32(len(categoryBrands))

	var brandInfoResponse []*proto.BrandInfoResponse
	for _, categoryBrand := range categoryBrands {
		brandInfoResponse = append(brandInfoResponse, &proto.BrandInfoResponse{
			Id:   categoryBrand.Brands.ID,
			Name: categoryBrand.Brands.Name,
			Logo: categoryBrand.Brands.Logo,
		})
	}
	brandListResponse.Data = brandInfoResponse

	return &brandListResponse, nil
}

// CreateCategoryBrand creates one relation; the database unique key rejects duplicates.
func (g *GoodsServer) CreateCategoryBrand(ctx context.Context, req *proto.CategoryBrandRequest) (*proto.CategoryBrandResponse, error) {
	if req.GetCategoryId() <= 0 || req.GetBrandId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "分类和品牌 ID 必须为正整数")
	}
	relation := model.GoodsCategoryBrand{CategoryID: req.CategoryId, BrandsID: req.BrandId}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateCategoryBrand(tx, req.CategoryId, req.BrandId); err != nil {
			return err
		}
		return tx.Omit(clause.Associations).Create(&relation).Error
	})
	if err != nil {
		return nil, categoryDBError(ctx, err, "创建分类品牌关联失败")
	}
	return &proto.CategoryBrandResponse{Id: relation.ID}, nil
}

// DeleteCategoryBrand uses the relation ID returned by CategoryBrandList.
func (g *GoodsServer) DeleteCategoryBrand(ctx context.Context, req *proto.CategoryBrandRequest) (*emptypb.Empty, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "关联 ID 必须为正整数")
	}
	result := global.DB.WithContext(ctx).Delete(&model.GoodsCategoryBrand{}, req.Id)
	if result.Error != nil {
		return nil, categoryDBError(ctx, result.Error, "删除分类品牌关联失败")
	}
	if result.RowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "分类品牌关联不存在")
	}
	return &emptypb.Empty{}, nil
}

// UpdateCategoryBrand changes only the selected relation and never inserts a missing row.
func (g *GoodsServer) UpdateCategoryBrand(ctx context.Context, req *proto.CategoryBrandRequest) (*emptypb.Empty, error) {
	if req.GetId() <= 0 || req.GetCategoryId() <= 0 || req.GetBrandId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "关联、分类和品牌 ID 必须为正整数")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var relation model.GoodsCategoryBrand
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&relation, req.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.NotFound, "分类品牌关联不存在")
			}
			return err
		}
		if err := validateCategoryBrand(tx, req.CategoryId, req.BrandId); err != nil {
			return err
		}
		return tx.Model(&model.GoodsCategoryBrand{}).Where("id = ?", req.Id).Updates(map[string]interface{}{
			"category_id": req.CategoryId, "brands_id": req.BrandId,
		}).Error
	})
	if err != nil {
		return nil, categoryDBError(ctx, err, "更新分类品牌关联失败")
	}
	return &emptypb.Empty{}, nil
}

func validateCategoryBrand(db *gorm.DB, categoryID, brandID int32) error {
	if err := db.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&model.Category{}, categoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Error(codes.InvalidArgument, "商品分类不存在")
		}
		return err
	}
	if err := db.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&model.Brands{}, brandID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Error(codes.InvalidArgument, "品牌不存在")
		}
		return err
	}
	return nil
}
