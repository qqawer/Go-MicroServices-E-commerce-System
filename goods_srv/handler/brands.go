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
	func (g *GoodsServer)BrandList(ctx context.Context, req *proto.BrandFilterRequest) (*proto.BrandListResponse, error){
		
		brandList:=proto.BrandListResponse{}
		brandInfo:=[]*proto.BrandInfoResponse{}
		brands:=[]model.Brands{}

		result:=global.DB.Find(&brands)
		if result.Error != nil {
		return nil, result.Error
	}

	var total int64
	global.DB.Model(&brands).Count(&total)

	global.DB.Scopes(Paginate(int(req.Pages), int(req.PagePerNums))).Find(&brands)
		for _,brand:=range brands{
			brandInfo=append(brandInfo,&proto.BrandInfoResponse{
				Id:brand.ID,
				Name:brand.Name,
				Logo:brand.Logo,
			})
		}
		brandList.Total = int32(total)
		brandList.Data = brandInfo
		return &brandList, nil
	}
	//CreateBrand 新增品牌
func (g *GoodsServer) CreateBrand(ctx context.Context, req *proto.BrandRequest) (*proto.BrandInfoResponse, error) {
	if result := global.DB.Where("name=?", req.Name).First(&model.Brands{}); result.RowsAffected == 1 {
		return nil, status.Errorf(codes.InvalidArgument, "品牌已存在")
	}

	//if res := global.DB.First(&model.Brands{}); res.RowsAffected == 1 {
	//	return nil, status.Errorf(codes.InvalidArgument, "品牌也存在")
	//}

	var brand model.Brands
	brand.Name = req.Name
	brand.Logo = req.Logo

	result := global.DB.Create(&brand)
	if result.Error != nil {
		return nil, status.Errorf(codes.Internal, result.Error.Error())
	}
	BrandResponse := ModelToRsponse(brand)
	return &BrandResponse, nil
}

//DeleteBrand 删除品牌
func (g *GoodsServer) DeleteBrand(ctx context.Context, req *proto.BrandRequest) (*emptypb.Empty, error) {
	if result := global.DB.Delete(&model.Brands{}, req.Id); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "品牌不存在")
	}
	return &emptypb.Empty{}, nil
}

//UpdateBrand 更新品牌
func (g *GoodsServer) UpdateBrand(ctx context.Context, req *proto.BrandRequest) (*emptypb.Empty, error) {
	db := global.DB.WithContext(ctx)
	var brand model.Brands
	if err := db.First(&brand, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Errorf(codes.NotFound, "品牌不存在")
		}
		return nil, catalogDBError(ctx, err, "查询品牌失败")
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
		if err := db.Model(&brand).Select(fields).Updates(&brand).Error; err != nil {
			return nil, catalogDBError(ctx, err, "更新品牌失败")
		}
	}
	return &emptypb.Empty{}, nil
}
