package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/payment"
)

// These durations control escalation and load, never proof of non-payment.
const closeReviewAfter = 15 * time.Minute
const closeReviewInterval = 5 * time.Minute

func unresolvedCloseReview(tx *gorm.DB, number string) (bool, error) {
	var count int64
	err := tx.Model(&model.OrderCloseReview{}).Where("order_sn = ? AND resolved_at IS NULL", number).Count(&count).Error
	return count > 0, err
}

func resolveCloseReview(tx *gorm.DB, number, resolution string) error {
	return tx.Model(&model.OrderCloseReview{}).Where("order_sn = ? AND resolved_at IS NULL", number).
		Updates(map[string]interface{}{"resolved_at": time.Now(), "resolution": resolution}).Error
}

func validateCloseReviewMerchant(tx *gorm.DB, number, appID, sellerID string) error {
	var review model.OrderCloseReview
	if err := tx.Where("order_sn = ?", number).First(&review).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if (review.AppID != "" && review.AppID != appID) || (review.SellerID != "" && review.SellerID != sellerID) {
		return status.Error(codes.FailedPrecondition, "支付商户配置已改变，请核对原收尾记录")
	}
	return nil
}

// Recheck locally committed evidence before another channel write. The channel
// call remains outside the lock; final state arbitration still uses the lock.
func closureStillPending(ctx context.Context, number string) (bool, error) {
	allowed := false
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", number).First(&order).Error; err != nil {
			return err
		}
		if paidStatus(order.Status) || order.Status == "TRADE_CLOSED" {
			return nil
		}
		if order.PayType != "alipay" {
			return status.Error(codes.FailedPrecondition, "订单支付方式不匹配")
		}
		if err := ensureRefundAllowed(tx, order); err != nil {
			return err
		}
		if global.ServerConfig == nil {
			return status.Error(codes.Unavailable, "支付配置尚未就绪")
		}
		if err := validateCloseReviewMerchant(tx, number, global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID); err != nil {
			return err
		}
		switch order.Status {
		case "WAIT_BUYER_PAY", "PAYING", "CLOSING":
			allowed = true
		case "PAYMENT_REVIEW":
			pending, err := unresolvedCloseReview(tx, number)
			if err != nil {
				return err
			}
			allowed = pending
		default:
			return status.Error(codes.FailedPrecondition, "订单状态不允许自动关单")
		}
		return nil
	})
	return allowed, err
}

// All callers reacquire the order lock after network I/O. A concurrent verified
// payment/closure wins over a stale not-found result and cannot be downgraded.
func recordCloseFailure(ctx context.Context, number string, cause error) error {
	escalated := false
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", number).First(&order).Error; err != nil {
			return err
		}
		if order.Status != "CLOSING" && order.Status != "PAYMENT_REVIEW" {
			escalated = true // a terminal/concurrent result already owns this order
			return nil
		}
		if err := ensureRefundAllowed(tx, order); err != nil {
			return err
		}
		var decision model.OrderTransaction
		if err := tx.Where("order_sn = ?", number).First(&decision).Error; err != nil {
			return err
		}
		if decision.State != model.OrderCommitted || decision.ExpiresAt == nil || time.Now().Before(*decision.ExpiresAt) {
			return status.Error(codes.FailedPrecondition, "订单尚不具备超时核对条件")
		}
		now := time.Now()
		reason := "channel_result_unknown"
		if errors.Is(cause, payment.ErrNotFound) {
			reason = "trade_not_found"
		}
		review := model.OrderCloseReview{OrderSn: number, Reason: reason, FirstSeenAt: now, LastCheckedAt: now, NextCheckAt: now}
		if global.ServerConfig != nil {
			review.AppID, review.SellerID = global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID
			if err := validateCloseReviewMerchant(tx, number, review.AppID, review.SellerID); err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&review).Error; err != nil {
			return err
		}
		escalated = !now.Before(decision.ExpiresAt.Add(closeReviewAfter))
		next := now
		if escalated {
			next = now.Add(closeReviewInterval)
			if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "PAYMENT_REVIEW").Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.OrderCloseReview{}).Where("order_sn = ? AND resolved_at IS NULL", number).
			Updates(map[string]interface{}{"reason": reason, "attempts": gorm.Expr("attempts + 1"), "last_checked_at": now, "next_check_at": next}).Error
	})
	if err != nil {
		return err
	}
	if escalated {
		// The durable review and recovery loop now own the retry; acknowledge MQ.
		return nil
	}
	return cause
}

