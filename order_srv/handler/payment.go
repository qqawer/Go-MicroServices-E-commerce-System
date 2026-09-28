package handler

import (
	"context"
	"errors"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
	"grpctest/order_srv/proto"
)

// ProcessPaymentNotification is the only notification path that may mark an
// order paid. Never accept a caller-supplied payment status without verification.
func (*OrderServer) ProcessPaymentNotification(ctx context.Context, req *proto.PaymentNotification) (*empty.Empty, error) {
	if global.ServerConfig == nil || !global.ServerConfig.Alipay.Enabled || global.PaymentGateway == nil {
		return nil, status.Error(codes.FailedPrecondition, "支付宝支付尚未启用")
	}
	if len(req.GetBody()) == 0 || len(req.GetBody()) > 64*1024 {
		return nil, status.Error(codes.InvalidArgument, "支付通知内容无效")
	}
	trade, err := global.PaymentGateway.VerifyNotification(req.Body)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "支付通知验证失败")
	}
	if _, err := applyPaymentTrade(ctx, trade); err != nil {
		return nil, paymentError(ctx, err)
	}
	return &empty.Empty{}, nil
}

func paidStatus(value string) bool { return value == "TRADE_SUCCESS" || value == "TRADE_FINISHED" }

func validatePaymentTrade(trade payment.Trade) error {
	if global.ServerConfig == nil || !global.ServerConfig.Alipay.Enabled || !validOrderSN.MatchString(trade.OrderSn) || trade.TradeNo == "" || len(trade.TradeNo) > 100 || len(trade.NotifyID) > 128 || trade.AmountCents <= 0 {
		return status.Error(codes.FailedPrecondition, "支付交易信息不完整")
	}
	cfg := global.ServerConfig.Alipay
	if cfg.AppID == "" || cfg.SellerID == "" || trade.AppID != cfg.AppID || trade.SellerID != cfg.SellerID {
		return status.Error(codes.FailedPrecondition, "支付应用或收款方不匹配")
	}
	switch trade.Status {
	case "WAIT_BUYER_PAY", "TRADE_CLOSED":
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		if trade.PaidAt.IsZero() {
			return status.Error(codes.FailedPrecondition, "支付成功通知缺少付款时间")
		}
	default:
		return status.Error(codes.InvalidArgument, "支付交易状态无效")
	}
	return nil
}

// The same order lock serializes notifications, timeout closure and query
// reconciliation. Channel network calls must happen before entering this lock.
func applyPaymentTrade(ctx context.Context, trade payment.Trade) (bool, error) {
	if err := validatePaymentTrade(trade); err != nil {
		return false, err
	}
	refund := false
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", trade.OrderSn).First(&order).Error; err != nil {
			return err
		}
		// The legacy order table may use a case-insensitive MySQL collation.
		// A successful SQL lookup alone does not establish exact channel identity.
		if order.OrderSn != trade.OrderSn {
			return status.Error(codes.FailedPrecondition, "支付订单号不匹配")
		}
		if err := validateCloseReviewMerchant(tx, trade.OrderSn, trade.AppID, trade.SellerID); err != nil {
			return err
		}
		if order.PayType != "alipay" || order.TotalCents == nil || *order.TotalCents != trade.AmountCents {
			return status.Error(codes.FailedPrecondition, "订单支付方式或金额不匹配")
		}
		if order.TradeNo != "" && order.TradeNo != trade.TradeNo {
			return status.Error(codes.FailedPrecondition, "订单交易号不匹配")
		}
		record := model.OrderPayment{OrderSn: trade.OrderSn, TradeNo: trade.TradeNo, AppID: trade.AppID, SellerID: trade.SellerID, AmountCents: trade.AmountCents, Status: trade.Status}
		// Unique indexes fence reuse across different orders, whose row locks are
		// independent. A conflicting trade can never be silently reassigned.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
			return err
		}
		record = model.OrderPayment{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", trade.TradeNo).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Error(codes.FailedPrecondition, "订单已经关联其他支付交易")
			}
			return err
		}
		if record.OrderSn != trade.OrderSn || record.AppID != trade.AppID || record.SellerID != trade.SellerID || record.AmountCents != trade.AmountCents {
			return status.Error(codes.FailedPrecondition, "支付流水与订单不匹配")
		}
		changes := map[string]interface{}{}
		if trade.NotifyID != "" {
			changes["notify_id"] = trade.NotifyID
		}
		if !trade.PaidAt.IsZero() && record.PaidAt == nil {
			changes["paid_at"] = trade.PaidAt
		}
		// WAIT/CLOSED delivered after payment must not roll a paid transaction back.
		if !paidStatus(record.Status) || trade.Status == "TRADE_FINISHED" {
			if record.Status != "TRADE_CLOSED" || paidStatus(trade.Status) {
				changes["status"] = trade.Status
			}
		}
		needsReview := (order.Status == "TRADE_CLOSED" && paidStatus(trade.Status)) || trade.Refunded ||
			(trade.Status == "TRADE_CLOSED" && (!trade.PaidAt.IsZero() || trade.PaymentObserved || (order.PayTime != nil && !paidStatus(order.Status)))) ||
			(trade.Status == "WAIT_BUYER_PAY" && !paidStatus(record.Status) && (!trade.PaidAt.IsZero() || trade.PaymentObserved)) ||
			(!paidStatus(record.Status) && record.PaidAt != nil)
		if record.Review || needsReview {
			// Review is a durable exception state. Keep closed orders closed; all
			// other affected orders explicitly stop being fulfillable.
			if needsReview {
				changes["review"], changes["review_reason"] = true, "payment_after_close_or_refund"
			}
			if order.Status != "TRADE_CLOSED" && order.Status != "PAYMENT_REVIEW" {
				if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "PAYMENT_REVIEW").Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&model.OrderPayment{}).Where("id = ?", record.ID).Updates(changes).Error; err != nil {
				return err
			}
			return resolveCloseReview(tx, trade.OrderSn, "payment_exception")
		}
		if err := tx.Model(&model.OrderPayment{}).Where("id = ?", record.ID).Updates(changes).Error; err != nil {
			return err
		}
		if paidStatus(trade.Status) {
			switch order.Status {
			case "", "PAYING", "WAIT_BUYER_PAY", "CLOSING", "TRADE_SUCCESS", "TRADE_FINISHED":
			case "PAYMENT_REVIEW":
				pending, err := unresolvedCloseReview(tx, trade.OrderSn)
				if err != nil {
					return err
				}
				if !pending {
					return status.Error(codes.FailedPrecondition, "订单状态需要人工核对")
				}
			default:
				return status.Error(codes.FailedPrecondition, "订单状态需要人工核对")
			}
			next := trade.Status
			if order.Status == "TRADE_FINISHED" {
				next = order.Status
			}
			if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Updates(map[string]interface{}{"status": next, "trade_no": trade.TradeNo, "pay_time": trade.PaidAt}).Error; err != nil {
				return err
			}
			return resolveCloseReview(tx, trade.OrderSn, "channel_paid")
		}
		if trade.Status == "TRADE_CLOSED" && !paidStatus(order.Status) && !paidStatus(record.Status) && !record.Review {
			switch order.Status {
			case "", "PAYING", "WAIT_BUYER_PAY", "CLOSING", "TRADE_CLOSED":
			case "PAYMENT_REVIEW":
				pending, err := unresolvedCloseReview(tx, trade.OrderSn)
				if err != nil {
					return err
				}
				if !pending {
					return status.Error(codes.FailedPrecondition, "订单状态需要人工核对")
				}
			default:
				return status.Error(codes.FailedPrecondition, "订单状态需要人工核对")
			}
			if order.Status != "TRADE_CLOSED" {
				if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Updates(map[string]interface{}{"status": "TRADE_CLOSED", "trade_no": trade.TradeNo}).Error; err != nil {
					return err
				}
			}
			if err := addOrderEvent(tx, order.OrderSn, "order-reback", 0); err != nil {
				return err
			}
			refund = true
			return resolveCloseReview(tx, trade.OrderSn, "channel_closed")
		}
		return nil
	})
	return refund, err
}

