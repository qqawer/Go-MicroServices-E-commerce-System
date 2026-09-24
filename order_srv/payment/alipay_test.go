package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixtureKeys struct {
	once                  sync.Once
	application, provider *rsa.PrivateKey
	err                   error
}

func fixture(t *testing.T) (Config, *rsa.PrivateKey) {
	t.Helper()
	fixtureKeys.once.Do(func() {
		fixtureKeys.application, fixtureKeys.err = rsa.GenerateKey(rand.Reader, 2048)
		if fixtureKeys.err == nil {
			fixtureKeys.provider, fixtureKeys.err = rsa.GenerateKey(rand.Reader, 2048)
		}
	})
	if fixtureKeys.err != nil {
		t.Fatal(fixtureKeys.err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&fixtureKeys.provider.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Enabled: true, AppID: "2021000000000001", SellerID: "2088000000000001",
		PrivateKey:      string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(fixtureKeys.application)})),
		AlipayPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))}, fixtureKeys.provider
}

func sign(t *testing.T, key *rsa.PrivateKey, data []byte) string {
	t.Helper()
	digest := sha256.Sum256(data)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func canonical(values url.Values) []byte {
	var pairs []string
	for key, vs := range values {
		if key == "sign" || key == "sign_type" || key == "alipay_cert_sn" {
			continue
		}
		for _, value := range vs {
			pairs = append(pairs, key+"="+value)
		}
	}
	sort.Strings(pairs)
	return []byte(strings.Join(pairs, "&"))
}

func notificationValues(c Config) url.Values {
	return url.Values{"app_id": {c.AppID}, "seller_id": {c.SellerID}, "out_trade_no": {"order_123"},
		"trade_no": {"2026092300000001"}, "notify_id": {"notify_123"}, "notify_type": {"trade_status_sync"},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"12.34"}, "gmt_payment": {"2026-09-23 12:34:56"},
		"sign_type": {"RSA2"}, "subject": {"测试商品 & 苹果+香蕉"}}
}

func signedNotification(t *testing.T, key *rsa.PrivateKey, v url.Values) string {
	t.Helper()
	v.Set("sign", sign(t, key, canonical(v)))
	return v.Encode()
}

func TestDisabledPlaceholdersAndInvalidKeys(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if g, err := New(Config{}); g != nil || err != nil {
		t.Fatalf("disabled = %v, %v", g, err)
	}
	if err := (Config{Enabled: true}).Validate(); err == nil {
		t.Fatal("enabled empty configuration accepted")
	}
	c, _ := fixture(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"seller", "private", "public"} {
		bad := c
		switch field {
		case "seller":
			bad.SellerID = ""
		case "private":
			bad.PrivateKey = "secret-placeholder"
		case "public":
			bad.AlipayPublicKey = "secret-placeholder"
		}
		_, err := New(bad)
		if err == nil || strings.Contains(err.Error(), "secret-placeholder") {
			t.Fatalf("bad %s: %v", field, err)
		}
	}
}

