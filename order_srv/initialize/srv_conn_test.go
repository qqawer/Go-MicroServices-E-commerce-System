package initialize

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"grpctest/order_srv/global"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/test/bufconn"
)

func preserveServiceClients(t *testing.T) {
	t.Helper()
	goods, inventory := global.GoodsSrvClient, global.InventorySrvClient
	address := global.AddressBookClient
	global.GoodsSrvClient, global.InventorySrvClient = nil, nil
	global.AddressBookClient = nil
	t.Cleanup(func() {
		global.GoodsSrvClient, global.InventorySrvClient = goods, inventory
		global.AddressBookClient = address
	})
}

func TestInitSrvConnClosesFirstConnectionAfterPartialFailure(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	first, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	want := errors.New("inventory unavailable")
	calls := 0
	dial := func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return nil, want
	}
	cleanup, err := initSrvConn(context.Background(), cfg, dial)
	if cleanup != nil || !errors.Is(err, want) || !strings.Contains(err.Error(), "inventory-srv") {
		t.Fatalf("want inventory connection error, got %v", err)
	}
	if first.GetState() != connectivity.Shutdown || global.GoodsSrvClient != nil || global.InventorySrvClient != nil {
		t.Fatal("partial initialization must close the first connection without publishing clients")
	}
}

func TestInitSrvConnWaitsForReadyInsteadOfOnlyCreatingClients(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dial := func(ctx context.Context, target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
		builder := manual.NewBuilderWithScheme("consul")
		builder.InitialState(resolver.State{})
		// This local resolver never provides an endpoint; no Consul/DNS requests occur.
		return grpc.DialContext(ctx, target, append(options, grpc.WithResolvers(builder))...)
	}
	cleanup, err := initSrvConn(ctx, cfg, dial)
	if cleanup != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want readiness timeout, got %v", err)
	}
	if global.GoodsSrvClient != nil || global.InventorySrvClient != nil {
		t.Fatal("unavailable services must not publish usable clients")
	}
}

func TestInitSrvConnReusesReadyConnectionsAndCleansUp(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var connections []*grpc.ClientConn
	dial := func(ctx context.Context, target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
		if !strings.Contains(target, "healthy=true") || !strings.Contains(target, "timeout=16s") {
			t.Fatal("Consul discovery must filter health and bound its HTTP requests")
		}
		builder := manual.NewBuilderWithScheme("consul")
		builder.InitialState(resolver.State{Addresses: []resolver.Address{{Addr: "in-memory"}}})
		options = append(options, grpc.WithResolvers(builder), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
		conn, err := grpc.DialContext(ctx, target, options...)
		if conn != nil {
			connections = append(connections, conn)
		}
		return conn, err
	}
	cleanup, err := initSrvConn(ctx, cfg, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if len(connections) != 3 || global.GoodsSrvClient == nil || global.InventorySrvClient == nil || global.AddressBookClient == nil {
		t.Fatal("expected required goods/inventory and optional address connections")
	}
	for _, conn := range connections[:2] {
		if conn.GetState() != connectivity.Ready {
			t.Fatal("connection was not ready")
		}
	}
	cleanup()
	cleanup()
	for _, conn := range connections {
		if conn.GetState() != connectivity.Shutdown {
			t.Fatal("connection leaked")
		}
	}
}

func TestInitSrvConnKeepsCheckoutWhenAddressDialFails(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	var connections []*grpc.ClientConn
	dial := func(_ context.Context, target string, _ ...grpc.DialOption) (*grpc.ClientConn, error) {
		if strings.Contains(target, "/user-srv?") {
			return nil, errors.New("address unavailable")
		}
		conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
		connections = append(connections, conn)
		return conn, err
	}
	cleanup, err := initSrvConn(context.Background(), cfg, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if len(connections) != 2 || global.GoodsSrvClient == nil || global.InventorySrvClient == nil || global.AddressBookClient != nil {
		t.Fatal("optional address failure must preserve legacy checkout clients")
	}
	cleanup()
	for _, conn := range connections {
		if conn.GetState() != connectivity.Shutdown {
			t.Fatal("connection leaked")
		}
	}
}

func TestInitSrvConnDoesNotWaitForAddressReadiness(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	dial := func(ctx context.Context, target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
		if !strings.Contains(target, "/user-srv?") {
			return grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
		}
		builder := manual.NewBuilderWithScheme("consul")
		builder.InitialState(resolver.State{})
		return grpc.DialContext(ctx, target, append(options, grpc.WithResolvers(builder))...)
	}
	cleanup, err := initSrvConn(ctx, cfg, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if global.AddressBookClient == nil || ctx.Err() != nil {
		t.Fatal("optional address discovery must not block startup")
	}
}

func TestInitSrvConnConstructsAddressClientAfterRequiredStartupBudgetExpires(t *testing.T) {
	preserveServiceClients(t)
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dial := func(dialCtx context.Context, target string, _ ...grpc.DialOption) (*grpc.ClientConn, error) {
		if strings.Contains(target, "/inventory-srv?") {
			cancel() // The required connection succeeded just as its budget expired.
		}
		if strings.Contains(target, "/user-srv?") && dialCtx.Err() != nil {
			t.Fatal("address client inherited an exhausted required-service startup budget")
		}
		return grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	cleanup, err := initSrvConn(ctx, cfg, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if global.AddressBookClient == nil {
		t.Fatal("address client must be constructed for later automatic reconnection")
	}
}

func TestInitSrvConnRejectsMissingConfigurationBeforeDial(t *testing.T) {
	cleanup, err := initSrvConn(context.Background(), nil, func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error) {
		t.Fatal("must validate configuration before dialing")
		return nil, nil
	})
	if cleanup != nil || err == nil {
		t.Fatal("expected missing configuration error")
	}
}