type CloseReviewInfo struct {
	OrderID       int32                   `json:"order_id"`
	OrderSn       string                  `json:"order_sn"`
	Status        string                  `json:"status"`
	TotalCents    int64                   `json:"total_cents"`
	StockReleased bool                    `json:"stock_released"`
	ExpiresAt     *time.Time              `json:"expires_at"`
	Review        *model.OrderCloseReview `json:"review,omitempty"`
}

func InspectCloseReview(ctx context.Context, orderID int32) (CloseReviewInfo, error) {
	info := CloseReviewInfo{}
	if orderID <= 0 {
		return info, status.Error(codes.InvalidArgument, "无效订单 ID")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Where("id = ?", orderID).First(&order).Error; err != nil {
			return err
		}
		var decision model.OrderTransaction
		if err := tx.Where("order_sn = ?", order.OrderSn).First(&decision).Error; err != nil {
			return err
		}
		info = CloseReviewInfo{OrderID: order.ID, OrderSn: order.OrderSn, Status: order.Status, ExpiresAt: decision.ExpiresAt, StockReleased: decision.StockReleased}
		if order.TotalCents != nil {
			info.TotalCents = *order.TotalCents
		}
		var review model.OrderCloseReview
		if err := tx.Where("order_sn = ?", order.OrderSn).First(&review).Error; err == nil {
			info.Review = &review
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	})
	return info, err
}

type FenceCloseRequest struct {
	OrderID   int32
	RequestID string
	Operator  string
	BuyerID   string
}

