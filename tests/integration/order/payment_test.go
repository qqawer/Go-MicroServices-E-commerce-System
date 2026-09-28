//go:build integration

package order_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"
)

// The adapter has separate RSA signed notification/response tests. These RPC
// integration tests exercise verified channel results against real MySQL locks.
type paymentGateway struct {
	query func(context.Context, string) (payment.Trade, error)
	close func(context.Context, string) error
}

func (*paymentGateway) VerifyNotification(body string) (payment.Trade, error) {
	var trade payment.Trade
	err := json.Unmarshal([]byte(body), &trade)
	return trade, err
}
func (g *paymentGateway) Query(ctx context.Context, number string) (payment.Trade, error) {
	if g.query == nil {
		return payment.Trade{}, payment.ErrNotFound
	}
	return g.query(ctx, number)
}
func (g *paymentGateway) Close(ctx context.Context, number string) error {
	if g.close == nil {
		return errors.New("close outcome unknown")
	}
	return g.close(ctx, number)
}

func paymentSetup(t *testing.T) (*gorm.DB, proto.OrderClient, *paymentGateway, *transactionProducer) {
	t.Helper()
	db, _, producer := transactionSetup(t)
	oldConfig, oldGateway := global.ServerConfig, global.PaymentGateway
	gateway := &paymentGateway{}
	global.ServerConfig = &config.ServerConfig{Alipay: payment.Config{Enabled: true, AppID: "test-app", SellerID: "test-seller"}}
	global.PaymentGateway = gateway
	t.Cleanup(func() {
		for _, table := range []string{"order_close_review", "order_payment", "order_event", "order_transaction", "orderinfo"} {
			if err := db.Exec("DELETE FROM " + table + " WHERE order_sn LIKE 'pay-%'").Error; err != nil {
				t.Error(err)
			}
		}
		global.ServerConfig, global.PaymentGateway = oldConfig, oldGateway
	})
	_, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterOrderServer(s, &handler.OrderServer{}) })
	return db, proto.NewOrderClient(conn), gateway, producer
}

func paymentOrder(t *testing.T, db *gorm.DB, number string) payment.Trade {
	t.Helper()
	cents := int64(12345)
	order := model.OrderInfo{OrderSn: number, User: 900200, PayType: "alipay", Status: "WAIT_BUYER_PAY", TotalCents: &cents, OrderMount: 123.45}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	expiredDecision(t, db, number)
	return payment.Trade{OrderSn: number, TradeNo: number + "-channel", AppID: "test-app", SellerID: "test-seller", AmountCents: cents, Status: "TRADE_SUCCESS", NotifyID: number + "-notification", PaidAt: time.Now().Add(-time.Second).Truncate(time.Second)}
}
func notifyPayment(ctx context.Context, client proto.OrderClient, trade payment.Trade) error {
	body, _ := json.Marshal(trade)
	_, err := client.ProcessPaymentNotification(ctx, &proto.PaymentNotification{Body: string(body)})
	return err
}
func paymentState(t *testing.T, db *gorm.DB, number, want string, wantLedger, wantEvents int64) model.OrderInfo {
	t.Helper()
	var order model.OrderInfo
	if err := db.Where("order_sn = ?", number).First(&order).Error; err != nil || order.Status != want {
		t.Fatalf("order=%+v err=%v want=%s", order, err, want)
	}
	var ledger, events int64
	if err := db.Model(&model.OrderPayment{}).Where("order_sn = ?", number).Count(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.OrderEvent{}).Where("order_sn = ? AND topic = ?", number, "order-reback").Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if ledger != wantLedger || events != wantEvents {
		t.Fatalf("ledger=%d events=%d want=%d/%d", ledger, events, wantLedger, wantEvents)
	}
	return order
}

