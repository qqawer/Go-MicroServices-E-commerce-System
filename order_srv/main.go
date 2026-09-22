package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/initialize"
	"grpctest/order_srv/proto"

	"github.com/hashicorp/consul/api"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	ip := flag.String("ip", "0.0.0.0", "监听地址")
	port := flag.Int("port", 50054, "监听端口，0 表示自动分配")
	flag.Parse()
	initialize.InitLogger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *ip, *port); err != nil {
		zap.L().Error("order service stopped", zap.Error(err))
		os.Exit(1)
	}
}

var errShutdownTimeout = errors.New("order RPC handlers did not finish within the shutdown grace period")

func run(ctx context.Context, ip string, port int) (err error) {
	if port < 0 || port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}
	if err := initialize.InitConfig(); err != nil {
		return err
	}
	closeDB, err := initialize.InitDB()
	if err != nil {
		return err
	}
	defer func() {
		if !errors.Is(err, errShutdownTimeout) {
			closeDB()
		}
	}()
	closeClients, err := initialize.InitSrvConn()
	if err != nil {
		return err
	}
	defer func() {
		if !errors.Is(err, errShutdownTimeout) {
			closeClients()
		}
	}()
	closeMQ, err := initialize.InitMQ(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if !errors.Is(err, errShutdownTimeout) {
			closeMQ()
		}
	}()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen for order RPC: %w", err)
	}
	defer listener.Close()
	consulConfig := api.DefaultConfig()
	consulConfig.Address = net.JoinHostPort(global.ServerConfig.ConsulInfo.Host, strconv.Itoa(global.ServerConfig.ConsulInfo.Port))
	consulConfig.HttpClient = &http.Client{Timeout: 5 * time.Second}
	client, err := api.NewClient(consulConfig)
	if err != nil {
		return fmt.Errorf("create consul client: %w", err)
	}
	return serve(ctx, listener, client)
}

func serve(ctx context.Context, listener net.Listener, client *api.Client) (err error) {
	server := grpc.NewServer()
	proto.RegisterOrderServer(server, &handler.OrderServer{})
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	defer func() {
		if !stopServer(server) {
			err = errors.Join(err, errShutdownTimeout)
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	port := listener.Addr().(*net.TCPAddr).Port
	c := global.ServerConfig
	address := net.JoinHostPort(c.Host, strconv.Itoa(port))
	id := fmt.Sprintf("%s-%s-%d", c.Name, c.Host, port)
	registration := &api.AgentServiceRegistration{
		ID: id, Name: c.Name, Address: c.Host, Port: port, Tags: c.Tags,
		Check: &api.AgentServiceCheck{
			GRPC: address, Interval: "5s", Timeout: "2s", DeregisterCriticalServiceAfter: "1m",
		},
	}
	if err := client.Agent().ServiceRegister(registration); err != nil {
		return fmt.Errorf("register order service: %w", err)
	}
	defer func() {
		if err := client.Agent().ServiceDeregister(id); err != nil {
			zap.L().Warn("deregister order service", zap.Error(err))
		}
	}()
	zap.L().Info("order RPC started", zap.String("service", c.Name), zap.String("address", address))
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

func stopServer(server *grpc.Server) bool {
	return stopServerWithin(server, 5*time.Second)
}

// A false result means handlers may still be using shared dependencies. The
// caller must report a shutdown failure and let main exit the process rather
// than closing or clearing those dependencies underneath an active handler.
func stopServerWithin(server *grpc.Server, grace time.Duration) bool {
	stopped := make(chan struct{})
	go func() { server.GracefulStop(); close(stopped) }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-stopped:
		return true
	case <-timer.C:
		// Stop can itself wait for an internal mutex held by GracefulStop while
		// a non-cooperative handler is running, so neither call is joined here.
		go server.Stop()
		return false
	}
}
