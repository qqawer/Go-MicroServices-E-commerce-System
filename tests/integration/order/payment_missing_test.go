//go:build integration

package order_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
	"grpctest/order_srv/proto"
)

func missingPaymentSetup(t *testing.T, number string) (*gorm.DB, proto.OrderClient, *paymentGateway, payment.Trade) {
	t.Helper()
	db, client, gateway, _ := paymentSetup(t)
	trade := paymentOrder(t, db, number)
	expired := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&model.OrderTransaction{}).Where("order_sn = ?", number).Update("expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	gateway.query = func(context.Context, string) (payment.Trade, error) { return payment.Trade{}, payment.ErrNotFound }
	gateway.close = func(context.Context, string) error { return payment.ErrNotFound }
	return db, client, gateway, trade
}

func checkMissingReview(t *testing.T, db *gorm.DB, number string) model.OrderCloseReview {
	t.Helper()
	paymentState(t, db, number, "PAYMENT_REVIEW", 0, 0)
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", number).First(&decision).Error; err != nil || decision.StockReleased {
		t.Fatalf("missing trade released inventory: released=%v err=%v", decision.StockReleased, err)
	}
	var review model.OrderCloseReview
	if err := db.Where("order_sn = ?", number).First(&review).Error; err != nil || review.ResolvedAt != nil || review.Reason == "" || review.Attempts < 1 || review.FirstSeenAt.IsZero() || review.LastCheckedAt.IsZero() || !review.NextCheckAt.After(review.LastCheckedAt) {
		t.Fatalf("missing durable close review: review=%+v err=%v", review, err)
	}
	return review
}

func TestPaymentExpiredMissingTradeEntersReviewWithoutReleasingInventory(t *testing.T) {
	db, _, gateway, trade := missingPaymentSetup(t, "pay-expired-missing")
	var queries, closes, creates atomic.Int32
	gateway.query = func(context.Context, string) (payment.Trade, error) {
		queries.Add(1)
		return payment.Trade{}, payment.ErrNotFound
	}
	gateway.close = func(context.Context, string) error { closes.Add(1); return payment.ErrNotFound }
	global.PaymentGateway = &missingClosureGateway{paymentGateway: gateway, create: func(context.Context, payment.ClosureRequest) error {
		creates.Add(1)
		return errors.New("background recovery must not create a provider transaction")
	}}
	_ = handler.RecoverOrders(context.Background())
	review := checkMissingReview(t, db, trade.OrderSn)
	if queries.Load() < 2 || closes.Load() == 0 {
		t.Fatal("missing trade was not queried again after a close attempt")
	}
	beforeQueries, beforeCloses := queries.Load(), closes.Load()
	_ = handler.RecoverOrders(context.Background())
	second := checkMissingReview(t, db, trade.OrderSn)
	if queries.Load() != beforeQueries || closes.Load() != beforeCloses || second.Attempts != review.Attempts {
		t.Fatal("recovery ignored the persisted next check time")
	}
	if creates.Load() != 0 {
		t.Fatal("background recovery created a provider transaction without an operator request")
	}
}

func TestPaymentMissingEscalationUsesElapsedTimeWithoutTreatingItAsClosure(t *testing.T) {
	for _, age := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			db, _, _, trade := missingPaymentSetup(t, "pay-missing-threshold")
			expired := time.Now().Add(-age)
			if err := db.Model(&model.OrderTransaction{}).Where("order_sn = ?", trade.OrderSn).Update("expires_at", expired).Error; err != nil {
				t.Fatal(err)
			}
			_ = handler.RecoverOrders(context.Background())
			want := "CLOSING"
			if age > 15*time.Minute {
				want = "PAYMENT_REVIEW"
			}
			paymentState(t, db, trade.OrderSn, want, 0, 0)
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
				t.Fatalf("elapsed time was treated as closure evidence: %+v %v", decision, err)
			}
		})
	}
}

func TestPaymentMissingReviewAcceptsLaterVerifiedOutcome(t *testing.T) {
	for _, outcome := range []string{"TRADE_SUCCESS", "TRADE_CLOSED"} {
		t.Run(outcome, func(t *testing.T) {
			db, client, _, trade := missingPaymentSetup(t, "pay-review-later")
			_ = handler.RecoverOrders(context.Background())
			checkMissingReview(t, db, trade.OrderSn)
			trade.Status = outcome
			var events int64
			if outcome == "TRADE_CLOSED" {
				trade.PaidAt = time.Time{}
				events = 1
			}
			if err := notifyPayment(context.Background(), client, trade); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, outcome, 1, events)
			var review model.OrderCloseReview
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&review).Error; err != nil || review.ResolvedAt == nil || review.Resolution == "" {
				t.Fatalf("verified outcome did not resolve missing trade review: %+v %v", review, err)
			}
			if err := handler.RecoverOrders(context.Background()); err != nil {
				t.Fatal(err)
			}
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased != (outcome == "TRADE_CLOSED") {
				t.Fatalf("wrong inventory release after %s: %+v %v", outcome, decision, err)
			}
		})
	}
}

