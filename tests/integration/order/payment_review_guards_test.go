//go:build integration

package order_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
	"grpctest/tests/integration/testdb"
)

func TestPaymentWaitingEvidenceSurvivesLaterClosedWithoutEvidence(t *testing.T) {
	for _, evidence := range []string{"paid-at", "payment-observed"} {
		t.Run(evidence, func(t *testing.T) {
			db, client, _, _ := paymentSetup(t)
			trade := paymentOrder(t, db, "pay-waiting-evidence")
			trade.Status = "WAIT_BUYER_PAY"
			if evidence == "payment-observed" {
				trade.PaidAt = time.Time{}
				trade.PaymentObserved = true
			}
			if err := notifyPayment(context.Background(), client, trade); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
			var ledger model.OrderPayment
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&ledger).Error; err != nil || !ledger.Review {
				t.Fatalf("waiting payment evidence was not durable: %+v %v", ledger, err)
			}
			trade.Status, trade.PaidAt, trade.PaymentObserved = "TRADE_CLOSED", time.Time{}, false
			if err := notifyPayment(context.Background(), client, trade); err != nil {
				t.Fatal(err)
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 1, 0)
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&ledger).Error; err != nil || !ledger.Review {
				t.Fatalf("later sparse closed result erased review: %+v %v", ledger, err)
			}
			if err := handler.RecoverOrders(context.Background()); err != nil {
				t.Fatal(err)
			}
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
				t.Fatalf("contradictory payment evidence released stock: %+v %v", decision, err)
			}
		})
	}
}

func TestPaymentResolvedCloseReviewIgnoresOldUnpaidClosedResult(t *testing.T) {
	db, client, _, trade := missingPaymentSetup(t, "pay-resolved-old-closed")
	_ = handler.RecoverOrders(context.Background())
	checkMissingReview(t, db, trade.OrderSn)
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	trade.Status, trade.PaidAt, trade.PaymentObserved = "TRADE_CLOSED", time.Time{}, false
	if err := notifyPayment(context.Background(), client, trade); err != nil {
		t.Fatal(err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	var ledger model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&ledger).Error; err != nil || ledger.Review || ledger.Status != "TRADE_SUCCESS" {
		t.Fatalf("old unpaid closed result overrode paid state: %+v %v", ledger, err)
	}
}

func TestPaymentMissingReviewRejectsChangedMerchantBeforeRecoveryAndApply(t *testing.T) {
	for _, field := range []string{"app", "seller"} {
		t.Run(field, func(t *testing.T) {
			db, client, gateway, trade := missingPaymentSetup(t, "pay-review-merchant")
			_ = handler.RecoverOrders(context.Background())
			original := checkMissingReview(t, db, trade.OrderSn)
			if original.AppID != trade.AppID || original.SellerID != trade.SellerID {
				t.Fatal("missing trade review did not freeze original merchant")
			}
			if field == "app" {
				global.ServerConfig.Alipay.AppID = "changed-app"
			} else {
				global.ServerConfig.Alipay.SellerID = "changed-seller"
			}
			if err := db.Model(&model.OrderCloseReview{}).Where("order_sn = ?", trade.OrderSn).Update("next_check_at", time.Now().Add(-time.Minute)).Error; err != nil {
				t.Fatal(err)
			}
			changed := trade
			changed.AppID, changed.SellerID = global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID
			changed.Status, changed.PaidAt = "TRADE_CLOSED", time.Time{}
			queries, closes := 0, 0
			gateway.query = func(context.Context, string) (payment.Trade, error) { queries++; return changed, nil }
			gateway.close = func(context.Context, string) error { closes++; return nil }
			_ = handler.RecoverOrders(context.Background())
			if queries != 0 || closes != 0 {
				t.Fatalf("recovery queried a replacement merchant: queries=%d closes=%d", queries, closes)
			}
			// The boundary substitute represents a valid response from the NEW merchant.
			// Matching current config is insufficient to settle the OLD merchant's order.
			if err := notifyPayment(context.Background(), client, changed); err == nil {
				t.Fatal("replacement merchant result was applied to original order")
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 0, 0)
			var review model.OrderCloseReview
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&review).Error; err != nil || review.AppID != original.AppID || review.SellerID != original.SellerID || review.ResolvedAt != nil {
				t.Fatalf("frozen merchant binding changed: %+v %v", review, err)
			}
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || decision.StockReleased {
				t.Fatalf("replacement merchant released inventory: %+v %v", decision, err)
			}
		})
	}
}

func pendingFenceGuardSetup(t *testing.T, number string) (*gorm.DB, *paymentGateway, payment.Trade, handler.FenceCloseRequest, *[]payment.ClosureRequest) {
	t.Helper()
	db, _, gateway, trade := missingPaymentSetup(t, number)
	_ = handler.RecoverOrders(context.Background())
	checkMissingReview(t, db, number)
	request := fenceRequest(t, db, number)
	issued := []payment.ClosureRequest{}
	global.PaymentGateway = &missingClosureGateway{paymentGateway: gateway, create: func(_ context.Context, request payment.ClosureRequest) error {
		issued = append(issued, request)
		return context.DeadlineExceeded
	}}
	if _, err := handler.FenceAndCloseOrder(context.Background(), request); err == nil {
		t.Fatal("uncertain creation reported completed closure")
	}
	if len(issued) != 1 {
		t.Fatalf("expected one initial creation attempt, got %d", len(issued))
	}
	return db, gateway, trade, request, &issued
}

