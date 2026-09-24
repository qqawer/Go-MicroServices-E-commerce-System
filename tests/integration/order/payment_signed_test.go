//go:build integration

package order_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/order_srv/config"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"
)

// This test bridges the real signed form, real SDK verifier, in-memory gRPC
// transport and real MySQL transaction. No payment gateway or verifier is mocked.
// Keys are generated for this test only, and no external Alipay request is made.
func TestSignedPaymentNotificationRPCCommitsOnce(t *testing.T) {
	db := testdb.Open(t, "grpctest_orders_srv")
	appKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	providerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&providerKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := payment.Config{
		Enabled: true, AppID: "2021000000000009", SellerID: "2088000000000009",
		PrivateKey:      string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)})),
		AlipayPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKey})),
	}
	gate, err := payment.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldDB, oldConfig, oldGateway := global.DB, global.ServerConfig, global.PaymentGateway
	global.DB, global.ServerConfig, global.PaymentGateway = db, &config.ServerConfig{Alipay: cfg}, gate
	t.Cleanup(func() {
		for _, table := range []string{"order_payment", "order_event", "order_transaction", "orderinfo"} {
			if err := db.Exec("DELETE FROM " + table + " WHERE order_sn LIKE 'pay-signed-%'").Error; err != nil {
				t.Error(err)
			}
		}
		global.DB, global.ServerConfig, global.PaymentGateway = oldDB, oldConfig, oldGateway
	})
	_, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterOrderServer(s, &handler.OrderServer{}) })
	client := proto.NewOrderClient(conn)
	trade := paymentOrder(t, db, "pay-signed-rpc")
	paidAt := time.Now().Add(-time.Second).Truncate(time.Second)
	values := url.Values{
		"app_id": {cfg.AppID}, "seller_id": {cfg.SellerID},
		"out_trade_no": {trade.OrderSn}, "trade_no": {"20260923000000090001"},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"123.45"},
		"notify_id": {"signed_rpc_notification"}, "notify_type": {"trade_status_sync"},
		"sign_type": {"RSA2"}, "subject": {"测试商品 & 苹果+香蕉"},
		"gmt_payment": {paidAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05")},
	}
	var pairs []string
	for key, entries := range values {
		if key == "sign_type" {
			continue
		}
		for _, value := range entries {
			pairs = append(pairs, key+"="+value)
		}
	}
	sort.Strings(pairs)
	digest := sha256.Sum256([]byte(strings.Join(pairs, "&")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, providerKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", base64.StdEncoding.EncodeToString(signature))
	body := values.Encode()
	send := func(body string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.ProcessPaymentNotification(ctx, &proto.PaymentNotification{Body: body})
		return err
	}

	// Tampering must be rejected by the real SDK before any ledger is committed.
	tampered := strings.Replace(body, "total_amount=123.45", "total_amount=123.46", 1)
	if tampered == body {
		t.Fatal("fixture did not change the signed amount")
	}
	if err := send(tampered); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("tampered notification returned %v", err)
	}
	paymentState(t, db, trade.OrderSn, "WAIT_BUYER_PAY", 0, 0)

	if err := send(body); err != nil {
		t.Fatalf("valid signed notification: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := send(body); err != nil {
			t.Fatalf("duplicate notification %d: %v", i, err)
		}
	}
	order := paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
	if order.TradeNo != values.Get("trade_no") {
		t.Fatalf("order is bound to wrong channel trade: %q", order.TradeNo)
	}
	var ledger model.OrderPayment
	if err := db.Where("order_sn = ?", trade.OrderSn).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.AppID != cfg.AppID || ledger.SellerID != cfg.SellerID || ledger.AmountCents != 12345 ||
		ledger.NotifyID != values.Get("notify_id") || ledger.Status != "TRADE_SUCCESS" || ledger.Review ||
		ledger.PaidAt == nil || !ledger.PaidAt.Equal(paidAt) {
		t.Fatalf("verified evidence was not committed faithfully: %+v", ledger)
	}
	// A later forged status must also fail; success is not a bypass for verification.
	if err := send(strings.Replace(body, "trade_status=TRADE_SUCCESS", "trade_status=TRADE_CLOSED", 1)); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("forged later status returned %v", err)
	}
	paymentState(t, db, trade.OrderSn, "TRADE_SUCCESS", 1, 0)
}
