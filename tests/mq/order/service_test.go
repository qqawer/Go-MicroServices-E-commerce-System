//go:build integration

package order_test

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"grpctest/internal/mqruntime"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/initialize"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
)

func TestMQService(t *testing.T) {
	global.DB = testdb.Open(t, "grpctest_orders_srv")
	port, _ := strconv.Atoi(os.Getenv("TEST_MQ_PORT"))
	global.ServerConfig = &config.ServerConfig{MqInfo: mqruntime.Config{Enabled: true, Host: "127.0.0.1", Port: port, Mode: "demo", OrderTTLSeconds: 30}}
	goods, e := grpc.NewClient(os.Getenv("TEST_GOODS_ADDRESS"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	defer goods.Close()
	inventory, e := grpc.NewClient(os.Getenv("TEST_INVENTORY_ADDRESS"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	defer inventory.Close()
	global.GoodsSrvClient = proto.NewGoodsClient(goods)
	global.InventorySrvClient = proto.NewInventoryClient(inventory)
	closeMQ, e := initialize.InitMQ(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer closeMQ()
	listener, e := net.Listen("tcp", os.Getenv("TEST_RPC_ADDRESS"))
	if e != nil {
		t.Fatal(e)
	}
	server := grpc.NewServer()
	proto.RegisterOrderServer(server, &handler.OrderServer{})
	go server.Serve(listener)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-ctx.Done()
	server.GracefulStop()
}
