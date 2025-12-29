// init.go
package main

import (
	"grpctest/goods_srv/model/proto"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// 全局变量：所有文件共享（main.go、brand_test.go 可直接访问）
var (
	BrandClient proto.GoodsClient
	Conn        *grpc.ClientConn
)

// InitGRPC 初始化 gRPC 连接（对外暴露的初始化函数）
func InitGRPC() {
	var err error
	Conn, err = grpc.NewClient(
		"127.0.0.1:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("[InitGRPC] 连接失败：%v", err)
	}
	BrandClient = proto.NewGoodsClient(Conn)
	log.Println("[InitGRPC] gRPC 客户端初始化成功")
}

// CloseGRPC 关闭 gRPC 连接（优雅退出用）
func CloseGRPC() {
	if Conn != nil {
		if err := Conn.Close(); err != nil {
			log.Printf("[CloseGRPC] 关闭连接失败：%v", err)
		} else {
			log.Println("[CloseGRPC] gRPC 连接已关闭")
		}
	}
}