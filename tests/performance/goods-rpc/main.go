// This entry point runs production goods handlers against an owned, local test DB.
// It intentionally does not load YAML, Nacos, Consul, or inventory workers.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"grpctest/goods_srv/config"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/initialize"
	"grpctest/goods_srv/model/proto"
)

type connection struct {
	Host, User, Password, Marker string
	Port                         int
}

// Reuse the production read implementations; fixture writes are unavailable.
type readOnlyGoods struct {
	proto.UnimplementedGoodsServer
	production handler.GoodsServer
}

func (s *readOnlyGoods) GoodsList(ctx context.Context, request *proto.GoodsFilterRequest) (*proto.GoodsListResponse, error) {
	return s.production.GoodsList(ctx, request)
}

func (s *readOnlyGoods) GetGoodsDetail(ctx context.Context, request *proto.GoodInfoRequest) (*proto.GoodsInfoResponse, error) {
	return s.production.GetGoodsDetail(ctx, request)
}

func run() error {
	path := flag.String("config", "", "private generated test connection JSON")
	ready := flag.String("ready-file", "", "new readiness JSON file")
	flag.Parse()
	f, err := os.Open(*path)
	if err != nil {
		return fmt.Errorf("open private connection file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("connection file must be regular and owner-only")
	}
	var c connection
	decoder := json.NewDecoder(io.LimitReader(f, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return fmt.Errorf("invalid test connection JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("trailing connection data")
	}
	hex := regexp.MustCompile(`^[0-9a-f]{48}$`)
	if c.Host != "127.0.0.1" || c.Port < 1 || c.Port > 65535 || c.User != "grpctest" || !hex.MatchString(c.Password) || !hex.MatchString(c.Marker) {
		return fmt.Errorf("require generated application credentials and IPv4 loopback")
	}
	// Verify the marker before production initialization, using a bounded connection.
	d := driver.NewConfig()
	d.Net, d.Addr, d.User, d.Passwd = "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.User, c.Password
	d.Timeout, d.ReadTimeout, d.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	db, err := sql.Open("mysql", d.FormatDSN())
	if err != nil {
		return fmt.Errorf("open test marker connection")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	var marker string
	err = db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MIN(marker),'') FROM grpctest_migrations.test_environment").Scan(&count, &marker)
	if err != nil || count != 1 || marker != c.Marker {
		return fmt.Errorf("refusing database without matching test marker")
	}
	global.ServerConfig = &config.ServerConfig{MysqlInfo: config.MysqlConfig{
		Host: c.Host, Port: strconv.Itoa(c.Port), User: c.User, Password: c.Password, Name: "grpctest_goods_srv",
	}}
	// Keep production GORM Info logging and default connection pool for the baseline.
	initialize.InitDB()
	pool, err := global.DB.DB()
	if err != nil {
		return fmt.Errorf("get production SQL connection pool")
	}
	defer pool.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	server := grpc.NewServer()
	proto.RegisterGoodsServer(server, &readOnlyGoods{})
	errors := make(chan error, 1)
	go func() { errors <- server.Serve(listener) }()
	defer server.Stop()
	r, err := os.OpenFile(*ready, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create new readiness file: %w", err)
	}
	err = json.NewEncoder(r).Encode(map[string]string{"address": listener.Addr().String()})
	r.Close()
	if err != nil {
		return err
	}
	signals, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errors:
		return err
	case <-signals.Done():
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			server.Stop()
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
