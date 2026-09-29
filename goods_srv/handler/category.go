package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/golang/protobuf/ptypes/empty"
)

// GetAllCategorysList 获取所有分类
func (g *GoodsServer) GetAllCategorysList(ctx context.Context, _ *emptypb.Empty) (*proto.CategoryListResponse, error) {
	var categorys []model.Category
	//通过反向查询，查一级内目，查二级内目，查三级内目
	if err := global.DB.WithContext(ctx).Where(&model.Category{Level: 1}).Preload("SubCategory.SubCategory").Find(&categorys).Error; err != nil {
		return nil, catalogDBError(ctx, err, "查询分类失败")
	}

	//json序列化
	b, _ := json.Marshal(&categorys)

	return &proto.CategoryListResponse{JsonData: string(b)}, nil
}

// GetSubCategory 获取子分类
func (g *GoodsServer) GetSubCategory(c context.Context, req *proto.CategoryListRequest) (*proto.SubCategoryListResponse, error) {
	categoryListResponse := proto.SubCategoryListResponse{}

	var category model.Category
	//查询分类是否存在
	//查询当前目录
	if result := global.DB.Find(&category, req.Id); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "分类不存在")
	}
	categoryListResponse.Info = &proto.CategoryInfoResponse{
		Id:             category.ID,
		Name:           category.Name,
		Level:          category.Level,
		IsTab:          category.IsTab,
		ParentCategory: category.ParentCategoryID,
	}

	//当前目录子目录
	var subCategorys []model.Category
	var categoryInfoResponse []*proto.CategoryInfoResponse
	perloads := "SubCategory"
	if category.Level == 1 {
		perloads = "SubCategory.SubCategory"
	}
	global.DB.Where(&model.Category{ParentCategoryID: req.Id}).Preload(perloads).Find(&subCategorys)
	for _, subCategory := range subCategorys {
		categoryInfoResponse = append(categoryInfoResponse, &proto.CategoryInfoResponse{
			Id:             subCategory.ID,
			Name:           subCategory.Name,
			Level:          subCategory.Level,
			IsTab:          subCategory.IsTab,
			ParentCategory: subCategory.ParentCategoryID,
		})
	}
	categoryListResponse.SubCategorys = categoryInfoResponse
	return &categoryListResponse, nil
}

// CreateCategory 创建分类
func (s *GoodsServer) CreateCategory(ctx context.Context, req *proto.CategoryInfoRequest) (*proto.CategoryInfoResponse, error) {
	if strings.TrimSpace(req.GetName()) == "" || utf8.RuneCountInString(req.GetName()) > 20 || req.GetLevel() < 1 || req.GetLevel() > 3 {
		return nil, status.Error(codes.InvalidArgument, "分类名称须为 1–20 字，层级须为 1–3")
	}
	if (req.Level == 1 && req.ParentCategory != 0) || (req.Level > 1 && req.ParentCategory <= 0) {
		return nil, status.Error(codes.InvalidArgument, "一级分类不能设置父级，子分类必须指定父级")
	}
	db := global.DB.WithContext(ctx)
	if req.Level > 1 {
		var parent model.Category
		if err := db.Select("id", "level").First(&parent, req.ParentCategory).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, status.Error(codes.InvalidArgument, "父分类不存在")
			}
			return nil, categoryDBError(ctx, err, "创建分类失败")
		}
		if parent.Level != req.Level-1 {
			return nil, status.Error(codes.InvalidArgument, "父分类层级不匹配")
		}
	}
	category := model.Category{
		Name: req.Name, Level: req.Level, ParentCategoryID: req.ParentCategory, IsTab: req.IsTab,
	}
	omit := []string{clause.Associations}
	if req.Level == 1 {
		// Root categories use SQL NULL, not a foreign key pointing to ID 0.
		omit = append(omit, "ParentCategoryID")
		category.ParentCategoryID = 0
	}
	if err := db.Omit(omit...).Create(&category).Error; err != nil {
		return nil, categoryDBError(ctx, err, "创建分类失败")
	}
	return &proto.CategoryInfoResponse{
		Id: category.ID, Name: category.Name, Level: category.Level,
		ParentCategory: category.ParentCategoryID, IsTab: category.IsTab,
	}, nil
}

// DeleteCategory 删除分类
func (g *GoodsServer) DeleteCategory(c context.Context, req *proto.DeleteCategoryRequest) (*empty.Empty, error) {
	if result := global.DB.Delete(&model.Category{}, req.Id); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "商品分类不存在")
	}
	return &empty.Empty{}, nil
}

// UpdateCategory 更新分类
func (g *GoodsServer) UpdateCategory(c context.Context, req *proto.CategoryInfoRequest) (*empty.Empty, error) {
	db := global.DB.WithContext(c)
	var category model.Category
	if err := db.First(&category, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Errorf(codes.NotFound, "商品分类不存在")
		}
		return nil, catalogDBError(c, err, "查询分类失败")
	}
	fields := []string{"is_tab"}
	if req.Name != "" {
		category.Name = req.Name
		fields = append(fields, "name")
	}

	if req.ParentCategory != 0 {
		//类目级别
		category.ParentCategoryID = req.ParentCategory
		fields = append(fields, "parent_category_id")
	}
	if req.Level != 0 {
		category.Level = req.Level
		fields = append(fields, "level")
	}
	// The update form requires is_tab; false is an explicit value.
	category.IsTab = req.IsTab

	// Only submitted columns are written, preserving a root's NULL parent.
	if err := db.Model(&category).Select(fields).Updates(&category).Error; err != nil {
		return nil, categoryDBError(c, err, "更新分类失败")
	}

	return &empty.Empty{}, nil
}
