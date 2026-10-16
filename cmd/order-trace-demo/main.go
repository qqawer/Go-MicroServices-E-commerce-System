// order-trace-demo runs real order handlers against isolated SQL and a lab broker.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"syscall"
	"time"

	"grpctest/internal/mqruntime"
	"grpctest/internal/tracing"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/initialize"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	async := flag.Bool("async", false, "enable lab timeout consumer and outbox dispatcher")
	flag.Parse()
	if err := run(*async); err != nil {
		log.Fatal(err)
	}
}
func run(async bool) error {
	shutdown, err := tracing.Init("order-srv")
	if err != nil {
		return err
	}
	defer shutdown()
	global.ServerConfig = &config.ServerConfig{
		MysqlInfo: config.MysqlConfig{Host: "mysql", Port: "3306", Name: "trace_orders_demo", User: "root"},
		MqInfo:    mqruntime.Config{Enabled: true, Host: "trace-namesrv", Port: 9876, Mode: "demo", OrderTTLSeconds: 3600},
	}
	closeDB, err := initialize.InitDB()
	if err != nil {
		return err
	}
	defer closeDB()
	if async {
		global.ServerConfig.MqInfo.OrderTTLSeconds = 30
		// Demo payments are disabled; the dispatcher only reads this empty guard table.
		if err := global.DB.Exec("CREATE TABLE IF NOT EXISTS order_payment (id BIGINT PRIMARY KEY AUTO_INCREMENT, order_sn VARCHAR(200), review BOOLEAN DEFAULT FALSE, status VARCHAR(20), UNIQUE KEY uq_trace_payment_order(order_sn))").Error; err != nil {
			return err
		}
	}
	for _, table := range []any{&model.ShoppingCart{}, &model.OrderInfo{}, &model.OrderGoods{}, &model.OrderTransaction{}, &model.OrderEvent{}} {
		if !global.DB.Migrator().HasTable(table) {
			if err := global.DB.AutoMigrate(table); err != nil {
				return err
			}
		}
	}
	if !global.DB.Migrator().HasColumn(&model.OrderEvent{}, "TraceContext") {
		if err := global.DB.Migrator().AddColumn(&model.OrderEvent{}, "TraceContext"); err != nil {
			return err
		}
	}
	for _, ddl := range []string{
		"ALTER TABLE order_transaction MODIFY request_key VARCHAR(128) NULL",
		"ALTER TABLE order_event MODIFY order_sn VARCHAR(200), MODIFY topic VARCHAR(64)",
	} {
		if err := global.DB.Exec(ddl).Error; err != nil {
			return err
		}
	}
	for _, index := range []struct{ name, ddl string }{
		{"uq_trace_request", "CREATE UNIQUE INDEX uq_trace_request ON order_transaction(user_id,request_key)"},
		{"uq_trace_event", "CREATE UNIQUE INDEX uq_trace_event ON order_event(order_sn,topic)"},
	} {
		table := "order_transaction"
		if index.name == "uq_trace_event" {
			table = "order_event"
		}
		if !global.DB.Migrator().HasIndex(table, index.name) {
			if err := global.DB.Exec(index.ddl).Error; err != nil {
				return err
			}
		}
	}
	quantities := []int32{2, 101, 1}
	if async {
		quantities = append(quantities, 2, 1, 101, 1)
	}
	for offset, qty := range quantities {
		user := int32(60101 + offset)
		var intents, carts int64
		if err := global.DB.Model(&model.OrderTransaction{}).Where("user_id = ?", user).Count(&intents).Error; err != nil {
			return err
		}
		if err := global.DB.Model(&model.ShoppingCart{}).Where("user = ?", user).Count(&carts).Error; err != nil {
			return err
		}
		if intents == 0 && carts == 0 {
			if err := global.DB.Create(&model.ShoppingCart{User: user, Goods: 1, Nums: qty, Checked: true}).Error; err != nil {
				return err
			}
		}
	}
	connect := func(target string) (*grpc.ClientConn, error) {
		return grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	}
	goods, err := connect("grpctest-es-store-goods-rpc:50052")
	if err != nil {
		return err
	}
	defer goods.Close()
	inventory, err := connect("trace-inventory:50053")
	if err != nil {
		return err
	}
	defer inventory.Close()
	global.GoodsSrvClient = proto.NewGoodsClient(goods)
	global.InventorySrvClient = proto.NewInventoryClient(inventory)
	if err := mqruntime.WaitTopics(context.Background(), global.ServerConfig.MqInfo, "order-reback", "order_timeout"); err != nil {
		return err
	}
	events, err := mqruntime.Producer(global.ServerConfig.MqInfo, "trace-order-events")
	if err != nil {
		return err
	}
	defer events.Shutdown()
	transactions, err := mqruntime.TransactionProducer(global.ServerConfig.MqInfo, &handler.TransactionListener{})
	if err != nil {
		return err
	}
	defer transactions.Shutdown()
	global.MQOrder, global.MQInventory, global.MQTransactions = events, events, transactions
	if async {
		cleanup, err := startAsync()
		if err != nil {
			return err
		}
		defer cleanup()
	}
	lis, err := net.Listen("tcp", ":50054")
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	proto.RegisterOrderServer(server, &handler.OrderServer{})
	go func() {
		if err := server.Serve(lis); err != nil {
			log.Print(err)
		}
	}()
	log.Print("isolated order tracing RPC ready: trace_orders_demo, demo payments, real lab broker")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	server.GracefulStop()
	return nil
}

// Deliberately omit RecoverOrders here: verification must prove delivery through MQ,
// rather than let the fallback RPC release stock before a consumer receives it.
func startAsync() (func(), error) {
	consumer, err := mqruntime.Consumer(global.ServerConfig.MqInfo, "trace-order-timeout", "order_timeout", handler.TimeOutReback)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			cycle, stop := context.WithTimeout(ctx, 15*time.Second)
			if err := handler.DispatchOrderEvents(cycle, 100); err != nil && ctx.Err() == nil {
				log.Print("lab outbox pending: ", err)
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done; _ = consumer.Shutdown() }, nil
}
