package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/proto"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestServeRegistersActualPortAndCleansUp(t *testing.T) {
	old := global.ServerConfig
	global.ServerConfig = &config.ServerConfig{Name: "orders-srv", Host: "127.0.0.1", Tags: []string{"srv"}}
	t.Cleanup(func() { global.ServerConfig = old })
	registered := make(chan api.AgentServiceRegistration, 1)
	deregistered := make(chan string, 1)
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agent/service/register":
			var registration api.AgentServiceRegistration
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			registered <- registration
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/deregister/"):
			deregistered <- strings.TrimPrefix(r.URL.Path, "/v1/agent/service/deregister/")
		default:
			t.Errorf("unexpected Consul request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()
	client, err := api.NewClient(&api.Config{Address: consul.URL, HttpClient: consul.Client()})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, client) }()
	var registration api.AgentServiceRegistration
	select {
	case registration = <-registered:
	case <-time.After(3 * time.Second):
		t.Fatal("service did not register")
	}
	if registration.Address != "127.0.0.1" || registration.Port != listener.Addr().(*net.TCPAddr).Port || registration.Check.GRPC != listener.Addr().String() {
		t.Fatalf("registration does not match the listener: %+v", registration)
	}
	if registration.ID == registration.Name {
		t.Fatal("instance ID must distinguish instances of the same service")
	}
	requestCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	conn, err := grpc.DialContext(requestCtx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	health, err := grpc_health_v1.NewHealthClient(conn).Check(requestCtx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || health.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health check: response=%v error=%v", health, err)
	}
	_, err = proto.NewOrderClient(conn).CreateOrder(requestCtx, &proto.OrderRequest{UserId: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("order RPC must be registered and reject an unconfigured transaction: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
	select {
	case id := <-deregistered:
		if id != registration.ID {
			t.Fatalf("deregistered %q instead of %q", id, registration.ID)
		}
	default:
		t.Fatal("service was not deregistered")
	}
}

type blockedHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	entered chan struct{}
	release <-chan struct{}
}

func (s *blockedHealthServer) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	close(s.entered)
	// Deliberately ignore RPC cancellation to reproduce a handler stuck in work
	// that does not accept a context. The test always releases it before cleanup.
	<-s.release
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

func TestStopServerDoesNotWaitForHandlerIgnoringCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finishHandler := sync.OnceFunc(func() { close(release) })
	server := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(server, &blockedHealthServer{entered: entered, release: release})
	servingDone := make(chan struct{})
	go func() { _ = server.Serve(listener); close(servingDone) }()
	t.Cleanup(func() {
		finishHandler()
		// The deliberately blocked handler has now been released, so it is safe
		// to join shutdown and leave no serving/handler goroutines behind.
		server.GracefulStop()
		_ = listener.Close()
		<-servingDone
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	callDone := make(chan error, 1)
	go func() {
		_, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		callDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("health handler did not start")
	}
	stopped := make(chan bool, 1)
	go func() { stopped <- stopServerWithin(server, 25*time.Millisecond) }()
	returned := false
	select {
	case drained := <-stopped:
		returned = true
		if drained {
			t.Error("shutdown reported success while a handler was still blocked")
		}
	case <-time.After(time.Second):
		t.Error("shutdown exceeded its grace period while the handler ignored cancellation")
	}
	// Forceful shutdown must also disconnect the caller, even though the server
	// handler is still waiting for the test's release signal.
	select {
	case err := <-callDone:
		if err == nil {
			t.Error("blocked RPC unexpectedly succeeded during forceful shutdown")
		}
	case <-time.After(time.Second):
		t.Error("forceful shutdown did not disconnect the client RPC")
	}
	finishHandler()
	if !returned {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("shutdown did not finish after the test released the handler")
		}
	}
}
