// payment-review is an explicit sandbox operator tool. It does not start RPC,
// MQ, recovery loops or an HTTP service, and never edits inventory directly.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/payment"
)

const ordersDatabase = "grpctest_orders_srv"

const usage = `Usage:
  payment-review inspect --order-id ID < private-config.json
  payment-review fence-and-close --order-id ID --request-id UNIQUE_ID --operator NAME --buyer-id SANDBOX_BUYER_ID [--execute] < private-config.json

Without --execute, fence-and-close only inspects the order; it does not contact
Alipay or change the database. --execute may create a sandbox trade with the
existing order number and attempt to close it. Only a verified CLOSED result
permits the existing order recovery service to return stock.
Private JSON on stdin: {"mysql":{"host":"...","port":"3306","user":"...","password":"...","name":"grpctest_orders_srv"},"alipay":{...}}
`

type options struct {
	action              string
	orderID             int32
	requestID, operator string
	buyerID             string
	execute             bool
}

type privateConfig struct {
	Mysql  config.MysqlConfig `json:"mysql"`
	Alipay payment.Config     `json:"alipay"`
}

type reviewService interface {
	Inspect(context.Context, int32) (handler.CloseReviewInfo, error)
	Fence(context.Context, handler.FenceCloseRequest) (handler.CloseReviewInfo, error)
}

type liveReviewService struct{}

func (liveReviewService) Inspect(ctx context.Context, id int32) (handler.CloseReviewInfo, error) {
	return handler.InspectCloseReview(ctx, id)
}

func (liveReviewService) Fence(ctx context.Context, req handler.FenceCloseRequest) (handler.CloseReviewInfo, error) {
	return handler.FenceAndCloseOrder(ctx, req)
}

type runtimeFactory func(context.Context, privateConfig, bool) (reviewService, func(), error)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, openRuntime); err != nil {
		// Only locally constructed safe errors leave run. Never echo a flag
		// value, the input configuration, a driver error or a channel response.
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer, open runtimeFactory) error {
	opts, err := parseOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(output, usage)
		return err
	}
	if err != nil {
		return err
	}
	cfg, err := readConfig(input)
	if err != nil {
		return err
	}
	service, cleanup, err := open(ctx, cfg, opts.execute)
	if err != nil {
		return errors.New("payment review setup failed; check the private configuration, database connection and migration 0009")
	}
	defer cleanup()
	var info handler.CloseReviewInfo
	if opts.execute {
		info, err = service.Fence(ctx, handler.FenceCloseRequest{OrderID: opts.orderID, RequestID: opts.requestID, Operator: opts.operator, BuyerID: opts.buyerID})
	} else {
		info, err = service.Inspect(ctx, opts.orderID)
	}
	if err != nil {
		return errors.New("payment review did not complete; inspect this order before retrying with the same request ID and inputs")
	}
	result := struct {
		Operation string                  `json:"operation"`
		DryRun    bool                    `json:"dry_run"`
		Order     handler.CloseReviewInfo `json:"order"`
	}{Operation: opts.action, DryRun: !opts.execute, Order: info}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return errors.New("cannot write payment review result; inspect this order before retrying")
	}
	return nil
}

func parseOptions(args []string) (options, error) {
	var opts options
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return opts, flag.ErrHelp
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "fence-and-close") {
		return opts, errors.New("choose inspect or fence-and-close; see --help")
	}
	opts.action = args[0]
	flags := flag.NewFlagSet("payment-review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var orderID int64
	flags.Int64Var(&orderID, "order-id", 0, "existing positive order ID")
	flags.StringVar(&opts.requestID, "request-id", "", "unique operator action ID, reused on retry")
	flags.StringVar(&opts.operator, "operator", "", "operator audit name")
	flags.StringVar(&opts.buyerID, "buyer-id", "", "real sandbox buyer's Alipay ID")
	flags.BoolVar(&opts.execute, "execute", false, "execute sandbox channel reconciliation")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, flag.ErrHelp
		}
		return opts, errors.New("invalid payment review arguments; see --help")
	}
	if flags.NArg() != 0 || orderID <= 0 || orderID > math.MaxInt32 {
		return opts, errors.New("a positive 32-bit --order-id is required; positional arguments are not accepted")
	}
	opts.orderID = int32(orderID)
	if opts.action == "inspect" {
		if opts.execute || opts.requestID != "" || opts.operator != "" || opts.buyerID != "" {
			return opts, errors.New("inspect accepts only --order-id")
		}
		return opts, nil
	}
	if !auditIdentifier(opts.requestID, 100) || strings.ContainsAny(opts.requestID, ".@") || !auditIdentifier(opts.operator, 100) || !decimalIdentifier(opts.buyerID, 16, 16) || !strings.HasPrefix(opts.buyerID, "2088") {
		return opts, errors.New("fence-and-close requires --request-id, --operator and a numeric sandbox --buyer-id; see --help")
	}
	return opts, nil
}

func auditIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, ch := range value {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '@') {
			return false
		}
	}
	return true
}

func decimalIdentifier(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func readConfig(input io.Reader) (privateConfig, error) {
	var cfg privateConfig
	body, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return cfg, errors.New("private configuration must be a JSON object of at most 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid private configuration JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cfg, errors.New("private configuration must contain exactly one JSON object")
	}
	port, err := strconv.Atoi(cfg.Mysql.Port)
	if cfg.Mysql.Name != ordersDatabase || cfg.Mysql.Host == "" || strings.TrimSpace(cfg.Mysql.Host) != cfg.Mysql.Host ||
		cfg.Mysql.User == "" || err != nil || port < 1 || port > 65535 {
		return cfg, errors.New("private configuration must target grpctest_orders_srv with a valid MySQL host, port and user")
	}
	return cfg, nil
}

func openRuntime(ctx context.Context, cfg privateConfig, execute bool) (reviewService, func(), error) {
	var gateway payment.Gateway
	var err error
	if execute {
		if !cfg.Alipay.Enabled {
			return nil, nil, errors.New("sandbox payment is disabled")
		}
		gateway, err = payment.New(cfg.Alipay)
		if err != nil {
			return nil, nil, errors.New("invalid sandbox payment configuration")
		}
	}
	dsn := databaseConfig(cfg.Mysql)
	conn, err := sql.Open("mysql", dsn.FormatDSN())
	if err != nil {
		return nil, nil, errors.New("cannot open order database")
	}
	conn.SetMaxOpenConns(2)
	conn.SetMaxIdleConns(1)
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
		}
	}()
	if err := conn.PingContext(ctx); err != nil {
		return nil, nil, errors.New("cannot connect to order database")
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn}), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
		Logger:         logger.Default.LogMode(logger.Silent),
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, nil, errors.New("cannot initialize order database")
	}
	if err := requireReviewSchema(ctx, db); err != nil {
		return nil, nil, err
	}
	previousDB, previousConfig, previousGateway := global.DB, global.ServerConfig, global.PaymentGateway
	global.DB, global.ServerConfig, global.PaymentGateway = db, &config.ServerConfig{MysqlInfo: cfg.Mysql, Alipay: cfg.Alipay}, gateway
	failed = false
	return liveReviewService{}, func() {
		global.DB, global.ServerConfig, global.PaymentGateway = previousDB, previousConfig, previousGateway
		_ = conn.Close()
	}, nil
}

// The deployed AWS order service uses UTC DATETIME values. This tool must not
// reinterpret those values using the operator's laptop time zone.
func databaseConfig(cfg config.MysqlConfig) *mysqldriver.Config {
	dsn := mysqldriver.NewConfig()
	dsn.User, dsn.Passwd, dsn.DBName = cfg.User, cfg.Password, cfg.Name
	dsn.Net, dsn.Addr = "tcp", net.JoinHostPort(cfg.Host, cfg.Port)
	dsn.ParseTime, dsn.Loc = true, time.UTC
	dsn.Params = map[string]string{"charset": "utf8mb4"}
	dsn.Timeout, dsn.ReadTimeout, dsn.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	return dsn
}

func requireReviewSchema(ctx context.Context, db *gorm.DB) error {
	var columns []string
	if err := db.WithContext(ctx).Raw("SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?", ordersDatabase, "order_close_review").Scan(&columns).Error; err != nil {
		return errors.New("cannot inspect order review migration")
	}
	present := make(map[string]bool, len(columns))
	for _, column := range columns {
		present[column] = true
	}
	for _, column := range []string{"order_sn", "reason", "attempts", "first_seen_at", "last_checked_at", "next_check_at", "resolved_at", "resolution", "request_id", "operator", "request_hash", "action_started_at", "action_expires_at", "app_id", "seller_id"} {
		if !present[column] {
			return errors.New("apply migration 0009_order_close_review.sql before running this tool")
		}
	}
	return nil
}
