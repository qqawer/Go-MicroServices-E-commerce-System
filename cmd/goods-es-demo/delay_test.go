package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/goods_srv/model/proto"
)

func TestLabDelayRespectsCancellationAndDoesNotDelayOtherMethods(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delay-ms")
	if err := os.WriteFile(path, []byte("4000"), 0600); err != nil {
		t.Fatal(err)
	}
	interceptor := labQueryDelay(path)
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := interceptor(ctx, &proto.GoodsFilterRequest{StoreVisibleOnly: true}, &grpc.UnaryServerInfo{FullMethod: "/Goods/GoodsList"}, handler)
	if status.Code(err) != codes.Canceled || called {
		t.Fatal("delay ignored cancellation")
	}
	start := time.Now()
	_, err = interceptor(context.Background(), &proto.GoodsFilterRequest{}, &grpc.UnaryServerInfo{FullMethod: "/Goods/BatchGetGoods"}, handler)
	if err != nil || !called || time.Since(start) > time.Second {
		t.Fatal("lab delay affected another method")
	}
	if err := os.WriteFile(path, []byte("5001"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = interceptor(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true}, &grpc.UnaryServerInfo{FullMethod: "/Goods/GoodsList"}, handler)
	if status.Code(err) != codes.Internal {
		t.Fatal("unbounded lab delay accepted")
	}
}
