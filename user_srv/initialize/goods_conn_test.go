package initialize

import (
	"errors"
	"net/url"
	"testing"

	"grpctest/user_srv/config"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGoodsDiscoveryTargetDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct{ host, name, wantHost, wantPath string }{
		{"127.0.0.1", "", "127.0.0.1:8500", "/goods-srv"},
		{"::1", " goods-local ", "[::1]:8500", "/goods-local"},
	} {
		cfg := &config.ServerConfig{ConsulInfo: config.ConsulConfig{Host: tc.host, Port: 8500}, GoodsSrvInfo: config.GoodsServiceConfig{Name: tc.name}}
		target, err := goodsServiceTarget(cfg)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(target)
		if err != nil || parsed.Scheme != "consul" || parsed.Host != tc.wantHost || parsed.Path != tc.wantPath || parsed.Query().Get("healthy") != "true" || parsed.Query().Get("tag") != "srv" || parsed.Query().Get("timeout") != "16s" {
			t.Fatalf("incorrect discovery target: %s", target)
		}
	}
}

func TestGoodsConnectionIsLazyAndCleanupIsIdempotent(t *testing.T) {
	cfg := &config.ServerConfig{ConsulInfo: config.ConsulConfig{Host: "127.0.0.1", Port: 8500}}
	var conn *grpc.ClientConn
	client, cleanup, err := initGoodsConn(cfg, func(target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
		// NewClient is lazy: neither this target nor Consul is contacted here.
		var err error
		conn, err = grpc.NewClient("passthrough:///unavailable", append(options, grpc.WithTransportCredentials(insecure.NewCredentials()))...)
		return conn, err
	})
	if err != nil || client == nil || cleanup == nil || conn.GetState() != connectivity.Idle {
		t.Fatalf("optional goods connection must be reusable without waiting for readiness: %v", err)
	}
	cleanup()
	cleanup()
	if conn.GetState() != connectivity.Shutdown {
		t.Fatal("goods connection leaked")
	}
}

func TestGoodsConnectionRejectsInvalidConfigurationBeforeConstruction(t *testing.T) {
	for _, cfg := range []*config.ServerConfig{nil, {}, {ConsulInfo: config.ConsulConfig{Host: "127.0.0.1", Port: -1}}, {ConsulInfo: config.ConsulConfig{Host: "127.0.0.1", Port: 8500}, GoodsSrvInfo: config.GoodsServiceConfig{Name: "goods/other"}}} {
		client, cleanup, err := initGoodsConn(cfg, func(string, ...grpc.DialOption) (*grpc.ClientConn, error) {
			t.Fatal("invalid configuration must not create a connection")
			return nil, nil
		})
		if err == nil || client != nil || cleanup != nil {
			t.Fatal("expected configuration error")
		}
	}
	want := errors.New("construction failed")
	client, cleanup, err := initGoodsConn(&config.ServerConfig{ConsulInfo: config.ConsulConfig{Host: "127.0.0.1", Port: 8500}}, func(string, ...grpc.DialOption) (*grpc.ClientConn, error) { return nil, want })
	if !errors.Is(err, want) || client != nil || cleanup != nil {
		t.Fatalf("constructor failure must not publish a client: %v", err)
	}
}
