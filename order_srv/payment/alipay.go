// Package payment contains the sandbox Alipay boundary. Only verified provider
// results leave this package; order transitions and inventory belong to the caller.
package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/smartwalle/alipay/v3"
)

const (
	MaxNotificationBytes = 64 << 10
	gatewayTimeout       = 8 * time.Second
	// Page-pay total_amount permits at most 100,000,000 yuan (integer cents here).
	MaxAmountCents int64 = 10_000_000_000
)

var (
	ErrNotFound            = errors.New("payment trade not found")
	ErrInvalidNotification = errors.New("invalid payment notification")
	errGateway             = errors.New("alipay gateway request failed; payment outcome is unknown")
	alipayLocation         = time.FixedZone("Asia/Shanghai", 8*60*60)
)

type Config struct {
	Enabled         bool   `mapstructure:"enabled" json:"enabled"`
	AppID           string `mapstructure:"appid" json:"appid"`
	SellerID        string `mapstructure:"seller_id" json:"seller_id"`
	PrivateKey      string `mapstructure:"private_key" json:"private_key"`
	AlipayPublicKey string `mapstructure:"alipaypublic_key" json:"alipaypublic_key"`
}

// Validate permits deliberately empty placeholders while payment is disabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	_, err := makeClient(c, &http.Client{Timeout: gatewayTimeout})
	return err
}

type Trade struct {
	OrderSn     string
	TradeNo     string
	AppID       string
	SellerID    string
	Status      string
	NotifyID    string
	AmountCents int64
	PaidAt      time.Time
	// Refunded preserves explicit refund evidence even when gmt_payment is absent.
	Refunded bool
	// PaymentObserved preserves positive payment evidence on a channel query.
	// It does not assert a refund. Missing optional fields do not prove non-payment.
	PaymentObserved bool
}

type Gateway interface {
	VerifyNotification(body string) (Trade, error)
	Query(ctx context.Context, orderSn string) (Trade, error)
	Close(ctx context.Context, orderSn string) error
}

// ClosureCreator is deliberately separate from Gateway: ordinary recovery must
// never create a channel trade merely because a lookup returned NotFound.
type ClosureCreator interface {
	CreateForClosure(context.Context, ClosureRequest) error
}

// ClosureRequest is for an explicit operator-authorized sandbox close attempt.
// BuyerID is the real sandbox buyer's Alipay account ID, not a shop user ID.
type ClosureRequest struct {
	OrderSn     string
	AmountCents int64
	BuyerID     string
	ExpiresAt   time.Time
}

type gateway struct {
	client   *alipay.Client
	appID    string
	sellerID string
}

func New(c Config) (Gateway, error) {
	if !c.Enabled {
		return nil, nil
	}
	client, err := makeClient(c, &http.Client{Timeout: gatewayTimeout})
	if err != nil {
		return nil, err
	}
	return &gateway{client: client, appID: c.AppID, sellerID: c.SellerID}, nil
}

func makeClient(c Config, httpClient *http.Client) (*alipay.Client, error) {
	if !identifier(c.AppID, 32) || !identifier(c.SellerID, 30) || strings.TrimSpace(c.PrivateKey) == "" || strings.TrimSpace(c.AlipayPublicKey) == "" {
		return nil, errors.New("enabled alipay requires appid, seller_id, private_key and alipaypublic_key")
	}
	// false selects the sandbox. No production gateway is enabled by this batch.
	client, err := alipay.New(c.AppID, c.PrivateKey, false, alipay.WithHTTPClient(httpClient), alipay.WithTimeLocation(alipayLocation))
	if err != nil {
		return nil, errors.New("invalid alipay application private key")
	}
	if err := client.LoadAliPayPublicKey(c.AlipayPublicKey); err != nil {
		return nil, errors.New("invalid alipay public key")
	}
	return client, nil
}

