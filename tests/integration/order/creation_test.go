//go:build integration

package order_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

type creationProducer struct {
	rocketmq.TransactionProducer
	fail bool
}

func (p *creationProducer) SendMessageInTransaction(_ context.Context, msg *primitive.Message) (*primitive.TransactionSendResult, error) {
	if p.fail {
		return nil, errors.New("broker unavailable")
	}
	state := (&handler.TransactionListener{}).ExecuteLocalTransaction(msg)
	return &primitive.TransactionSendResult{State: state}, nil
}
func creationSetup(t *testing.T) (*gorm.DB, *creationProducer) {
	db, _, _ := transactionSetup(t)
	oldConfig, oldProducer := global.ServerConfig, global.MQTransactions
	p := &creationProducer{}
	global.MQTransactions = p
	global.ServerConfig = &config.ServerConfig{MqInfo: config.MqConfig{Enabled: true, Mode: "demo", OrderTTLSeconds: 30}}
	t.Cleanup(func() {
		var records []model.OrderTransaction
		db.Where("request_key LIKE ?", "creation-%").Find(&records)
		for _, r := range records {
			db.Exec("DELETE FROM ordergoods WHERE `order` IN (SELECT id FROM orderinfo WHERE order_sn = ?)", r.OrderSn)
			db.Unscoped().Where("order_sn = ?", r.OrderSn).Delete(&model.OrderInfo{})
			db.Where("order_sn = ?", r.OrderSn).Delete(&model.OrderEvent{})
			db.Where("order_sn = ?", r.OrderSn).Delete(&model.OrderTransaction{})
		}
		global.ServerConfig, global.MQTransactions = oldConfig, oldProducer
	})
	return db, p
}
func creationRequest(user int32, key string) *proto.OrderRequest {
	return &proto.OrderRequest{UserId: user, RequestKey: key, Name: "测试", Address: "测试地址", Mobile: "13000000001", Post: "测试"}
}
func TestCreationConcurrentKeyIsScopedAndSnapshotSurvivesRetry(t *testing.T) {
	db, p := creationSetup(t)
	db.Create(&model.ShoppingCart{User: 900101, Goods: 421, Nums: 2, Checked: true})
	request := creationRequest(900101, "creation-concurrent")
	p.fail = true
	pending, err := (&handler.OrderServer{}).CreateOrder(context.Background(), request)
	if err != nil || pending.Status != "CREATING" {
		t.Fatal(pending, err)
	}
	// A changed cart is not silently substituted into an already accepted request.
	db.Model(&model.ShoppingCart{}).Where("user = ?", 900101).Update("nums", 9)
	p.fail = false
	var wg sync.WaitGroup
	ids := make(chan int32, 30)
	errs := make(chan error, 30)
	for n := 0; n < 30; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := (&handler.OrderServer{}).CreateOrder(context.Background(), request)
			if e != nil {
				errs <- e
				return
			}
			if r.Status != "WAIT_BUYER_PAY" || r.TotalCents != 2500 || r.OrderSn != pending.OrderSn {
				errs <- fmt.Errorf("wrong replay: %v", r)
				return
			}
			ids <- r.Id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var id int32
	for got := range ids {
		if id == 0 {
			id = got
		}
		if got != id {
			t.Fatal("duplicate order", got, id)
		}
	}
	var count int64
	db.Model(&model.OrderInfo{}).Where("order_sn = ?", pending.OrderSn).Count(&count)
	if count != 1 {
		t.Fatal("orders", count)
	}
	var cart model.ShoppingCart
	db.Where("user = ?", 900101).First(&cart)
	if cart.Nums != 9 {
		t.Fatal("edited cart was deleted")
	}
	var item model.OrderGoods
	db.Where("`order` = ?", id).First(&item)
	if item.Nums != 2 || item.UnitCents == nil || *item.UnitCents != 1250 {
		t.Fatal("snapshot changed", item)
	}
	request.Address = "不同地址"
	if _, e := (&handler.OrderServer{}).CreateOrder(context.Background(), request); status.Code(e) != codes.AlreadyExists {
		t.Fatal("changed body reused key", e)
	}
	if _, e := (&handler.OrderServer{}).GetOrderCreation(context.Background(), creationRequest(900102, request.RequestKey)); status.Code(e) != codes.NotFound {
		t.Fatal("cross-user request exposed", e)
	}
	db.Create(&model.ShoppingCart{User: 900102, Goods: 421, Nums: 1, Checked: true})
	other, e := (&handler.OrderServer{}).CreateOrder(context.Background(), creationRequest(900102, request.RequestKey))
	if e != nil || other.Id == id || other.TotalCents != 1250 {
		t.Fatal(other, e)
	}
}
func TestCreationRecoveryCancelsAbandonedAndRejectsLateCallback(t *testing.T) {
	db, p := creationSetup(t)
	db.Create(&model.ShoppingCart{User: 900103, Goods: 421, Nums: 1, Checked: true})
	p.fail = true
	req := creationRequest(900103, "creation-abandoned")
	pending, e := (&handler.OrderServer{}).CreateOrder(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	db.Model(&model.OrderTransaction{}).Where("order_sn = ?", pending.OrderSn).Update("created_at", time.Now().Add(-2*time.Minute))
	if e := handler.RecoverOrders(context.Background()); e != nil {
		t.Fatal(e)
	}
	p.fail = false
	r, e := (&handler.OrderServer{}).CreateOrder(context.Background(), req)
	if e != nil || r.Status != "CREATE_CANCELLED" {
		t.Fatal(r, e)
	}
	msg := primitive.NewMessage("order-reback", []byte(fmt.Sprintf(`{"OrderSn":%q,"User":900103}`, pending.OrderSn)))
	if got := (&handler.TransactionListener{}).ExecuteLocalTransaction(msg); got != primitive.CommitMessageState {
		t.Fatal("late callback resurrected order", got)
	}
	assertOrderState(t, db, pending.OrderSn, model.OrderCancelled, 0)
	var record model.OrderTransaction
	db.Where("order_sn = ?", pending.OrderSn).First(&record)
	if !record.StockReleased {
		t.Fatal("release recovery did not converge")
	}
}
func TestEarlyTimeoutDoesNotCloseDemoOrderAndExpiryRecoveryDoes(t *testing.T) {
	db, _ := creationSetup(t)
	db.Create(&model.ShoppingCart{User: 900104, Goods: 421, Nums: 1, Checked: true})
	req := creationRequest(900104, "creation-deadline")
	r, e := (&handler.OrderServer{}).CreateOrder(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	handler.TimeOutReback(context.Background(), timeoutMessage(r.OrderSn))
	var order model.OrderInfo
	db.Where("order_sn = ?", r.OrderSn).First(&order)
	if order.Status != "WAIT_BUYER_PAY" {
		t.Fatal("early delivery closed order")
	}
	if _, e := (&handler.OrderServer{}).UpdateOrderStatus(context.Background(), &proto.OrderStatus{OrderSn: r.OrderSn, Status: "TRADE_SUCCESS"}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("demo order accepted payment", e)
	}
	db.Model(&model.OrderTransaction{}).Where("order_sn = ?", r.OrderSn).Update("expires_at", time.Now().Add(-time.Second))
	if e := handler.RecoverOrders(context.Background()); e != nil {
		t.Fatal(e)
	}
	r, e = (&handler.OrderServer{}).GetOrderCreation(context.Background(), req)
	if e != nil || r.Status != "TRADE_CLOSED" {
		t.Fatal(r, e)
	}
}
