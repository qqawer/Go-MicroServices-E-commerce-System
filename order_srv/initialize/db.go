package initialize

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"time"

	"grpctest/order_srv/global"

	mysqldriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// InitDB publishes the order database only after connecting successfully.
// The caller owns the returned cleanup function.
func InitDB() (func(), error) {
	if global.ServerConfig == nil {
		return nil, fmt.Errorf("order configuration has not been loaded")
	}
	c := global.ServerConfig.MysqlInfo
	dsn := mysqldriver.NewConfig()
	dsn.User, dsn.Passwd, dsn.DBName = c.User, c.Password, c.Name
	dsn.Net, dsn.Addr = "tcp", net.JoinHostPort(c.Host, c.Port)
	dsn.ParseTime, dsn.Loc = true, time.Local
	dsn.Params = map[string]string{"charset": "utf8mb4"}
	dsn.Timeout, dsn.ReadTimeout, dsn.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	conn, err := sql.Open("mysql", dsn.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open order database: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("connect order database: %w", err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn}), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
		Logger:         logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("initialize order database: %w", err)
	}
	global.DB = db
	return func() {
		if err := conn.Close(); err != nil {
			zap.L().Warn("close order database", zap.Error(err))
		}
		global.DB = nil
	}, nil
}