func (g *gateway) VerifyNotification(body string) (Trade, error) {
	if len(body) == 0 || len(body) > MaxNotificationBytes {
		return Trade{}, ErrInvalidNotification
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		return Trade{}, ErrInvalidNotification
	}
	for _, entries := range values {
		// A signature over ambiguous repeated parameters is never accepted.
		if len(entries) != 1 {
			return Trade{}, ErrInvalidNotification
		}
	}
	if values.Get("sign_type") != "RSA2" || values.Get("notify_type") != "trade_status_sync" || !identifier(values.Get("notify_id"), 128) {
		return Trade{}, ErrInvalidNotification
	}
	// DecodeNotification verifies all body parameters using the Alipay public key.
	// Query-string parameters are intentionally not merged with this signed body.
	n, err := g.client.DecodeNotification(values)
	if err != nil || n == nil || n.AppId != g.appID || n.SellerId != g.sellerID {
		return Trade{}, ErrInvalidNotification
	}
	t, err := verifiedTrade(n.OutTradeNo, n.TradeNo, string(n.TradeStatus), n.TotalAmount, n.GmtPayment)
	if err != nil {
		return Trade{}, ErrInvalidNotification
	}
	t.AppID, t.SellerID, t.NotifyID = n.AppId, n.SellerId, n.NotifyId
	for _, amount := range []string{n.RefundFee, n.RefundAmount} {
		if amount == "" {
			continue
		}
		cents, err := parseMoney(amount, true)
		if err != nil {
			return Trade{}, ErrInvalidNotification
		}
		t.Refunded = t.Refunded || cents > 0
	}
	if n.GmtRefund != "" {
		if _, err := time.ParseInLocation("2006-01-02 15:04:05", n.GmtRefund, alipayLocation); err != nil {
			return Trade{}, ErrInvalidNotification
		}
		t.Refunded = true
	}
	return t, nil
}

func (g *gateway) Query(ctx context.Context, orderSn string) (Trade, error) {
	if !identifier(orderSn, 64) {
		return Trade{}, errors.New("invalid payment order number")
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayTimeout)
	defer cancel()
	rsp, err := g.client.TradeQuery(ctx, alipay.TradeQuery{OutTradeNo: orderSn})
	if err != nil {
		return Trade{}, providerError(ctx, err)
	}
	if rsp == nil {
		return Trade{}, errGateway
	}
	if rsp.Code != alipay.CodeSuccess {
		return Trade{}, businessError(rsp.Error)
	}
	if rsp.OutTradeNo != orderSn {
		return Trade{}, errGateway
	}
	t, err := verifiedTrade(rsp.OutTradeNo, rsp.TradeNo, string(rsp.TradeStatus), rsp.TotalAmount, rsp.SendPayDate)
	if err != nil {
		return Trade{}, errGateway
	}
	// TradeQueryRsp has no app_id/seller_id. These identify the configured direct
	// merchant application making this signed, verified query (no delegation token),
	// not fields claimed to have been returned by Alipay. The sandbox configuration
	// must pair this AppID with its own receiving seller account.
	t.AppID, t.SellerID = g.appID, g.sellerID
	// A CLOSED trade can follow a payment/refund. Optional positive payment
	// amounts or a nonempty funding list must not be discarded merely because
	// send_pay_date is absent. These observations do not assert a refund.
	for _, amount := range []string{rsp.BuyerPayAmount, rsp.ReceiptAmount, rsp.PointAmount, rsp.InvoiceAmount} {
		if amount == "" {
			continue
		}
		cents, err := parseMoney(amount, true)
		if err != nil {
			return Trade{}, errGateway
		}
		t.PaymentObserved = t.PaymentObserved || cents > 0
	}
	t.PaymentObserved = t.PaymentObserved || len(rsp.FundBillList) > 0
	// This SDK exposes no refund amount/date on TradeQueryRsp. Preserve the
	// payment date above; do not invent missing refund evidence from the query.
	return t, nil
}

// Close reports a provider-confirmed close request only. The workflow must query
// again to establish CLOSED before releasing inventory. NotFound is not closure:
// an old signed page-pay URL may not have reached Alipay yet.
func (g *gateway) Close(ctx context.Context, orderSn string) error {
	if !identifier(orderSn, 64) {
		return errors.New("invalid payment order number")
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayTimeout)
	defer cancel()
	rsp, err := g.client.TradeClose(ctx, alipay.TradeClose{OutTradeNo: orderSn})
	if err != nil {
		return providerError(ctx, err)
	}
	if rsp == nil {
		return errGateway
	}
	// SDK IsSuccess also accepts 10003 (payment in progress), which is not closure.
	if rsp.Code != alipay.CodeSuccess {
		return businessError(rsp.Error)
	}
	if rsp.OutTradeNo != orderSn || !identifier(rsp.TradeNo, 64) {
		return errGateway
	}
	return nil
}