var closeBuyerID = regexp.MustCompile(`^2088[0-9]{12}$`)
var closeRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// FenceAndCloseOrder is an operator-only sandbox tool, not a public RPC. Its
// durable intent and original merchant order number make retries resumable. It
// never treats missing/create/close acknowledgments as permission to refund.
func FenceAndCloseOrder(ctx context.Context, req FenceCloseRequest) (CloseReviewInfo, error) {
	if req.OrderID <= 0 || !closeRequestID.MatchString(req.RequestID) || strings.TrimSpace(req.Operator) != req.Operator || req.Operator == "" || len(req.Operator) > 100 || strings.ContainsAny(req.Operator, "\r\n\x00") || !closeBuyerID.MatchString(req.BuyerID) {
		return CloseReviewInfo{}, status.Error(codes.InvalidArgument, "请提供订单、请求编号、操作者及有效沙箱买家 ID")
	}
	if global.ServerConfig == nil || !global.ServerConfig.Alipay.Enabled || req.BuyerID == global.ServerConfig.Alipay.SellerID {
		return CloseReviewInfo{}, status.Error(codes.FailedPrecondition, "需要启用沙箱支付并使用买家账号")
	}
	creator, ok := global.PaymentGateway.(payment.ClosureCreator)
	if !ok {
		return CloseReviewInfo{}, status.Error(codes.FailedPrecondition, "支付适配器不支持沙箱关单登记")
	}
	var order model.OrderInfo
	var fenceExpiry time.Time
	alreadyResolved := false
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.OrderID).First(&order).Error; err != nil {
			return err
		}
		var decision model.OrderTransaction
		if err := tx.Where("order_sn = ?", order.OrderSn).First(&decision).Error; err != nil {
			return err
		}
		if order.PayType != "alipay" || decision.State != model.OrderCommitted || decision.ExpiresAt == nil || time.Now().Before(*decision.ExpiresAt) || order.TotalCents == nil || *order.TotalCents <= 0 || *order.TotalCents > payment.MaxAmountCents {
			return status.Error(codes.FailedPrecondition, "仅允许收尾已到期且金额明确的支付宝订单")
		}
		var review model.OrderCloseReview
		err := tx.Where("order_sn = ?", order.OrderSn).First(&review).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		fenceExpiry = time.Now().Add(5 * time.Minute).Truncate(time.Second)
		if review.ActionExpiresAt != nil {
			fenceExpiry = *review.ActionExpiresAt
		}
		if review.RequestID != "" && review.ActionExpiresAt == nil {
			return status.Error(codes.FailedPrecondition, "收尾请求缺少固定期限，请核对记录")
		}
		// Freeze merchant, amount, buyer and the short channel deadline before
		// any Create request. A config change cannot move this intent elsewhere.
		raw, _ := json.Marshal(struct {
			Request                         FenceCloseRequest
			Scope, AppID, SellerID, OrderSn string
			AmountCents, ExpiresAt          int64
		}{req, "sandbox-FACE_TO_FACE_PAYMENT-closure-v1", global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID, order.OrderSn, *order.TotalCents, fenceExpiry.Unix()})
		digest := sha256.Sum256(raw)
		hash := hex.EncodeToString(digest[:])
		if review.RequestID != "" && (review.RequestID != req.RequestID || review.RequestHash != hash || review.Operator != req.Operator) {
			return status.Error(codes.AlreadyExists, "该订单已有收尾请求，请用相同编号和参数继续")
		}
		if paidStatus(order.Status) || order.Status == "TRADE_CLOSED" {
			alreadyResolved = true
			return nil
		}
		if decision.StockReleased || (order.Status != "WAIT_BUYER_PAY" && order.Status != "PAYING" && order.Status != "CLOSING" && order.Status != "PAYMENT_REVIEW") {
			return status.Error(codes.FailedPrecondition, "订单不能执行沙箱关单登记")
		}
		if err := ensureRefundAllowed(tx, order); err != nil {
			return err
		}
		if err := validateCloseReviewMerchant(tx, order.OrderSn, global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID); err != nil {
			return err
		}
		if order.Status == "PAYMENT_REVIEW" && (review.OrderSn == "" || review.ResolvedAt != nil) {
			return status.Error(codes.FailedPrecondition, "该付款异常不属于交易缺失核对")
		}
		now := time.Now()
		if review.OrderSn == "" {
			review = model.OrderCloseReview{OrderSn: order.OrderSn, Reason: "operator_fence_requested", FirstSeenAt: now, LastCheckedAt: now, NextCheckAt: now, AppID: global.ServerConfig.Alipay.AppID, SellerID: global.ServerConfig.Alipay.SellerID}
			if err := tx.Create(&review).Error; err != nil {
				return err
			}
		}
		if review.RequestID == "" {
			if err := tx.Model(&model.OrderCloseReview{}).Where("order_sn = ?", order.OrderSn).Updates(map[string]interface{}{"request_id": req.RequestID, "operator": req.Operator, "request_hash": hash, "action_started_at": now, "action_expires_at": fenceExpiry}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "PAYMENT_REVIEW").Error
	})
	if err != nil {
		return CloseReviewInfo{}, err
	}
	if alreadyResolved {
		return InspectCloseReview(ctx, req.OrderID)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	trade, queryErr := global.PaymentGateway.Query(requestCtx, order.OrderSn)
	if queryErr == nil {
		_, err = settleQueriedTrade(requestCtx, order.OrderSn, trade)
	} else if errors.Is(queryErr, payment.ErrNotFound) {
		// Even a lost Create acknowledgment is followed by Query/Close/Query. A
		// repeated call uses the same merchant order number, never a new purchase.
		if !time.Now().Before(fenceExpiry) {
			return inspectCloseReviewExpired(ctx, req.OrderID)
		}
		pending, pendingErr := closureStillPending(requestCtx, order.OrderSn)
		if pendingErr != nil {
			return CloseReviewInfo{}, pendingErr
		}
		if !pending {
			return InspectCloseReview(ctx, req.OrderID)
		}
		_ = creator.CreateForClosure(requestCtx, payment.ClosureRequest{OrderSn: order.OrderSn, AmountCents: *order.TotalCents, BuyerID: req.BuyerID, ExpiresAt: fenceExpiry})
		_, err = settleClosingOrder(requestCtx, order.OrderSn)
	} else {
		err = queryErr
	}
	if err != nil {
		if recordErr := recordCloseFailure(ctx, order.OrderSn, err); recordErr != nil && !errors.Is(recordErr, err) {
			return CloseReviewInfo{}, recordErr
		}
	}
	info, inspectErr := InspectCloseReview(ctx, req.OrderID)
	if inspectErr != nil {
		return info, inspectErr
	}
	if info.Status != "TRADE_CLOSED" && !paidStatus(info.Status) {
		return info, status.Error(codes.Unavailable, "渠道尚未确认终态，库存保持占用；请用同一请求重试")
	}
	return info, nil
}

func inspectCloseReviewExpired(ctx context.Context, orderID int32) (CloseReviewInfo, error) {
	info, err := InspectCloseReview(ctx, orderID)
	if err != nil {
		return info, err
	}
	return info, status.Error(codes.FailedPrecondition, "固定的渠道登记期限已过且交易仍不存在；不能自动延长期限")
}
