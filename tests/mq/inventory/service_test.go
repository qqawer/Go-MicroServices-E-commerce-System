//go:build integration

package inventory_test

import (
	"context"
	"google.golang.org/grpc"
	"grpctest/internal/mqruntime"
	"grpctest/inventory_srv/config"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
	"grpctest/inventory_srv/initialize"
	"grpctest/inventory_srv/model/proto"
	"grpctest/tests/integration/testdb"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
)

func TestMQService(t *testing.T) {
	global.DB = testdb.Open(t, "grpctest_inventory_srv")
	port, _ := strconv.Atoi(os.Getenv("TEST_MQ_PORT"))
	global.ServerConfig = &config.ServerConfig{MqInfo: mqruntime.Config{Enabled: true, Host: "127.0.0.1", Port: port}}
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
	proto.RegisterInventoryServer(server, &handler.InventoryServer{})
	go server.Serve(listener)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-ctx.Done()
	server.GracefulStop()
}