func TestPaymentChecksOrderIdentityAmountAndMode(t *testing.T) {
	db, client, _, _ := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-validation")
	for _, tc := range []struct {
		name   string
		mutate func(*payment.Trade)
	}{
		{"amount", func(v *payment.Trade) { v.AmountCents++ }},
		{"exact-order-number", func(v *payment.Trade) { v.OrderSn = strings.ToUpper(v.OrderSn) }},
		{"app", func(v *payment.Trade) { v.AppID = "wrong-app" }},
		{"seller", func(v *payment.Trade) { v.SellerID = "wrong-seller" }},
		{"missing-time", func(v *payment.Trade) { v.PaidAt = time.Time{} }},
		{"unknown-status", func(v *payment.Trade) { v.Status = "SUCCESS" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := trade
			tc.mutate(&invalid)
			if err := notifyPayment(context.Background(), client, invalid); err == nil {
				t.Fatal("accepted invalid trade")
			}
			paymentState(t, db, trade.OrderSn, "WAIT_BUYER_PAY", 0, 0)
		})
	}
	if _, err := client.UpdateOrderStatus(context.Background(), &proto.OrderStatus{OrderSn: trade.OrderSn, Status: "TRADE_SUCCESS"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unverified legacy RPC accepted payment", err)
	}
	for _, mode := range []string{"demo", "alipay"} {
		if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", trade.OrderSn).Updates(map[string]interface{}{"pay_type": mode, "total_cents": nil}).Error; err != nil {
			t.Fatal(err)
		}
		if err := notifyPayment(context.Background(), client, trade); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("accepted demo or unknown historical amount", err)
		}
	}
	if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", trade.OrderSn).Updates(map[string]interface{}{"pay_type": "alipay", "total_cents": trade.AmountCents}).Error; err != nil {
		t.Fatal(err)
	}
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	order := paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	if order.TradeNo != trade.TradeNo || order.PayTime == nil {
		t.Fatal("missing payment evidence", order)
	}
}

func TestPaymentDuplicateAndReorderedNotificationsAreMonotonic(t *testing.T) {
	db, client, _, _ := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-duplicate")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- notifyPayment(context.Background(), client, trade) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"TRADE_FINISHED", "TRADE_SUCCESS", "WAIT_BUYER_PAY", "TRADE_CLOSED"} {
		reordered := trade
		reordered.Status = state
		if state == "WAIT_BUYER_PAY" || state == "TRADE_CLOSED" {
			reordered.PaidAt = time.Time{}
		}
		if err := notifyPayment(context.Background(), client, reordered); err != nil {
			t.Fatal(err)
		}
	}
	paymentState(t, db, trade.OrderSn, "TRADE_FINISHED", 1, 0)
	var record model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&record).Error; err != nil || record.Status != "TRADE_FINISHED" || record.Review {
		t.Fatal(record, err)
	}
	other := paymentOrder(t, db, "pay-reused-channel")
	other.TradeNo = trade.TradeNo
	if err := notifyPayment(context.Background(), client, other); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("channel transaction reused for a different order", err)
	}
	paymentState(t, db, other.OrderSn, "WAIT_BUYER_PAY", 0, 0)
	trade.TradeNo += "-different"
	if err := notifyPayment(context.Background(), client, trade); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("second channel transaction accepted", err)
	}
}

func TestPaymentLedgerAndOrderCommitTogether(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "lost-ack"}[committed], func(t *testing.T) {
			db, client, _, _ := paymentSetup(t)
			trade := paymentOrder(t, db, "pay-atomic")
			testdb.FailCommit(t, db, 1, committed)
			if err := notifyPayment(context.Background(), client, trade); status.Code(err) != codes.Unavailable {
				t.Fatal("unconfirmed commit must be retried", err)
			}
			if committed {
				paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
			} else {
				paymentState(t, db, trade.OrderSn, "WAIT_BUYER_PAY", 0, 0)
			}
			if err := notifyPayment(context.Background(), client, trade); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
		})
	}
}