func TestNotificationVerifiesRealRSA2AndMerchantIdentity(t *testing.T) {
	c, key := fixture(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"WAIT_BUYER_PAY", "TRADE_SUCCESS", "TRADE_FINISHED", "TRADE_CLOSED"} {
		v := notificationValues(c)
		v.Set("trade_status", status)
		trade, err := g.VerifyNotification(signedNotification(t, key, v))
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if trade.AmountCents != 1234 || trade.AppID != c.AppID || trade.SellerID != c.SellerID || trade.OrderSn != "order_123" || trade.NotifyID != "notify_123" || trade.Status != status {
			t.Fatalf("unexpected verified trade: %+v", trade)
		}
		if trade.PaidAt.UTC().Format(time.RFC3339) != "2026-09-23T04:34:56Z" {
			t.Fatalf("wrong payment timezone: %v", trade.PaidAt)
		}
	}
	tests := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"other app", func(v url.Values) { v.Set("app_id", "other_app") }},
		{"other seller", func(v url.Values) { v.Set("seller_id", "other_seller") }},
		{"missing trade", func(v url.Values) { v.Del("trade_no") }},
		{"long order", func(v url.Values) { v.Set("out_trade_no", strings.Repeat("a", 65)) }},
		{"unknown status", func(v url.Values) { v.Set("trade_status", "SUCCESS") }},
		{"missing paid time", func(v url.Values) { v.Del("gmt_payment") }},
		{"bad paid time", func(v url.Values) { v.Set("gmt_payment", "not-a-date") }},
		{"RSA1 rejected", func(v url.Values) { v.Set("sign_type", "RSA") }},
		{"unknown event", func(v url.Values) { v.Set("notify_type", "other") }},
		{"missing notify id", func(v url.Values) { v.Del("notify_id") }},
		{"duplicate amount", func(v url.Values) { v.Add("total_amount", "12.34") }},
		{"duplicate unrelated signed field", func(v url.Values) { v.Add("subject", "same") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := notificationValues(c)
			tc.mutate(v)
			if _, err := g.VerifyNotification(signedNotification(t, key, v)); !errors.Is(err, ErrInvalidNotification) {
				t.Fatalf("accepted malformed signed notification: %v", err)
			}
		})
	}
	v := notificationValues(c)
	body := signedNotification(t, key, v)
	body = strings.Replace(body, "total_amount=12.34", "total_amount=99.99", 1)
	if _, err := g.VerifyNotification(body); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("accepted tampered body: %v", err)
	}
	for _, body := range []string{"", "value=%ZZ", strings.Repeat("x", MaxNotificationBytes+1)} {
		if _, err := g.VerifyNotification(body); !errors.Is(err, ErrInvalidNotification) {
			t.Fatalf("accepted invalid form: %v", err)
		}
	}
}

