package main

import (
	"context"
	"fmt"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var brandClient proto.GoodsClient
var conn *grpc.ClientConn


func Init() {
	var err error
	conn, err = grpc.NewClient(
		"127.0.0.1:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil{
		panic(err)
	}
	// defer conn.Close()

	brandClient = proto.NewGoodsClient(conn)
	
}
func TestGetBrandList(){
	rsp,err:=brandClient.BrandList(context.Background(),&proto.BrandFilterRequest{
		Pages:1,
		PagePerNums:5,
	})
	if err!= nil{
		panic(err)
	}
	fmt.Println(rsp.Total)
	for _,brand :=range rsp.Data{
		fmt.Println(brand.Name)
	}                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      
}


func main() {
	Init()

	TestGetBrandList()
	conn.Close()
}