func TestPaymentCloseKeepsUnknownResultAndRecoveryConfirmsChannel(t *testing.T) {
	for _, queryErr := range []error{payment.ErrNotFound, context.DeadlineExceeded} {
		t.Run(queryErr.Error(), func(t *testing.T) {
			db, _, gateway, producer := paymentSetup(t)
			trade := paymentOrder(t, db, "pay-close-recover")
			gateway.query = func(context.Context, string) (payment.Trade, error) { return payment.Trade{}, queryErr }
			if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage(trade.OrderSn)); result != consumer.ConsumeRetryLater {
				t.Fatal("unknown channel state was acknowledged", result)
			}
			paymentState(t, db, trade.OrderSn, "CLOSING", 0, 0)
			if len(producer.sent) != 0 {
				t.Fatal("unknown result refunded inventory")
			}
			trade.Status, trade.PaidAt = "WAIT_BUYER_PAY", time.Time{}
			gateway.query = func(context.Context, string) (payment.Trade, error) { return trade, nil }
			gateway.close = func(context.Context, string) error { trade.Status = "TRADE_CLOSED"; return context.DeadlineExceeded }
			// A lost close acknowledgment is resolved by the independent signed query.
			if err := handler.RecoverOrders(context.Background()); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || !decision.StockReleased {
				t.Fatal("release did not recover", decision, err)
			}
			if err := handler.DispatchOrderEvents(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			if len(producer.sent) != 1 {
				t.Fatal("refund event missing", producer.sent)
			}
			if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage(trade.OrderSn)); result != consumer.ConsumeSuccess {
				t.Fatal(result)
			}
			if len(producer.sent) != 1 {
				t.Fatal("repeated timeout resent confirmed event")
			}
		})
	}
}

func TestPaymentLateSuccessIsRecordedForReviewWithoutRevivingOrder(t *testing.T) {
	db, client, gateway, producer := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-late")
	closed := trade
	closed.Status, closed.PaidAt = "TRADE_CLOSED", time.Time{}
	gateway.query = func(context.Context, string) (payment.Trade, error) { return closed, nil }
	if _, err := client.UpdateOrderStatus(context.Background(), &proto.OrderStatus{OrderSn: trade.OrderSn, Status: "TRADE_CLOSED"}); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
	var record model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&record).Error; err != nil || !record.Review || record.PaidAt == nil || record.Status != "TRADE_SUCCESS" {
		t.Fatal("late payment evidence lost", record, err)
	}
	if err := handler.DispatchOrderEvents(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(producer.sent) != 0 {
		t.Fatal("paid review emitted refund")
	}
	if err := handler.RecoverOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
		t.Fatal("review allowed fallback refund", decision, err)
	}
}

func TestPaymentCallbackWinsAgainstConcurrentTimeoutQuery(t *testing.T) {
	db, client, gateway, producer := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-race")
	entered, resume := make(chan struct{}), make(chan struct{})
	gateway.query = func(ctx context.Context, _ string) (payment.Trade, error) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return payment.Trade{}, ctx.Err()
		}
		closed := trade
		closed.Status, closed.PaidAt = "TRADE_CLOSED", time.Time{}
		return closed, nil
	}
	var closeCalls atomic.Int32
	gateway.close = func(context.Context, string) error { closeCalls.Add(1); return nil }
	done := make(chan consumer.ConsumeResult, 1)
	go func() {
		result, _ := handler.TimeOutReback(context.Background(), timeoutMessage(trade.OrderSn))
		done <- result
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := notifyPayment(ctx, client, trade); err != nil {
		close(resume)
		t.Fatal("channel network call held order database lock", err)
	}
	close(resume)
	if result := <-done; result != consumer.ConsumeSuccess {
		t.Fatal(result)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	if closeCalls.Load() != 0 || len(producer.sent) != 0 {
		t.Fatal("paid order closed/refunded")
	}
}

func TestPaymentQueryFindsPaidOrderWithoutNotification(t *testing.T) {
	db, _, gateway, producer := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-query-paid")
	gateway.query = func(context.Context, string) (payment.Trade, error) { return trade, nil }
	if err := handler.RecoverOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	if len(producer.sent) != 0 {
		t.Fatal("query-confirmed payment returned inventory")
	}
}

func TestPaymentRefundedClosedEvidenceRemainsUnderReview(t *testing.T) {
	db, client, gateway, producer := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-refund-review")
	closed := trade
	closed.Status = "TRADE_CLOSED" // Retains PaidAt: this trade was previously paid.
	gateway.query = func(context.Context, string) (payment.Trade, error) { return closed, nil }
	if err := handler.RecoverOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
	var record model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&record).Error; err != nil || !record.Review || record.PaidAt == nil {
		t.Fatal("review was lost", record, err)
	}
	if len(producer.sent) != 0 {
		t.Fatal("refunded payment was treated as unpaid timeout")
	}
}

