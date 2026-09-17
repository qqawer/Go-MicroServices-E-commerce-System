package handler

import (
	"context"
	"encoding/json"
	"testing"

	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCreateOrderRequiresTransactionRuntimeBeforeWriting(t *testing.T) {
	previousConfig, previousDB := global.ServerConfig, global.DB
	previousGoods, previousInventory := global.GoodsSrvClient, global.InventorySrvClient
	previousMQOrder, previousMQInventory := global.MQOrder, global.MQInventory
	// 基础依赖已创建，但 MQ 运行时尚未提供；这应在进入旧交易流程前失败。
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	global.DB, err = gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	global.ServerConfig = &config.ServerConfig{}
	global.GoodsSrvClient = proto.NewGoodsClient(nil)
	global.InventorySrvClient = proto.NewInventoryClient(nil)
	global.MQOrder = nil
	global.MQInventory = nil
	t.Cleanup(func() {
		global.ServerConfig, global.DB = previousConfig, previousDB
		global.GoodsSrvClient, global.InventorySrvClient = previousGoods, previousInventory
		global.MQOrder, global.MQInventory = previousMQOrder, previousMQInventory
	})

	_, err = (&OrderServer{}).CreateOrder(context.Background(), &proto.OrderRequest{UserId: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unconfigured transaction runtime: got %v, want FailedPrecondition", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderHonorsCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&OrderServer{}).CreateOrder(ctx, &proto.OrderRequest{UserId: 1})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled request: got %v, want Canceled", err)
	}
}

func TestOrderTransactionWithoutParentSpan(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	gormDB, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := global.DB
	global.DB = gormDB
	t.Cleanup(func() { global.DB = previous })
	mock.ExpectQuery("SELECT .* FROM `shoppingcart`").WillReturnRows(sqlmock.NewRows([]string{"id", "user", "goods", "nums", "checked"}))
	body, err := json.Marshal(model.OrderInfo{User: 1, OrderSn: "empty-cart"})
	if err != nil {
		t.Fatal(err)
	}
	listener := OrderListener{Ctx: context.Background()}
	state := listener.ExecuteLocalTransaction(primitive.NewMessage("order-reback", body))
	if state != primitive.RollbackMessageState || listener.Code != codes.InvalidArgument {
		t.Fatalf("empty cart without a parent span: state=%v code=%v", state, listener.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
