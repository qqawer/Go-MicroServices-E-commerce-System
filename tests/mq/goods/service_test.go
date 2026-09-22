//go:build integration

package goods_test

import (
	"context"
	"google.golang.org/grpc"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"
	"net"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

func TestMQService(t *testing.T) {
	global.DB = testdb.Open(t, "grpctest_goods_srv")
	listener, e := net.Listen("tcp", os.Getenv("TEST_RPC_ADDRESS"))
	if e != nil {
		t.Fatal(e)
	}
	server := grpc.NewServer()
	proto.RegisterGoodsServer(server, &handler.GoodsServer{})
	go server.Serve(listener)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-ctx.Done()
	server.GracefulStop()
}
