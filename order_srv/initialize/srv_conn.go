package initialize

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/proto"

	_ "github.com/mbobakov/grpc-consul-resolver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// InitSrvConn 建立可复用的下游连接；返回的清理函数应在停止接收 RPC 后调用。
func InitSrvConn() (cleanup func(), err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return initSrvConn(ctx, global.ServerConfig, grpc.DialContext)
}

func initSrvConn(ctx context.Context, cfg *config.ServerConfig, dial func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error)) (func(), error) {
	if err := validateServerConfig(cfg); err != nil {
		return nil, err
	}
	connect := func(service string) (*grpc.ClientConn, error) {
		target := url.URL{
			Scheme: "consul",
			Host:   net.JoinHostPort(cfg.ConsulInfo.Host, strconv.Itoa(cfg.ConsulInfo.Port)),
			Path:   "/" + service,
			// resolver 自身的 HTTP 请求也设期限，避免关闭连接后无限等待 Consul。
			RawQuery: "wait=14s&timeout=16s&tag=srv&healthy=true",
		}
		// WithBlock 让期限覆盖服务发现和连接建立；Ready 只证明传输连接可用，
		// 仍需由各次业务 RPC 的结果判断服务是否完成了请求。
		conn, err := dial(ctx, target.String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
			grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
		)
		if err != nil {
			return nil, fmt.Errorf("connect to service %s: %w", service, err)
		}
		return conn, nil
	}
	goodsConn, err := connect(cfg.GoodsSerInfo.Name)
	if err != nil {
		return nil, err
	}
	inventoryConn, err := connect(cfg.InventorySerInfo.Name)
	if err != nil {
		_ = goodsConn.Close()
		return nil, err
	}
	global.GoodsSrvClient = proto.NewGoodsClient(goodsConn)
	global.InventorySrvClient = proto.NewInventoryClient(inventoryConn)
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = inventoryConn.Close()
			_ = goodsConn.Close()
		})
	}, nil
}