func paymentError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "订单不存在")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	zap.S().Errorw("支付结果持久化失败", "error", err)
	return status.Error(codes.Unavailable, "支付结果暂时无法确认，请稍后重试")
}

// settleClosingOrder never treats a missing trade or a close acknowledgment alone
// as evidence that payment is impossible. Only a verified CLOSED query releases
// inventory; a lost response remains CLOSING for the recovery loop.
func settleClosingOrder(ctx context.Context, number string) (bool, error) {
	if global.PaymentGateway == nil || global.ServerConfig == nil || !global.ServerConfig.Alipay.Enabled {
		return false, status.Error(codes.Unavailable, "支付渠道未就绪，关单待核对")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	trade, err := global.PaymentGateway.Query(requestCtx, number)
	if errors.Is(err, payment.ErrNotFound) {
		// A page request may create the trade after the first query. A close
		// attempt followed by a new query can resolve that race; neither the
		// not-found result nor the close acknowledgment alone releases stock.
		pending, pendingErr := closureStillPending(requestCtx, number)
		if pendingErr != nil {
			return false, pendingErr
		}
		if !pending {
			return false, nil
		}
		closeErr := global.PaymentGateway.Close(requestCtx, number)
		trade, err = global.PaymentGateway.Query(requestCtx, number)
		if err != nil {
			return false, errors.Join(err, closeErr)
		}
	}
	if err != nil {
		return false, err
	}
	return settleQueriedTrade(requestCtx, number, trade)
}

func settleQueriedTrade(ctx context.Context, number string, trade payment.Trade) (bool, error) {
	if trade.OrderSn != number {
		return false, status.Error(codes.FailedPrecondition, "渠道查询订单号不匹配")
	}
	if err := validatePaymentTrade(trade); err != nil {
		return false, err
	}
	if trade.Status != "WAIT_BUYER_PAY" {
		return applyPaymentTrade(ctx, trade)
	}
	// Record and validate the bound amount/identity before issuing close.
	if _, err := applyPaymentTrade(ctx, trade); err != nil {
		return false, err
	}
	pending, err := closureStillPending(ctx, number)
	if err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	closeErr := global.PaymentGateway.Close(ctx, number)
	trade, err = global.PaymentGateway.Query(ctx, number)
	if err != nil {
		return false, errors.Join(closeErr, err)
	}
	if trade.OrderSn != number {
		return false, status.Error(codes.FailedPrecondition, "渠道查询订单号不匹配")
	}
	if trade.Status == "WAIT_BUYER_PAY" {
		return false, status.Error(codes.Unavailable, "支付渠道尚未确认关单")
	}
	return applyPaymentTrade(ctx, trade)
}
