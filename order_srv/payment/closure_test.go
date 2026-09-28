package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/smartwalle/alipay/v3"
)

func closureRequest() ClosureRequest {
	return ClosureRequest{OrderSn: "order_123", AmountCents: 1234, BuyerID: "2088000000000002", ExpiresAt: time.Now().Add(4 * time.Minute).Truncate(time.Second)}
}

func TestCreateForClosureSignsOneSandboxCreateWithoutPaying(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		money string
	}{{1, "0.01"}, {1234, "12.34"}, {MaxAmountCents, "100000000.00"}} {
		t.Run(tc.money, func(t *testing.T) {
			req := closureRequest()
			req.AmountCents = tc.cents
			g, calls := fakeGateway(t, "alipay.trade.create", map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001"}, false)
			transport := g.client.Client.Transport
			g.client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				var biz map[string]string
				if err := json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); err != nil {
					t.Fatal(err)
				}
				if biz["buyer_id"] != req.BuyerID || biz["total_amount"] != tc.money ||
					biz["time_expire"] != req.ExpiresAt.In(alipayLocation).Format("2006-01-02 15:04:05") || biz["timeout_express"] != "" ||
					biz["product_code"] != "FACE_TO_FACE_PAYMENT" || biz["subject"] != "sandbox order closure "+req.OrderSn ||
					r.Form.Get("notify_url") != "" || r.Form.Get("return_url") != "" || r.Form.Get("app_auth_token") != "" {
					t.Fatal("unexpected closure creation parameters")
				}
				// The shared fixture also verifies the real SDK request RSA2
				// signature, sandbox host, identity and bounded request deadline.
				return transport.RoundTrip(r)
			})
			if err := g.CreateForClosure(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if *calls != 1 {
				t.Fatalf("expected one create request, got %d", *calls)
			}
		})
	}
}

func TestCreateForClosureRequiresVerifiedExactSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		patch  map[string]any
		badSig bool
	}{
		{"processing", map[string]any{"code": "10003"}, false},
		{"not found", map[string]any{"code": "40004", "sub_code": "ACQ.TRADE_NOT_EXIST"}, false},
		{"business error", map[string]any{"code": "40004", "sub_msg": "private-buyer-do-not-print"}, false},
		{"other order", map[string]any{"out_trade_no": "another_order"}, false},
		{"empty trade", map[string]any{"trade_no": ""}, false},
		{"invalid trade", map[string]any{"trade_no": "invalid/trade"}, false},
		{"oversized trade", map[string]any{"trade_no": strings.Repeat("1", 65)}, false},
		{"invalid signature", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001"}
			for key, value := range tc.patch {
				payload[key] = value
			}
			g, calls := fakeGateway(t, "alipay.trade.create", payload, tc.badSig)
			err := g.CreateForClosure(context.Background(), closureRequest())
			if err == nil || strings.Contains(err.Error(), "private-buyer") || *calls != 1 {
				t.Fatalf("unsafe create acknowledgment: %v", err)
			}
			if errors.Is(err, ErrNotFound) != (tc.name == "not found") {
				t.Fatalf("wrong business error classification: %v", err)
			}
		})
	}
}

func TestCreateForClosureRejectsInvalidInputBeforeNetwork(t *testing.T) {
	for name, mutate := range map[string]func(*ClosureRequest){
		"empty order":        func(r *ClosureRequest) { r.OrderSn = "" },
		"long order":         func(r *ClosureRequest) { r.OrderSn = strings.Repeat("x", 65) },
		"invalid order":      func(r *ClosureRequest) { r.OrderSn = "a/b" },
		"zero amount":        func(r *ClosureRequest) { r.AmountCents = 0 },
		"negative amount":    func(r *ClosureRequest) { r.AmountCents = -1 },
		"large amount":       func(r *ClosureRequest) { r.AmountCents = MaxAmountCents + 1 },
		"empty buyer":        func(r *ClosureRequest) { r.BuyerID = "" },
		"short buyer":        func(r *ClosureRequest) { r.BuyerID = strings.Repeat("1", 15) },
		"long buyer":         func(r *ClosureRequest) { r.BuyerID = strings.Repeat("1", 31) },
		"nondigit buyer":     func(r *ClosureRequest) { r.BuyerID = "20880000000000xx" },
		"unicode buyer":      func(r *ClosureRequest) { r.BuyerID = "２０８８０００００００００００２" },
		"seller is buyer":    func(r *ClosureRequest) { r.BuyerID = "2088000000000001" },
		"wrong buyer prefix": func(r *ClosureRequest) { r.BuyerID = "3088000000000002" },
		"no expiry":          func(r *ClosureRequest) { r.ExpiresAt = time.Time{} },
		"past expiry":        func(r *ClosureRequest) { r.ExpiresAt = time.Now().Add(-time.Second) },
		"far expiry":         func(r *ClosureRequest) { r.ExpiresAt = time.Now().Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			g, calls := fakeGateway(t, "alipay.trade.create", nil, false)
			req := closureRequest()
			mutate(&req)
			if err := g.CreateForClosure(context.Background(), req); err == nil || *calls != 0 {
				t.Fatal("invalid request reached network")
			}
		})
	}
	c, _ := fixture(t)
	client, err := alipay.New(c.AppID, c.PrivateKey, true)
	if err != nil {
		t.Fatal(err)
	}
	client.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("production request reached network")
		return nil, errors.New("unreachable")
	})}
	g := &gateway{client: client, appID: c.AppID, sellerID: c.SellerID}
	if err := g.CreateForClosure(context.Background(), closureRequest()); err == nil {
		t.Fatal("production closure creation accepted")
	}
}

func TestCreateForClosureCancellationAndErrorsAreSafe(t *testing.T) {
	c, _ := fixture(t)
	gw, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g := gw.(*gateway)
	if _, ok := gw.(ClosureCreator); !ok {
		t.Fatal("sandbox gateway lacks explicit closure creator")
	}
	g.client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.CreateForClosure(ctx, closureRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := g.CreateForClosure(ctx, closureRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not preserved: %v", err)
	}
	g.client.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private-key-and-buyer-must-not-leak")
	})
	if err := g.CreateForClosure(context.Background(), closureRequest()); err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestCreateForClosureRetriesKeepOriginalAbsoluteExpiry(t *testing.T) {
	g, calls := fakeGateway(t, "alipay.trade.create", map[string]any{"code": "10000", "out_trade_no": "order_123", "trade_no": "2026092300000001"}, false)
	transport := g.client.Client.Transport
	var previous string
	g.client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		biz := r.Form.Get("biz_content")
		if previous != "" && previous != biz {
			t.Fatal("retry changed the creation identity or extended expiry")
		}
		previous = biz
		return transport.RoundTrip(r)
	})
	req := closureRequest()
	for i := 0; i < 2; i++ {
		if err := g.CreateForClosure(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 2 {
		t.Fatal("unexpected SDK retries")
	}
}