func TestPaymentCloseAcknowledgmentAloneDoesNotReleaseInventory(t *testing.T) {
	db, _, gateway, producer := paymentSetup(t)
	trade := paymentOrder(t, db, "pay-close-ack")
	trade.Status, trade.PaidAt = "WAIT_BUYER_PAY", time.Time{}
	gateway.query = func(context.Context, string) (payment.Trade, error) { return trade, nil }
	gateway.close = func(context.Context, string) error { return nil }
	if result, _ := handler.TimeOutReback(context.Background(), timeoutMessage(trade.OrderSn)); result != consumer.ConsumeRetryLater {
		t.Fatal("close acknowledgment became proof of closure", result)
	}
	paymentState(t, db, trade.OrderSn, "CLOSING", 1, 0)
	if len(producer.sent) != 0 {
		t.Fatal("unconfirmed closure refunded inventory")
	}
}

func TestAlipayCreationKeepsExactAmountAndPaymentDeadline(t *testing.T) {
	db, _ := creationSetup(t)
	oldGateway := global.PaymentGateway
	t.Cleanup(func() { global.PaymentGateway = oldGateway })
	global.ServerConfig.MqInfo.Mode = "alipay"
	global.ServerConfig.Alipay = payment.Config{Enabled: true, AppID: "test-app", SellerID: "test-seller"}
	global.PaymentGateway = &paymentGateway{}
	if err := db.Create(&model.ShoppingCart{User: 900210, Goods: 421, Nums: 2, Checked: true}).Error; err != nil {
		t.Fatal(err)
	}
	req := creationRequest(900210, "creation-alipay")
	server := &handler.OrderServer{}
	created, err := server.CreateOrder(context.Background(), req)
	if err != nil || created.PayType != "alipay" || created.Status != "WAIT_BUYER_PAY" || created.TotalCents != 2500 || created.ExpiresAt <= time.Now().Unix() {
		t.Fatal(created, err)
	}
	detail, err := server.OrderDetail(context.Background(), &proto.OrderRequest{Id: created.Id, UserId: req.UserId})
	if err != nil || detail.OrderInfo.ExpiresAt != created.ExpiresAt || detail.OrderInfo.TotalCents != created.TotalCents {
		t.Fatal(detail, err)
	}
	global.PaymentGateway = nil
	if err := db.Create(&model.ShoppingCart{User: 900211, Goods: 421, Nums: 2, Checked: true}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := server.CreateOrder(context.Background(), creationRequest(900211, "creation-alipay-unready")); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unconfigured payment accepted new order", err)
	}
}

type expensiveGoods struct{ proto.GoodsClient }

func (*expensiveGoods) BatchGetGoods(context.Context, *proto.BatchGoodsIdInfo, ...grpc.CallOption) (*proto.GoodsListResponse, error) {
	return &proto.GoodsListResponse{Data: []*proto.GoodsInfoResponse{{Id: 421, Name: "expensive", ShopPrice: 1e9, OnSale: true, InventoryReady: true}}}, nil
}
func TestAlipayCreationRejectsAmountsAboveProviderLimit(t *testing.T) {
	db, _ := creationSetup(t)
	oldGateway, oldGoods := global.PaymentGateway, global.GoodsSrvClient
	t.Cleanup(func() { global.PaymentGateway, global.GoodsSrvClient = oldGateway, oldGoods })
	global.ServerConfig.MqInfo.Mode = "alipay"
	global.ServerConfig.Alipay = payment.Config{Enabled: true, AppID: "test-app", SellerID: "test-seller"}
	global.PaymentGateway, global.GoodsSrvClient = &paymentGateway{}, &expensiveGoods{}
	if err := db.Create(&model.ShoppingCart{User: 900212, Goods: 421, Nums: 1, Checked: true}).Error; err != nil {
		t.Fatal(err)
	}
	req := creationRequest(900212, "creation-alipay-limit")
	if _, err := (&handler.OrderServer{}).CreateOrder(context.Background(), req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("provider-incompatible amount accepted", err)
	}
	var count int64
	if err := db.Model(&model.OrderTransaction{}).Where("user_id = ?", req.UserId).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid amount prepared inventory transaction", count, err)
	}
}