func TestPaymentManualFenceRejectsMerchantAndAmountDrift(t *testing.T) {
	for _, field := range []string{"app", "seller", "amount"} {
		t.Run(field, func(t *testing.T) {
			db, gateway, trade, request, issued := pendingFenceGuardSetup(t, "pay-fence-drift")
			var original model.OrderCloseReview
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&original).Error; err != nil {
				t.Fatal(err)
			}
			switch field {
			case "app":
				global.ServerConfig.Alipay.AppID = "changed-app"
			case "seller":
				global.ServerConfig.Alipay.SellerID = "changed-seller"
			case "amount":
				if err := db.Model(&model.OrderInfo{}).Where("order_sn = ?", trade.OrderSn).Update("total_cents", trade.AmountCents+1).Error; err != nil {
					t.Fatal(err)
				}
			}
			queries, closes := 0, 0
			gateway.query = func(context.Context, string) (payment.Trade, error) {
				queries++
				return payment.Trade{}, payment.ErrNotFound
			}
			gateway.close = func(context.Context, string) error { closes++; return payment.ErrNotFound }
			if _, err := handler.FenceAndCloseOrder(context.Background(), request); err == nil {
				t.Fatal("same request silently accepted changed merchant or amount")
			}
			if queries != 0 || closes != 0 || len(*issued) != 1 {
				t.Fatal("changed request reached the provider before being rejected")
			}
			var unchanged model.OrderCloseReview
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&unchanged).Error; err != nil || unchanged.RequestHash != original.RequestHash || unchanged.ActionExpiresAt == nil || original.ActionExpiresAt == nil || !unchanged.ActionExpiresAt.Equal(*original.ActionExpiresAt) {
				t.Fatalf("rejected request changed its persisted action: %+v %v", unchanged, err)
			}
			paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 0, 0)
		})
	}
}

func TestPaymentManualFenceResumeKeepsOriginalChannelDeadline(t *testing.T) {
	db, _, trade, request, issued := pendingFenceGuardSetup(t, "pay-fence-fixed-deadline")
	var original model.OrderCloseReview
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&original).Error; err != nil || original.ActionExpiresAt == nil {
		t.Fatalf("missing persisted action deadline: %+v %v", original, err)
	}
	if !(*issued)[0].ExpiresAt.Equal(*original.ActionExpiresAt) {
		t.Fatal("first channel request did not use the persisted deadline")
	}
	// Channel timestamps have second precision. Cross that boundary so recomputing
	// now+5m cannot accidentally appear to preserve the same deadline.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)) + 10*time.Millisecond)
	if _, err := handler.FenceAndCloseOrder(context.Background(), request); err == nil {
		t.Fatal("still-unknown transaction reported completed closure")
	}
	if len(*issued) != 2 {
		t.Fatalf("same request did not resume channel creation: %d calls", len(*issued))
	}
	for _, created := range *issued {
		if created.OrderSn != trade.OrderSn || created.AmountCents != trade.AmountCents || created.BuyerID != request.BuyerID || !created.ExpiresAt.Equal(*original.ActionExpiresAt) {
			t.Fatal("resumed provider request changed its frozen order, buyer, amount or deadline")
		}
	}
	var resumed model.OrderCloseReview
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&resumed).Error; err != nil || resumed.ActionExpiresAt == nil || !resumed.ActionExpiresAt.Equal(*original.ActionExpiresAt) || resumed.RequestHash != original.RequestHash {
		t.Fatalf("resume extended or rebound its action: %+v %v", resumed, err)
	}
	paymentState(t, db, trade.OrderSn, "PAYMENT_REVIEW", 0, 0)
}

func TestPaymentClosedReviewAndOutboxCommitTogetherUnderLostReply(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "lost-commit-ack"}[committed], func(t *testing.T) {
			db, client, _, trade := missingPaymentSetup(t, "pay-review-close-commit")
			_ = handler.RecoverOrders(context.Background())
			checkMissingReview(t, db, trade.OrderSn)
			trade.Status, trade.PaidAt = "TRADE_CLOSED", time.Time{}
			testdb.FailCommit(t, db, 1, committed)
			if err := notifyPayment(context.Background(), client, trade); err == nil {
				t.Fatal("unconfirmed close commit was acknowledged")
			}
			if committed {
				paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
			} else {
				checkMissingReview(t, db, trade.OrderSn)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := notifyPayment(context.Background(), client, trade); err != nil {
					t.Fatal(err)
				}
				paymentState(t, db, trade.OrderSn, "TRADE_CLOSED", 1, 1)
			}
			if err := handler.RecoverOrders(context.Background()); err != nil {
				t.Fatal(err)
			}
			var decision model.OrderTransaction
			if err := db.Where("order_sn = ?", trade.OrderSn).First(&decision).Error; err != nil || !decision.StockReleased {
				t.Fatalf("confirmed closure did not resume inventory release: %+v %v", decision, err)
			}
		})
	}
}
