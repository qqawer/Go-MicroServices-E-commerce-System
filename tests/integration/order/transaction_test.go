//go:build integration

package order_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"
)

type transactionGoods struct{ proto.GoodsClient }

func (*transactionGoods) BatchGetGoods(context.Context, *proto.BatchGoodsIdInfo, ...grpc.CallOption) (*proto.GoodsListResponse, error) {
	return &proto.GoodsListResponse{Data: []*proto.GoodsInfoResponse{{Id: 421, Name: "transaction goods", ShopPrice: 12.5, OnSale: true, InventoryReady: true}}}, nil
}

type transactionStock struct {
	proto.InventoryClient
	sell  func(context.Context, *proto.SellInfo) error
	calls int
	mu    sync.Mutex
}

func (f *transactionStock) Sell(ctx context.Context, r *proto.SellInfo, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.sell != nil {
		return nil, f.sell(ctx, r)
	}
	return &emptypb.Empty{}, nil
}

type transactionProducer struct {
	rocketmq.Producer
	mu   sync.Mutex
	fail bool
	sent []string
}

func (f *transactionProducer) SendSync(_ context.Context, msgs ...*primitive.Message) (*primitive.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, msg := range msgs {
		f.sent = append(f.sent, msg.Topic+":"+string(msg.Body))
	}
	if f.fail {
		return nil, errors.New("broker response lost")
	}
	return &primitive.SendResult{Status: primitive.SendOK}, nil
}
func transactionSetup(t *testing.T) (*gorm.DB, *transactionStock, *transactionProducer) {
	t.Helper()
	db := testdb.Open(t, "grpctest_orders_srv")
	previousDB, previousGoods, previousStock := global.DB, global.GoodsSrvClient, global.InventorySrvClient
	previousOrderMQ, previousStockMQ := global.MQOrder, global.MQInventory
	stock, producer := &transactionStock{}, &transactionProducer{}
	global.DB, global.GoodsSrvClient, global.InventorySrvClient = db, &transactionGoods{}, stock
	global.MQOrder, global.MQInventory = producer, producer
	t.Cleanup(func() {
		db.Exec("DELETE FROM ordergoods WHERE `order` IN (SELECT id FROM orderinfo WHERE order_sn LIKE 'tx-%')")
		db.Exec("DELETE FROM orderinfo WHERE order_sn LIKE 'tx-%'")
		db.Exec("DELETE FROM order_event WHERE order_sn LIKE 'tx-%'")
		db.Exec("DELETE FROM order_transaction WHERE order_sn LIKE 'tx-%'")
		db.Unscoped().Where("user >= ?", 900001).Delete(&model.ShoppingCart{})
		global.DB, global.GoodsSrvClient, global.InventorySrvClient = previousDB, previousGoods, previousStock
		global.MQOrder, global.MQInventory = previousOrderMQ, previousStockMQ
	})
	return db, stock, producer
}
func orderMessage(number string, user int32) *primitive.Message {
	var cart []model.ShoppingCart
	global.DB.Where("user = ? AND checked = ?", user, true).Find(&cart)
	goods := []model.OrderGoods{}
	stock := []*proto.GoodsInventoryInfo{}
	for _, c := range cart {
		goods = append(goods, model.OrderGoods{Goods: c.Goods, GoodsName: "test", GoodsPrice: 12.5, Nums: c.Nums})
		stock = append(stock, &proto.GoodsInventoryInfo{GoodsId: c.Goods, Num: c.Nums})
	}
	raw, _ := json.Marshal(map[string]interface{}{"Order": model.OrderInfo{OrderSn: number, User: user, OrderMount: 25}, "Cart": cart, "Goods": goods, "Stock": stock})
	snapshot := string(raw)
	expiry := time.Now().Add(time.Hour)
	global.DB.Create(&model.OrderTransaction{OrderSn: number, Snapshot: &snapshot, ExpiresAt: &expiry})
	body, _ := json.Marshal(model.OrderInfo{OrderSn: number, User: user})
	return primitive.NewMessage("order-reback", body)
}
func timeoutMessage(number string) *primitive.MessageExt {
	return &primitive.MessageExt{Message: primitive.Message{Body: []byte(fmt.Sprintf(`{"OrderSn":%q}`, number))}}
}
func assertOrderState(t *testing.T, db *gorm.DB, number string, state int32, count int64) {
	t.Helper()
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", number).First(&decision).Error; err != nil || decision.State != state {
		t.Fatalf("decision: %v %v", decision, err)
	}
	var actual int64
	db.Model(&model.OrderInfo{}).Where("order_sn = ?", number).Count(&actual)
	if actual != count {
		t.Fatalf("orders=%d want=%d", actual, count)
	}
}
func TestOrderSellErrorPublishesDurableCancellation(t *testing.T) {
	db, stock, _ := transactionSetup(t)
	db.Create(&model.ShoppingCart{User: 900001, Goods: 421, Nums: 2, Checked: true})
	stock.sell = func(context.Context, *proto.SellInfo) error {
		return status.Error(codes.DeadlineExceeded, "stock committed but response lost")
	}
	listener := &handler.OrderListener{Ctx: context.Background()}
	msg := orderMessage("tx-lost-stock-reply", 900001)
	state := listener.ExecuteLocalTransaction(msg)
	if state != primitive.CommitMessageState || listener.Code == codes.OK || stock.calls != 1 {
		t.Fatalf("compensation discarded: %v %v", state, listener.Code)
	}
	assertOrderState(t, db, "tx-lost-stock-reply", model.OrderCancelled, 0)
	// A new listener (no in-memory execution state) makes the same decision.
	if got := (&handler.OrderListener{}).CheckLocalTransaction(&primitive.MessageExt{Message: *msg}); got != primitive.CommitMessageState {
		t.Fatal(got)
	}
}
func TestOrderCheckFencesLateExecution(t *testing.T) {
	db, stock, _ := transactionSetup(t)
	db.Create(&model.ShoppingCart{User: 900002, Goods: 421, Nums: 2, Checked: true})
	entered, resume := make(chan struct{}), make(chan struct{})
	stock.sell = func(context.Context, *proto.SellInfo) error { close(entered); <-resume; return nil }
	msg := orderMessage("tx-check-race", 900002)
	listener := &handler.OrderListener{}
	done := make(chan primitive.LocalTransactionState, 1)
	go func() { done <- listener.ExecuteLocalTransaction(msg) }()
	<-entered
	state := (&handler.OrderListener{}).CheckLocalTransaction(&primitive.MessageExt{Message: *msg})
	close(resume)
	if state != primitive.CommitMessageState || <-done != primitive.CommitMessageState {
		t.Fatal("late creation bypassed cancellation")
	}
	assertOrderState(t, db, "tx-check-race", model.OrderCancelled, 0)
	early := orderMessage("tx-check-first", 900002)
	if state := (&handler.OrderListener{}).CheckLocalTransaction(&primitive.MessageExt{Message: *early}); state != primitive.CommitMessageState {
		t.Fatal(state)
	}
	if state := (&handler.OrderListener{}).ExecuteLocalTransaction(early); state != primitive.CommitMessageState {
		t.Fatal(state)
	}
	if stock.calls != 1 {
		t.Fatal("cancelled-before-execution still called stock")
	}
}

