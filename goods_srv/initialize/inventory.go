package initialize

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"net"
	"net/url"
	"strconv"

	_ "github.com/mbobakov/grpc-consul-resolver"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/credentials/insecure"
	"grpctest/goods_srv/config"
)

// ConnectInventory reuses one connection; individual RPCs enforce deadlines.
// Discovery is asynchronous so inventory downtime does not block product reads.
func ConnectInventory(cfg *config.ServerConfig) (*grpc.ClientConn, error) {
	name := cfg.InventoryInfo.Name
	if name == "" {
		name = "inventory-srv"
	}
	target := url.URL{Scheme: "consul", Host: net.JoinHostPort(cfg.ConsulInfo.Host, strconv.Itoa(cfg.ConsulInfo.Port)),
		Path: "/" + name, RawQuery: "wait=14s&timeout=16s&tag=srv&healthy=true"}
	return grpc.NewClient(target.String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`))
}
