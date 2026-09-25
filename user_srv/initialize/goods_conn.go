package initialize

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	goodsproto "grpctest/goods_srv/model/proto"
	"grpctest/user_srv/config"
	"grpctest/user_srv/global"

	_ "github.com/mbobakov/grpc-consul-resolver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// InitGoodsConn creates one reusable lazy connection. An unavailable goods
// service must not block login or addresses; favorites fail only when queried.
func InitGoodsConn() (goodsproto.GoodsClient, func(), error) {
	return initGoodsConn(global.ServerConfig, grpc.NewClient)
}

func goodsServiceTarget(cfg *config.ServerConfig) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.ConsulInfo.Host) == "" || cfg.ConsulInfo.Port < 1 || cfg.ConsulInfo.Port > 65535 {
		return "", fmt.Errorf("goods discovery requires a valid Consul host and port")
	}
	name := strings.TrimSpace(cfg.GoodsSrvInfo.Name)
	if name == "" {
		name = "goods-srv"
	}
	if strings.ContainsAny(name, "/?# \t\r\n") {
		return "", fmt.Errorf("invalid goods service name")
	}
	target := url.URL{
		Scheme: "consul", Host: net.JoinHostPort(cfg.ConsulInfo.Host, strconv.Itoa(cfg.ConsulInfo.Port)),
		Path: "/" + name, RawQuery: "wait=14s&timeout=16s&tag=srv&healthy=true",
	}
	return target.String(), nil
}

func initGoodsConn(cfg *config.ServerConfig, create func(string, ...grpc.DialOption) (*grpc.ClientConn, error)) (goodsproto.GoodsClient, func(), error) {
	target, err := goodsServiceTarget(cfg)
	if err != nil {
		return nil, nil, err
	}
	conn, err := create(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct goods connection: %w", err)
	}
	var once sync.Once
	return goodsproto.NewGoodsClient(conn), func() { once.Do(func() { _ = conn.Close() }) }, nil
}