func TestOrderLostCommitReplyDoesNotRefundCommittedOrder(t *testing.T) {
	db, _, producer := transactionSetup(t)
	db.Create(&model.ShoppingCart{User: 900003, Goods: 421, Nums: 2, Checked: true})
	msg := orderMessage("tx-order-commit-lost", 900003)
	testdb.FailCommit(t, db, 2, true) // prepare decision, then business commit
	listener := &handler.OrderListener{}
	state := listener.ExecuteLocalTransaction(msg)
	if state != primitive.RollbackMessageState || listener.Code != codes.OK || listener.ID <= 0 {
		t.Fatalf("committed order was cancelled: %v code=%v", state, listener.Code)
	}
	assertOrderState(t, db, "tx-order-commit-lost", model.OrderCommitted, 1)
	if state := (&handler.OrderListener{}).CheckLocalTransaction(&primitive.MessageExt{Message: *msg}); state != primitive.RollbackMessageState {
		t.Fatal("check refunded existing order", state)
	}
	if len(producer.sent) != 1 {
		t.Fatalf("timeout message count=%d", len(producer.sent))
	}
}
func TestTimeoutRefundPublishesOnlyAfterBusinessCommit(t *testing.T) {
	db, _, producer := transactionSetup(t)
	db.Create(&model.OrderInfo{OrderSn: "tx-close-rollback", User: 900004, Status: "WAIT_BUYER_PAY"})
	testdb.FailCommit(t, db, 1, false)
	result, err := handler.TimeOutReback(context.Background(), timeoutMessage("tx-close-rollback"))
	if result != consumer.ConsumeRetryLater || err != nil {
		t.Fatal(result, err)
	}
	var order model.OrderInfo
	db.Where("order_sn = ?", "tx-close-rollback").First(&order)
	var events int64
	db.Model(&model.OrderEvent{}).Where("order_sn = ?", order.OrderSn).Count(&events)
	if order.Status != "WAIT_BUYER_PAY" || events != 0 || len(producer.sent) != 0 {
		t.Fatal("refund escaped rolled-back closing transaction")
	}
}
func TestPendingRefundSurvivesSendFailureAndNewDispatcher(t *testing.T) {
	db, _, producer := transactionSetup(t)
	db.Create(&model.OrderInfo{OrderSn: "tx-outbox-retry", User: 900005, Status: "WAIT_BUYER_PAY"})
	producer.fail = true
	result, _ := handler.TimeOutReback(context.Background(), timeoutMessage("tx-outbox-retry"))
	if result != consumer.ConsumeRetryLater {
		t.Fatal("failed broker send was acknowledged")
	}
	var order model.OrderInfo
	db.Where("order_sn = ?", "tx-outbox-retry").First(&order)
	var event model.OrderEvent
	db.Where("order_sn = ?", order.OrderSn).First(&event)
	if order.Status != "TRADE_CLOSED" || event.Published {
		t.Fatal("closing and pending intent were not persisted")
	}
	replacement := &transactionProducer{}
	global.MQInventory = replacement
	if err := handler.DispatchOrderEvents(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(replacement.sent) != 1 {
		t.Fatal("pending event was lost across dispatcher replacement")
	}
	if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage(order.OrderSn)); result != consumer.ConsumeSuccess {
		t.Fatal(result)
	}
	if len(replacement.sent) != 1 {
		t.Fatal("completed event was unnecessarily resent")
	}
	// A late payment notification cannot overwrite the local closed state.
	_, err := (&handler.OrderServer{}).UpdateOrderStatus(context.Background(), &proto.OrderStatus{OrderSn: order.OrderSn, Status: "TRADE_SUCCESS"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal("late payment revived closed order", err)
	}
}
func TestTimeoutBatchSkipsPaidAndClosesBothUnpaidOrders(t *testing.T) {
	db, _, producer := transactionSetup(t)
	for _, number := range []string{"tx-batch-a", "tx-batch-b", "tx-batch-paid"} {
		state := "WAIT_BUYER_PAY"
		if number == "tx-batch-paid" {
			state = "TRADE_SUCCESS"
		}
		db.Create(&model.OrderInfo{OrderSn: number, User: 900006, Status: state})
	}
	msgs := []*primitive.MessageExt{timeoutMessage("tx-batch-paid"), timeoutMessage("tx-batch-a"), timeoutMessage("tx-batch-b")}
	for n := 0; n < 2; n++ {
		result, _ := handler.TimeOutReback(context.Background(), msgs...)
		if result != consumer.ConsumeSuccess {
			t.Fatal(result)
		}
	}
	if len(producer.sent) != 2 {
		t.Fatalf("refund sends=%d want=2", len(producer.sent))
	}
}
func TestOrderQueryErrorKeepsMessageUnknown(t *testing.T) {
	db, _, _ := transactionSetup(t)
	name := "test:order_query_failure"
	db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) { tx.AddError(errors.New("injected query failure")) })
	defer db.Callback().Query().Remove(name)
	if state := (&handler.OrderListener{}).CheckLocalTransaction(timeoutMessage("tx-query-failure")); state != primitive.UnkonwnState {
		t.Fatal("query failure became a final decision", state)
	}
}

func TestTimeoutLostCommitReplyRetriesPersistedRefund(t *testing.T) {
	db, _, producer := transactionSetup(t)
	db.Create(&model.OrderInfo{OrderSn: "tx-close-commit-lost", User: 900007, Status: "WAIT_BUYER_PAY"})
	testdb.FailCommit(t, db, 1, true)
	if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage("tx-close-commit-lost")); result != consumer.ConsumeRetryLater {
		t.Fatal(result)
	}
	if len(producer.sent) != 0 {
		t.Fatal("sent before closing commit was confirmed")
	}
	if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage("tx-close-commit-lost")); result != consumer.ConsumeSuccess {
		t.Fatal(result)
	}
	if len(producer.sent) != 1 {
		t.Fatal("lost refund after unknown closing commit")
	}
}

func (f *transactionStock) Reback(context.Context, *proto.SellInfo, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