func TestPaymentMissingReviewDoesNotClearPaymentRefundReview(t *testing.T) {
	db, client, _, trade := missingPaymentSetup(t, "pay-review-refunded")
	_ = handler.RecoverOrders(context.Background())
	checkMissingReview(t, db, trade.OrderSn)
	refunded := trade
	refunded.Status, refunded.Refunded = "TRADE_CLOSED", true
	if err := notifyPayment(context.Background(), client, refunded); err != nil {
		t.Fatal(err)
	}
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
	var ledger model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&ledger).Error; err != nil || !ledger.Review {
		t.Fatalf("refund review was cleared: %+v %v", ledger, err)
	}
	_ = handler.RecoverOrders(context.Background())
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
		t.Fatalf("refund review released inventory: %+v %v", decision, err)
	}
}

func TestPaymentMissingQueryRechecksChannelAfterClose(t *testing.T) {
	db, _, gateway, trade := missingPaymentSetup(t, "pay-missing-create-race")
	closed := trade
	closed.Status, closed.PaidAt = "TRADE_CLOSED", time.Time{}
	var closeCalled bool
	gateway.query = func(context.Context, string) (payment.Trade, error) {
		if closeCalled {
			return closed, nil
		}
		return payment.Trade{}, payment.ErrNotFound
	}
	gateway.close = func(context.Context, string) error { closeCalled = true; return nil }
	if err := handler.RecoverOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
}

// A separate optional interface models the operator-only create-and-close fence.
// Normal recovery must never manufacture a provider transaction on its own.
type missingClosureGateway struct {
	*paymentGateway
	create func(context.Context, payment.ClosureRequest) error
}

var _ payment.ClosureCreator = (*missingClosureGateway)(nil)

func (g *missingClosureGateway) CreateForClosure(ctx context.Context, request payment.ClosureRequest) error {
	return g.create(ctx, request)
}

func fenceRequest(t *testing.T, db *gorm.DB, number string) handler.FenceCloseRequest {
	t.Helper()
	var order model.OrderInfo
	if err := db.Where("order_sn = ?", number).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	return handler.FenceCloseRequest{OrderID: order.ID, RequestID: "integration-fence-001", Operator: "integration-test", BuyerID: "2088000000000001"}
}

func TestPaymentManualFenceClosesAndReleasesOnlyOnce(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed-create", true: "lost-create-ack"}[lostAck], func(t *testing.T) {
			db, _, gateway, trade := missingPaymentSetup(t, "pay-manual-fence")
			_ = handler.RecoverOrders(context.Background())
			checkMissingReview(t, db, trade.OrderSn)
			var current *payment.Trade
			var creates atomic.Int32
			gateway.query = func(context.Context, string) (payment.Trade, error) {
				if current == nil {
					return payment.Trade{}, payment.ErrNotFound
				}
				return *current, nil
			}
			gateway.close = func(context.Context, string) error {
				if current == nil {
					return payment.ErrNotFound
				}
				current.Status = "TRADE_CLOSED"
				return nil
			}
			global.PaymentGateway = &missingClosureGateway{paymentGateway: gateway, create: func(context.Context, payment.ClosureRequest) error {
				creates.Add(1)
				waiting := trade
				waiting.Status, waiting.PaidAt = "WAIT_BUYER_PAY", time.Time{}
				current = &waiting
				if lostAck {
					return context.DeadlineExceeded
				}
				return nil
			}}
			request := fenceRequest(t, db, trade.OrderSn)
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := handler.FenceAndCloseOrder(context.Background(), request); err != nil {
					t.Fatalf("fence attempt %d: %v", attempt+1, err)
				}
				paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
			}
			if creates.Load() != 1 {
				t.Fatalf("idempotent operator request created %d transactions", creates.Load())
			}
			if err := handler.RecoverOrders(context.Background()); err != nil {
				t.Fatal(err)
			}
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || !decision.StockReleased {
				t.Fatalf("confirmed closed transaction did not release stock: %+v %v", decision, err)
			}
		})
	}
}

