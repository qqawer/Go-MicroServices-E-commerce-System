//go:build integration

// Package testdb provides marked test databases and in-memory gRPC transport.
// It never imports service globals, YAML configuration, or Nacos initialization.
package testdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func Open(t *testing.T, database string) *gorm.DB {
	t.Helper()
	switch database {
	case "grpctest_user_srv", "grpctest_goods_srv", "grpctest_inventory_srv", "grpctest_orders_srv":
	default:
		t.Fatal("database is outside the integration allowlist")
	}
	file, err := os.Open(os.Getenv("GRPCTEST_TEST_CONFIG"))
	if err != nil {
		t.Fatal("GRPCTEST_TEST_CONFIG must point to the generated private test connection file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("test connection file must be regular and accessible only by its owner")
	}
	var settings struct {
		Host, User, Password, Marker string
		Port                         int
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		t.Fatal("invalid test connection JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		t.Fatal("unexpected trailing test connection data")
	}
	ip := net.ParseIP(settings.Host)
	if ip == nil || !ip.IsLoopback() || settings.Port < 1 || settings.Port > 65535 || settings.User != "grpctest" || settings.Password == "" || settings.Marker == "" {
		t.Fatal("require loopback MySQL, grpctest application account, password, and isolated environment marker")
	}
	cfg := driver.NewConfig()
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port))
	cfg.User, cfg.Passwd, cfg.DBName = settings.User, settings.Password, database
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open integration database")
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	var marker string
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MIN(marker),'') FROM grpctest_migrations.test_environment").Scan(&count, &marker); err != nil || count != 1 || marker != settings.Marker {
		t.Fatal("refusing database without matching isolated environment marker")
	}
	orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
		NamingStrategy:       schema.NamingStrategy{SingularTable: true},
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal("cannot initialize test database models")
	}
	return orm
}

func Client(t *testing.T, register func(*grpc.Server)) (context.Context, *grpc.ClientConn) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	register(server)
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	conn, err := grpc.DialContext(ctx, "passthrough:///integration", grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal("cannot connect to in-memory gRPC server")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ctx, conn
}
