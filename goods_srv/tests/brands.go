package main

import (
	"context"
	"fmt"
	"grpctest/goods_srv/model/proto"
	"log"
)



func TestGetBrandList() {
	c := context.Background()
	Rsp, err := BrandClient.BrandList(c, &proto.BrandFilterRequest{
		Pages:       1,
		PagePerNums: 3,
	})
	if err != nil {
		log.Fatal("获取用户列表失败", err)
	}
	for _, value := range Rsp.Data {
		fmt.Println("品牌：", value.Name)
	}
	fmt.Println("总数:", Rsp.Total)
}

func TestCreateBrand() {
	c := context.Background()
	res, err := BrandClient.CreateBrand(c, &proto.BrandRequest{
		Name: "银蕨农场",
		Logo: "https://img30.360buyimg.com/popshop/jfs/t6739/299/1630614351/29051/a47f9456/5954983fN2f665b13.jpg",
	})
	if err != nil {
		log.Fatal("新增商品失败", err)
	}
	fmt.Println("商品：", res)

}

func TestDeleteBrand() {
	c := context.Background()
	_, err := BrandClient.DeleteBrand(c, &proto.BrandRequest{
		Id: 1087,
	})
	if err != nil {
		log.Fatal("删除失败", err.Error())
	}
}

func TestUpdateBrand() {
	c := context.Background()
	_, err := BrandClient.UpdateBrand(c, &proto.BrandRequest{
		Id:   1086,
		Name: "烟台苹果",
		Logo: "https://img30.360buyimg.com",
	})
	if err != nil {
		log.Fatal("更新失败", err)
	}
}
