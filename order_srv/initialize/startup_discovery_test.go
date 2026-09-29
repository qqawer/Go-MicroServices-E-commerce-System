package initialize

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"grpctest/order_srv/global"

	"google.golang.org/grpc"
)

// Use the real Consul resolver, TCP transport and five-second startup budget.
// No business RPC, AWS service or database is accessed by this reproduction.
func TestStartupRequiresDiscoveredDependenciesBeforeOrder(t *testing.T) {
	for _, ready := range []bool{false, true} {
		name := "order_before_dependencies"
		if ready {
			name = "dependencies_before_order"
		}
		t.Run(name, func(t *testing.T) {
			preserveServiceClients(t)
			cfg, err := parseServerConfig(validOrderConfig)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			stopping := make(chan struct{})
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("index") == "1" {
					select {
					case <-r.Context().Done():
					case <-stopping:
					}
					return
				}
				w.Header().Set("X-Consul-Index", "1")
				w.Header().Set("Content-Type", "application/json")
				if !ready {
					_, _ = w.Write([]byte("[]"))
					return
				}
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"Service": map[string]any{
					"Address": "127.0.0.1", "Port": listener.Addr().(*net.TCPAddr).Port,
				}}})
			}))
			t.Cleanup(func() { close(stopping); consul.Close() })
			cfg.ConsulInfo.Host = "127.0.0.1"
			cfg.ConsulInfo.Port = consul.Listener.Addr().(*net.TCPAddr).Port
			previous := global.ServerConfig
			global.ServerConfig = cfg
			t.Cleanup(func() { global.ServerConfig = previous })
			started := time.Now()
			cleanup, err := InitSrvConn()
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			t.Logf("startup: %v; error: %v", time.Since(started), err)
			if ready {
				if err != nil || cleanup == nil {
					t.Fatalf("healthy dependencies must allow startup: %v", err)
				}
			} else if cleanup != nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "goods-srv") {
				t.Fatalf("expected reproducible dependency startup timeout, got %v", err)
			}
		})
	}
}
