package payment

import (
	"context"
	"testing"
)

func TestClosedQueryPreservesPositivePaymentEvidenceWithoutInventingRefund(t *testing.T) {
	for _, field := range []string{"buyer_pay_amount", "receipt_amount", "point_amount", "invoice_amount", "fund_bill_list"} {
		t.Run(field, func(t *testing.T) {
			payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001", "trade_status": "TRADE_CLOSED", "total_amount": "12.34"}
			if field == "fund_bill_list" {
				payload[field] = []map[string]string{{"fund_channel": "ALIPAYACCOUNT", "amount": "12.34"}}
			} else {
				payload[field] = "0.01"
			}
			g, _ := fakeGateway(t, "alipay.trade.query", payload, false)
			trade, err := g.Query(context.Background(), "order_123")
			if err != nil || !trade.PaymentObserved || trade.Refunded || !trade.PaidAt.IsZero() {
				t.Fatalf("lost or invented query evidence: %+v, %v", trade, err)
			}
		})
	}
}

func TestQueryDoesNotInferPaymentFromAbsentOrZeroOptionalAmounts(t *testing.T) {
	for _, amount := range []string{"", "0", "0.00"} {
		payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001", "trade_status": "TRADE_CLOSED", "total_amount": "12.34",
			"buyer_pay_amount": amount, "receipt_amount": amount, "point_amount": amount, "invoice_amount": amount, "fund_bill_list": []any{}}
		g, _ := fakeGateway(t, "alipay.trade.query", payload, false)
		trade, err := g.Query(context.Background(), "order_123")
		if err != nil || trade.PaymentObserved || trade.Refunded {
			t.Fatalf("inferred payment/refund from absent evidence: %+v, %v", trade, err)
		}
	}
}

func TestQueryRejectsMalformedPaymentAmounts(t *testing.T) {
	for _, field := range []string{"buyer_pay_amount", "receipt_amount", "point_amount", "invoice_amount"} {
		for _, amount := range []string{"-1.00", "1e2", "NaN", "0.001", "100000000.01"} {
			payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001", "trade_status": "TRADE_CLOSED", "total_amount": "12.34", field: amount}
			g, _ := fakeGateway(t, "alipay.trade.query", payload, false)
			if _, err := g.Query(context.Background(), "order_123"); err == nil {
				t.Fatalf("accepted malformed %s", field)
			}
		}
	}
}