func TestPaymentManualFenceUnknownOutcomeKeepsStockAndRejectsChangedIdentity(t *testing.T) {
	db, _, gateway, trade := missingPaymentSetup(t, "pay-manual-unknown")
	_ = handler.RecoverOrders(context.Background())
	checkMissingReview(t, db, trade.OrderSn)
	var creates atomic.Int32
	request := fenceRequest(t, db, trade.OrderSn)
	var claimVisible atomic.Bool
	global.PaymentGateway = &missingClosureGateway{paymentGateway: gateway, create: func(context.Context, payment.ClosureRequest) error {
		creates.Add(1)
		var claimed model.OrderCloseReview
		if err := db.Where("order_sn = ?", trade.OrderSn).First(&claimed).Error; err == nil && claimed.RequestID == request.RequestID && claimed.RequestHash != "" && claimed.ActionStartedAt != nil {
			claimVisible.Store(true)
		}
		return errors.New("create outcome unknown")
	}}
	if _, err := handler.FenceAndCloseOrder(context.Background(), request); err == nil {
		t.Fatal("unknown creation outcome reported successful closure")
	}
	if !claimVisible.Load() {
		t.Fatal("provider creation started before the operator claim committed")
	}
	checkMissingReview(t, db, trade.OrderSn)
	var persisted model.OrderCloseReview
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&persisted).Error; err != nil || persisted.RequestID != request.RequestID || persisted.RequestHash == "" || persisted.Operator != request.Operator || persisted.ActionStartedAt == nil {
		t.Fatalf("operator request claim not durable: %+v %v", persisted, err)
	}
	for _, mutate := range []func(*handler.FenceCloseRequest){
		func(r *handler.FenceCloseRequest) { r.BuyerID = "2088000000000002" },
		func(r *handler.FenceCloseRequest) { r.Operator = "other-operator" },
		func(r *handler.FenceCloseRequest) { r.RequestID = "integration-fence-002" },
	} {
		changed := request
		mutate(&changed)
		if _, err := handler.FenceAndCloseOrder(context.Background(), changed); err == nil {
			t.Fatal("changed operator request replaced an unresolved claimed action")
		}
	}
	if creates.Load() != 1 {
		t.Fatal("conflicting operator request reached provider creation")
	}
	checkMissingReview(t, db, trade.OrderSn)
	// The original create committed but became visible only on a later query.
	waiting := trade
	waiting.Status, waiting.PaidAt = "WAIT_BUYER_PAY", time.Time{}
	gateway.query = func(context.Context, string) (payment.Trade, error) { return waiting, nil }
	gateway.close = func(context.Context, string) error { waiting.Status = "TRADE_CLOSED"; return nil }
	if _, err := handler.FenceAndCloseOrder(context.Background(), request); err != nil {
		t.Fatal("identical operator request did not resume its uncertain result", err)
	}
	if creates.Load() != 1 {
		t.Fatal("resuming a visible trade created another provider transaction")
	}
	paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
}

func TestPaymentManualFenceStaleClosedQueryDoesNotOverridePaidCallback(t *testing.T) {
	db, client, gateway, trade := missingPaymentSetup(t, "pay-fence-callback-race")
	_ = handler.RecoverOrders(context.Background())
	checkMissingReview(t, db, trade.OrderSn)
	var mu sync.Mutex
	var current *payment.Trade
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	gateway.query = func(ctx context.Context, _ string) (payment.Trade, error) {
		mu.Lock()
		if current == nil {
			mu.Unlock()
			return payment.Trade{}, payment.ErrNotFound
		}
		result := *current
		mu.Unlock()
		if result.Status == "TRADE_CLOSED" {
			once.Do(func() { close(entered) })
			select {
			case <-resume:
			case <-ctx.Done():
				return payment.Trade{}, ctx.Err()
			}
		}
		return result, nil
	}
	gateway.close = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if current == nil {
			return payment.ErrNotFound
		}
		current.Status = "TRADE_CLOSED"
		return nil
	}
	global.PaymentGateway = &missingClosureGateway{paymentGateway: gateway, create: func(context.Context, payment.ClosureRequest) error {
		mu.Lock()
		defer mu.Unlock()
		waiting := trade
		waiting.Status, waiting.PaidAt = "WAIT_BUYER_PAY", time.Time{}
		current = &waiting
		return nil
	}}
	request := fenceRequest(t, db, trade.OrderSn)
	done := make(chan error, 1)
	fenceCtx, cancelFence := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFence()
	go func() { _, err := handler.FenceAndCloseOrder(fenceCtx, request); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("fence stopped before final channel query: %v", err)
	case <-fenceCtx.Done():
		close(resume)
		<-done
		t.Fatal("fence did not reach final channel query")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := notifyPayment(ctx, client, trade)
	cancel()
	close(resume)
	if err != nil {
		<-done
		t.Fatalf("channel query held order lock or rejected paid callback: %v", err)
	}
	<-done
	paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	if err := handler.RecoverOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	var decision model.OrderTransaction
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
		t.Fatalf("paid callback race released inventory: %+v %v", decision, err)
	}
}