func TestSignedNotificationAmountsUseExactCents(t *testing.T) {
	c, key := fixture(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for amount, want := range map[string]int64{"0.01": 1, "1": 100, "1.2": 120, "12345678.91": 1234567891, "100000000.00": MaxAmountCents} {
		v := notificationValues(c)
		v.Set("total_amount", amount)
		got, err := g.VerifyNotification(signedNotification(t, key, v))
		if err != nil || got.AmountCents != want {
			t.Fatalf("%s: %v, %v", amount, got, err)
		}
	}
	for _, amount := range []string{"0", "-1.00", "+1.00", "1e2", "NaN", "1.001", "1.", ".1", " 1.00", "100000000.01", "9223372036854775808"} {
		v := notificationValues(c)
		v.Set("total_amount", amount)
		if _, err := g.VerifyNotification(signedNotification(t, key, v)); !errors.Is(err, ErrInvalidNotification) {
			t.Fatalf("accepted %q: %v", amount, err)
		}
	}
}

func TestRefundNotificationWithoutPaymentDateRetainsReviewEvidence(t *testing.T) {
	c, key := fixture(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field, value string
		refunded     bool
	}{
		{"refund_fee", "12.34", true},
		{"refund_amount", "0.01", true},
		{"gmt_refund", "2026-09-23 14:00:00", true},
		{"refund_fee", "0.00", false},
		{"refund_amount", "0", false},
	} {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			values := notificationValues(c)
			values.Set("trade_status", "TRADE_CLOSED")
			values.Del("gmt_payment")
			values.Set(tc.field, tc.value)
			trade, err := g.VerifyNotification(signedNotification(t, key, values))
			if err != nil || trade.Refunded != tc.refunded || !trade.PaidAt.IsZero() {
				t.Fatalf("refund evidence lost or payment date invented: %+v, %v", trade, err)
			}
		})
	}
	for _, tc := range []struct{ field, value string }{
		{"refund_fee", "-1.00"}, {"refund_amount", "1e2"}, {"gmt_refund", "invalid-date"},
	} {
		values := notificationValues(c)
		values.Set(tc.field, tc.value)
		if _, err := g.VerifyNotification(signedNotification(t, key, values)); !errors.Is(err, ErrInvalidNotification) {
			t.Fatalf("accepted malformed refund evidence %s: %v", tc.field, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerResponse(t *testing.T, key *rsa.PrivateKey, method string, payload map[string]any, badSignature bool) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(t, key, raw)
	if badSignature {
		sig = sign(t, key, []byte("different response"))
	}
	encoded, err := json.Marshal(map[string]any{strings.ReplaceAll(method, ".", "_") + "_response": json.RawMessage(raw), "sign": sig})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func fakeGateway(t *testing.T, method string, payload map[string]any, badSignature bool) (*gateway, *int) {
	t.Helper()
	c, key := fixture(t)
	calls := 0
	client, err := makeClient(c, &http.Client{Timeout: gatewayTimeout, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Host != "openapi-sandbox.dl.alipaydev.com" {
			t.Fatalf("wrong target: %s %v", r.Method, r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("method") != method || r.Form.Get("app_id") != c.AppID {
			t.Fatalf("wrong request: %s", r.Form.Get("method"))
		}
		var biz map[string]string
		if err := json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); err != nil {
			t.Fatal(err)
		}
		if biz["out_trade_no"] != "order_123" {
			t.Fatalf("wrong merchant order: %q", biz["out_trade_no"])
		}
		if d, ok := r.Context().Deadline(); !ok || time.Until(d) > gatewayTimeout {
			t.Fatal("request has no bounded deadline")
		}
		ts, err := time.ParseInLocation("2006-01-02 15:04:05", r.Form.Get("timestamp"), alipayLocation)
		if err != nil || time.Since(ts) > 2*time.Second {
			t.Fatalf("request uses wrong timestamp timezone: %v, %v", ts, err)
		}
		// Request signatures include sign_type; only sign itself is omitted.
		var pairs []string
		for k, vs := range r.Form {
			if k != "sign" {
				for _, v := range vs {
					pairs = append(pairs, k+"="+v)
				}
			}
		}
		sort.Strings(pairs)
		digest := sha256.Sum256([]byte(strings.Join(pairs, "&")))
		sig, err := base64.StdEncoding.DecodeString(r.Form.Get("sign"))
		if err != nil {
			t.Fatal(err)
		}
		if err := rsa.VerifyPKCS1v15(&fixtureKeys.application.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
			t.Fatalf("request signature: %v", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(providerResponse(t, key, method, payload, badSignature))), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return &gateway{client: client, appID: c.AppID, sellerID: c.SellerID}, &calls
}

func TestQueryVerifiesSignedResponseAndBusinessCode(t *testing.T) {
	for _, status := range []string{"WAIT_BUYER_PAY", "TRADE_SUCCESS", "TRADE_FINISHED", "TRADE_CLOSED"} {
		t.Run(status, func(t *testing.T) {
			payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001", "trade_status": status, "total_amount": "12.34", "send_pay_date": "2026-09-23 12:34:56"}
			g, calls := fakeGateway(t, "alipay.trade.query", payload, false)
			got, err := g.Query(context.Background(), "order_123")
			if err != nil || got.Status != status || got.AmountCents != 1234 || got.AppID != g.appID || got.SellerID != g.sellerID || got.PaidAt.IsZero() {
				t.Fatalf("query: %+v, %v", got, err)
			}
			if *calls != 1 {
				t.Fatalf("unexpected retries: %d", *calls)
			}
		})
	}
	tests := []struct {
		name             string
		patch            map[string]any
		badSig, notFound bool
	}{
		{"not found", map[string]any{"code": "40004", "sub_code": "ACQ.TRADE_NOT_EXIST"}, false, true},
		{"system error", map[string]any{"code": "20000", "sub_code": "ACQ.SYSTEM_ERROR"}, false, false},
		{"still processing", map[string]any{"code": "10003"}, false, false},
		{"tampered response", nil, true, false},
		{"other order", map[string]any{"out_trade_no": "other_order"}, false, false},
		{"missing trade number", map[string]any{"trade_no": ""}, false, false},
		{"invalid amount", map[string]any{"total_amount": "1e2"}, false, false},
		{"unknown status", map[string]any{"trade_status": "unknown"}, false, false},
		{"missing payment date", map[string]any{"send_pay_date": ""}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001", "trade_status": "TRADE_SUCCESS", "total_amount": "12.34", "send_pay_date": "2026-09-23 12:34:56"}
			for k, v := range tc.patch {
				payload[k] = v
			}
			g, _ := fakeGateway(t, "alipay.trade.query", payload, tc.badSig)
			_, err := g.Query(context.Background(), "order_123")
			if err == nil || errors.Is(err, ErrNotFound) != tc.notFound {
				t.Fatalf("query returned %v", err)
			}
		})
	}
}

func TestCloseRequiresVerifiedDefinitiveSuccess(t *testing.T) {
	for _, code := range []string{"10000", "10003", "40004", "20000"} {
		t.Run(code, func(t *testing.T) {
			payload := map[string]any{"code": code, "out_trade_no": "order_123", "trade_no": "2026092300000001", "sub_code": "ACQ.TRADE_NOT_EXIST"}
			g, calls := fakeGateway(t, "alipay.trade.close", payload, false)
			err := g.Close(context.Background(), "order_123")
			if (err == nil) != (code == "10000") || errors.Is(err, ErrNotFound) != (code == "40004") {
				t.Fatalf("%s returned %v", code, err)
			}
			if *calls != 1 {
				t.Fatalf("unexpected retries: %d", *calls)
			}
		})
	}
	for _, patch := range []map[string]any{{"out_trade_no": "other_order"}, {"trade_no": ""}} {
		payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001"}
		for k, v := range patch {
			payload[k] = v
		}
		g, _ := fakeGateway(t, "alipay.trade.close", payload, false)
		if err := g.Close(context.Background(), "order_123"); err == nil {
			t.Fatal("accepted mismatched close response")
		}
	}
	g, _ := fakeGateway(t, "alipay.trade.close", map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001"}, true)
	if err := g.Close(context.Background(), "order_123"); err == nil {
		t.Fatal("accepted unverified close response")
	}
}

func TestGatewayHonorsContextAndDoesNotLeakProviderErrors(t *testing.T) {
	c, _ := fixture(t)
	gw, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g := gw.(*gateway)
	if g.client.Client.Timeout != gatewayTimeout {
		t.Fatal("unbounded HTTP timeout")
	}
	g.client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Query(ctx, "order_123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("query cancellation: %v", err)
	}
	if err := g.Close(ctx, "order_123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("close cancellation: %v", err)
	}
	g.client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("private-key-do-not-leak") })
	if _, err := g.Query(context.Background(), "order_123"); err == nil || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSDKErrorEnvelopeAndEmptyResponsesAreNotClosure(t *testing.T) {
	c, _ := fixture(t)
	tests := []struct {
		name, body string
		notFound   bool
	}{
		{"unsigned error envelope", `{"error_response":{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}}`, true},
		{"unsigned query business error", `{"alipay_trade_query_response":{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}}`, true},
		{"unsigned close business error", `{"alipay_trade_close_response":{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}}`, true},
		{"unknown gateway error", `{"error_response":{"code":"20000","sub_msg":"private-detail"}}`, false},
		{"invalid JSON", `gateway interrupted`, false},
		{"missing response", `{}`, false},
		{"null response", `{"alipay_trade_query_response":null}`, false},
		{"unsigned success", `{"alipay_trade_query_response":{"code":"10000"}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := makeClient(c, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			g := &gateway{client: client, appID: c.AppID, sellerID: c.SellerID}
			if strings.Contains(tc.name, "close") {
				err = g.Close(context.Background(), "order_123")
			} else {
				_, err = g.Query(context.Background(), "order_123")
			}
			if err == nil || errors.Is(err, ErrNotFound) != tc.notFound || strings.Contains(err.Error(), "private-detail") {
				t.Fatalf("unsafe SDK error classification: %v", err)
			}
		})
	}
}

func TestConcurrentNotificationVerificationReusesClient(t *testing.T) {
	c, key := fixture(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := signedNotification(t, key, notificationValues(c))
	errs := make(chan error, 32)
	for i := 0; i < cap(errs); i++ {
		go func() { _, err := g.VerifyNotification(body); errs <- err }()
	}
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