func TestPaymentRefundEvidenceWithoutPaymentTimeStopsFulfillment(t *testing.T) {
	for _, fromQuery := range []bool{false, true} {
		t.Run(map[bool]string{false: "notification", true: "query"}[fromQuery], func(t *testing.T) {
			db, client, gateway, producer := paymentSetup(t)
			trade := paymentOrder(t, db, "pay-refund-without-time")
			closed := trade
			closed.Status, closed.PaidAt, closed.Refunded = "TRADE_CLOSED", time.Time{}, true
			if fromQuery {
				gateway.query = func(context.Context, string) (payment.Trade, error) { return closed, nil }
				if err := handler.RecoverOrders(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := notifyPayment(context.Background(), client, trade); err != nil {
					t.Fatal(err)
				}
				if err := notifyPayment(context.Background(), client, closed); err != nil {
					t.Fatal(err)
				}
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
			if err := notifyPayment(context.Background(), client, trade); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
			if len(producer.sent) != 0 {
				t.Fatal("refund evidence triggered inventory return")
			}
		})
	}
}

func TestPaymentRecoveryRotatesMoreThanOneBatchOfUnknownTrades(t *testing.T) {
	db, _, gateway, _ := paymentSetup(t)
	base := time.Now().Add(-time.Hour)
	var ready payment.Trade
	for n := 0; n < 101; n++ {
		trade := paymentOrder(t, db, fmt.Sprintf("pay-fair-%03d", n))
		if err := db.Model(&model.OrderTransaction{}).Where("order_sn = ?", trade.OrderSn).UpdateColumn("updated_at", base.Add(time.Duration(n)*time.Millisecond)).Error; err != nil {
			t.Fatal(err)
		}
		if n == 100 {
			ready = trade
			ready.Status, ready.PaidAt = "TRADE_CLOSED", time.Time{}
		}
	}
	gateway.query = func(_ context.Context, number string) (payment.Trade, error) {
		if number == ready.OrderSn {
			return ready, nil
		}
		return payment.Trade{}, payment.ErrNotFound
	}
	if err := handler.RecoverOrders(context.Background()); err == nil {
		t.Fatal("unknown trades must remain pending")
	}
	paymentState(t, db, ready.OrderSn, "WAIT_BUYER_PAY", 0, 0)
	_ = handler.RecoverOrders(context.Background())
	paymentState(t, db, ready.OrderSn, "TRADE_CLOSED", 1, 1)
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", ready.OrderSn).First(&decision).Error; err != nil || !decision.StockReleased {
		t.Fatal("known order starved behind unknown trades", decision, err)
	}
}

func TestPaymentSlowQueryRotatesBeforeNextRecoveryAndDoesNotTouchTail(t *testing.T) {
	db, _, gateway, _ := paymentSetup(t)
	slow := paymentOrder(t, db, "pay-slow-query")
	ready := paymentOrder(t, db, "pay-ready-query")
	base := time.Now().Add(-time.Hour)
	for n, number := range []string{slow.OrderSn, ready.OrderSn} {
		if err := db.Model(&model.OrderTransaction{}).Where("order_sn = ?", number).UpdateColumn("updated_at", base.Add(time.Duration(n)*time.Second)).Error; err != nil {
			t.Fatal(err)
		}
	}
	var original model.OrderTransaction
	if err := db.Where("order_sn = ?", ready.OrderSn).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	gateway.query = func(ctx context.Context, number string) (payment.Trade, error) {
		if number == slow.OrderSn {
			<-ctx.Done()
			return payment.Trade{}, ctx.Err()
		}
		closed := ready
		closed.Status, closed.PaidAt = "TRADE_CLOSED", time.Time{}
		return closed, nil
	}
	for n := 0; n < 2; n++ {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		_ = handler.RecoverOrders(ctx)
		cancel()
		if n == 0 {
			var untouched model.OrderTransaction
			if err := db.Where("order_sn = ?", ready.OrderSn).First(&untouched).Error; err != nil || !untouched.UpdatedAt.Equal(original.UpdatedAt) {
				t.Fatal("unattempted tail was rotated", untouched, err)
			}
			paymentState(t, db, ready.OrderSn, "WAIT_BUYER_PAY", 0, 0)
		}
	}
	paymentState(t, db, ready.OrderSn, "TRADE_CLOSED", 1, 1)
	// The next pass releases the already closed order before the slow channel call.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = handler.RecoverOrders(ctx)
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", ready.OrderSn).First(&decision).Error; err != nil || !decision.StockReleased {
		t.Fatal("release phase starved behind channel timeout", decision, err)
	}
}