// CreateForClosure attempts to bind an absent sandbox trade to its existing
// merchant order number. It neither pays nor returns a cashier link. An ACK is
// not closure: callers must close and query a verified CLOSED trade before
// releasing inventory. Failures can still mean that creation reached Alipay.
func (g *gateway) CreateForClosure(ctx context.Context, req ClosureRequest) error {
	if g == nil || g.client == nil || g.client.IsProduction() {
		return errors.New("closure creation is restricted to the sandbox")
	}
	if !identifier(req.OrderSn, 64) || req.AmountCents <= 0 || req.AmountCents > MaxAmountCents ||
		!buyerIdentifier(req.BuyerID) || req.BuyerID == g.sellerID || !req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("invalid sandbox closure request")
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayTimeout)
	defer cancel()
	rsp, err := g.client.TradeCreate(ctx, alipay.TradeCreate{
		Trade: alipay.Trade{
			Subject:     "sandbox order closure " + req.OrderSn,
			OutTradeNo:  req.OrderSn,
			TotalAmount: fmt.Sprintf("%d.%02d", req.AmountCents/100, req.AmountCents%100),
			ProductCode: "FACE_TO_FACE_PAYMENT",
			TimeExpire:  req.ExpiresAt.In(alipayLocation).Format("2006-01-02 15:04:05"),
		},
		BuyerId: req.BuyerID,
	})
	if err != nil {
		return providerError(ctx, err)
	}
	if rsp == nil {
		return errGateway
	}
	if rsp.Code != alipay.CodeSuccess {
		return businessError(rsp.Error)
	}
	if rsp.OutTradeNo != req.OrderSn || !identifier(rsp.TradeNo, 64) {
		return errGateway
	}
	return nil
}

func buyerIdentifier(value string) bool {
	if len(value) != 16 || !strings.HasPrefix(value, "2088") {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func providerError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("alipay gateway request interrupted: %w", ctx.Err())
	}
	var apiErr *alipay.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		return businessError(*apiErr)
	}
	// SDK errors can contain network request details; do not propagate them.
	return errGateway
}

func businessError(e alipay.Error) error {
	if e.Code == alipay.CodeBusinessFailed && e.SubCode == "ACQ.TRADE_NOT_EXIST" {
		return ErrNotFound
	}
	return errGateway
}

func verifiedTrade(orderSn, tradeNo, status, amount, paidAt string) (Trade, error) {
	if !identifier(orderSn, 64) || !identifier(tradeNo, 64) {
		return Trade{}, ErrInvalidNotification
	}
	switch alipay.TradeStatus(status) {
	case alipay.TradeStatusWaitBuyerPay, alipay.TradeStatusClosed, alipay.TradeStatusSuccess, alipay.TradeStatusFinished:
	default:
		return Trade{}, ErrInvalidNotification
	}
	cents, err := parseAmount(amount)
	if err != nil {
		return Trade{}, err
	}
	t := Trade{OrderSn: orderSn, TradeNo: tradeNo, Status: status, AmountCents: cents}
	if paidAt != "" {
		t.PaidAt, err = time.ParseInLocation("2006-01-02 15:04:05", paidAt, alipayLocation)
		if err != nil {
			return Trade{}, ErrInvalidNotification
		}
	}
	if (status == string(alipay.TradeStatusSuccess) || status == string(alipay.TradeStatusFinished)) && t.PaidAt.IsZero() {
		return Trade{}, ErrInvalidNotification
	}
	return t, nil
}

func identifier(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// Alipay reports yuan as a decimal string. Parse directly into cents, allowing
// at most two fraction digits and never passing through binary floating point.
func parseAmount(s string) (int64, error) {
	return parseMoney(s, false)
}

func parseMoney(s string, allowZero bool) (int64, error) {
	if len(s) == 0 || len(s) > 12 {
		return 0, ErrInvalidNotification
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, ErrInvalidNotification
	}
	for _, part := range parts {
		if part == "" {
			return 0, ErrInvalidNotification
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, ErrInvalidNotification
			}
		}
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, ErrInvalidNotification
		}
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	cents, err := strconv.ParseInt(parts[0]+fraction, 10, 64)
	if err != nil || cents < 0 || (!allowZero && cents == 0) || cents > MaxAmountCents {
		return 0, ErrInvalidNotification
	}
	return cents, nil
}
