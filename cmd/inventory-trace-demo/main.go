// inventory-trace-demo runs the real inventory RPC with its own synthetic stock.
package main

import (
	"context"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"grpctest/internal/tracing"
	"grpctest/inventory_srv/config"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
	"grpctest/inventory_srv/initialize"
	"grpctest/inventory_srv/model"
	"grpctest/inventory_srv/model/proto"
	"log"
	"net"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	shutdown, err := tracing.Init("inventory-srv")
	if err != nil {
		return err
	}
	defer shutdown()
	global.ServerConfig = &config.ServerConfig{MysqlInfo: config.MysqlConfig{Host: "mysql", Port: "3306", Name: "trace_inventory_demo", User: "root"}}
	closeDB, err := initialize.InitDB()
	if err != nil {
		return err
	}
	defer closeDB()
	if err := global.DB.AutoMigrate(&model.Inventory{}, &model.StockSellDetail{}, &model.InventoryInitialization{}); err != nil {
		return err
	}
	if !global.DB.Migrator().HasIndex("inventory", "uq_trace_goods") {
		if err := global.DB.Exec("CREATE UNIQUE INDEX uq_trace_goods ON inventory(goods)").Error; err != nil {
			return err
		}
	}
	var count int64
	if err := global.DB.Model(&model.Inventory{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := global.DB.Create(&model.Inventory{Goods: 1, Stocks: 100}).Error; err != nil {
			return err
		}
	}
	lis, err := net.Listen("tcp", ":50053")
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	proto.RegisterInventoryServer(server, &handler.InventoryServer{})
	go func() {
		if err := server.Serve(lis); err != nil {
			log.Print(err)
		}
	}()
	log.Print("isolated inventory tracing RPC ready: trace_inventory_demo")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	server.GracefulStop()
	return nil
}