func TestPaymentReviewOutboxDoesNotBlockLaterRefunds(t *testing.T) {
	db, _, _, producer := paymentSetup(t)
	for n := 0; n < 101; n++ {
		trade := paymentOrder(t, db, fmt.Sprintf("pay-outbox-review-%03d", n))
		if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", trade.OrderSn).Update("status", "TRADE_CLOSED").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.OrderPayment{OrderSn: trade.OrderSn, TradeNo: trade.TradeNo, AppID: trade.AppID, SellerID: trade.SellerID, AmountCents: trade.AmountCents, Status: "TRADE_SUCCESS", Review: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.OrderEvent{OrderSn: trade.OrderSn, Topic: "order-reback", Body: fmt.Sprintf(`{"OrderSn":%q}`, trade.OrderSn)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ready := paymentOrder(t, db, "pay-outbox-ready")
	if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", ready.OrderSn).Update("status", "TRADE_CLOSED").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.OrderEvent{OrderSn: ready.OrderSn, Topic: "order-reback", Body: fmt.Sprintf(`{"OrderSn":%q}`, ready.OrderSn)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := handler.DispatchOrderEvents(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(producer.sent) != 1 || !strings.Contains(producer.sent[0], ready.OrderSn) {
		t.Fatal("review queue starved eligible refund", producer.sent)
	}
}

type slowPaymentProducer struct {
	*transactionProducer
	slowOrder string
}

func (p *slowPaymentProducer) SendSync(ctx context.Context, msgs ...*primitive.Message) (*primitive.SendResult, error) {
	for _, msg := range msgs {
		if strings.Contains(string(msg.Body), p.slowOrder) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return p.transactionProducer.SendSync(ctx, msgs...)
}
func TestPaymentSlowOutboxSendRotatesAndDoesNotTouchTail(t *testing.T) {
	db, _, _, producer := paymentSetup(t)
	for _, number := range []string{"pay-send-slow", "pay-send-ready"} {
		if err := db.Create(&model.OrderEvent{OrderSn: number, Topic: "order_timeout", Body: fmt.Sprintf(`{"OrderSn":%q}`, number)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	global.MQOrder = &slowPaymentProducer{transactionProducer: producer, slowOrder: "pay-send-slow"}
	for n := 0; n < 2; n++ {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		_ = handler.DispatchOrderEvents(ctx, 100)
		cancel()
		if n == 0 {
			var untouched model.OrderEvent
			if err := db.Where("order_sn = ?", "pay-send-ready").First(&untouched).Error; err != nil || untouched.LastAttemptAt != nil {
				t.Fatal("unattempted event was rotated", untouched, err)
			}
		}
	}
	if len(producer.sent) != 1 || !strings.Contains(producer.sent[0], "pay-send-ready") {
		t.Fatal("slow sender starved later event", producer.sent)
	}
}

type slowReleaseStock struct{ proto.InventoryClient }

func (*slowReleaseStock) Reback(ctx context.Context, _ *proto.SellInfo, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestPaymentSlowInventoryReleaseDoesNotStarvePaymentQuery(t *testing.T) {
	db, _, gateway, _ := paymentSetup(t)
	closed := paymentOrder(t, db, "pay-slow-release")
	if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", closed.OrderSn).Update("status", "TRADE_CLOSED").Error; err != nil {
		t.Fatal(err)
	}
	paid := paymentOrder(t, db, "pay-query-during-release-outage")
	global.InventorySrvClient = &slowReleaseStock{}
	gateway.query = func(context.Context, string) (payment.Trade, error) { return paid, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	if err := handler.RecoverOrders(ctx); err == nil {
		t.Fatal("inventory outage should remain pending")
	}
	paymentState(t, db, paid.OrderSn, "TRADE_SUCCESS", 1, 0)
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", closed.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
		t.Fatal("unconfirmed inventory release was marked complete", decision, err)
	}
}
