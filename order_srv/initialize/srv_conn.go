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
	"go.uber.org/zap"
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
	connect := func(service string, waitReady bool) (*grpc.ClientConn, error) {
		target := url.URL{
			Scheme: "consul",
			Host:   net.JoinHostPort(cfg.ConsulInfo.Host, strconv.Itoa(cfg.ConsulInfo.Port)),
			Path:   "/" + service,
			// resolver 自身的 HTTP 请求也设期限，避免关闭连接后无限等待 Consul。
			RawQuery: "wait=14s&timeout=16s&tag=srv&healthy=true",
		}
		// WithBlock 让期限覆盖服务发现和连接建立；Ready 只证明传输连接可用，
		// 仍需由各次业务 RPC 的结果判断服务是否完成了请求。
		options := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
		}
		if waitReady {
			options = append(options, grpc.WithBlock())
		}
		dialCtx := ctx
		if !waitReady {
			// Required discovery may have consumed the shared startup deadline. The
			// optional reconnecting client needs a fresh construction budget, not an
			// already-expired context that would leave it nil until a process restart.
			var cancel context.CancelFunc
			dialCtx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
		}
		conn, err := dial(dialCtx, target.String(), options...)
		if err != nil {
			return nil, fmt.Errorf("connect to service %s: %w", service, err)
		}
		return conn, nil
	}
	goodsConn, err := connect(cfg.GoodsSerInfo.Name, true)
	if err != nil {
		return nil, err
	}
	inventoryConn, err := connect(cfg.InventorySerInfo.Name, true)
	if err != nil {
		_ = goodsConn.Close()
		return nil, err
	}
	// Address lookup is optional for legacy checkout and durable retries. Connect
	// without blocking readiness so a user-service outage cannot stop those flows;
	// gRPC reconnects when discovery supplies a healthy endpoint again.
	addressConn, addressErr := connect(cfg.UserSrvInfo.Name, false)
	global.AddressBookClient = nil
	if addressErr != nil {
		zap.S().Warnf("address lookup connection unavailable: %v", addressErr)
	} else {
		global.AddressBookClient = proto.NewAddressBookClient(addressConn)
	}
	global.GoodsSrvClient = proto.NewGoodsClient(goodsConn)
	global.InventorySrvClient = proto.NewInventoryClient(inventoryConn)
	var once sync.Once
	return func() {
		once.Do(func() {
			if addressConn != nil {
				_ = addressConn.Close()
			}
			_ = inventoryConn.Close()
			_ = goodsConn.Close()
		})
	}, nil
}
