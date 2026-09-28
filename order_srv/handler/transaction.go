package handler

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
)

var validOrderSN = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

func orderDecision(ctx context.Context, number string, cancel bool) (int32, error) {
	state := model.OrderPreparing
	if cancel {
		state = model.OrderCancelled
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := model.OrderTransaction{OrderSn: number, State: state}
		if err := tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"order_sn"})}).Create(&record).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", number).First(&record).Error; err != nil {
			return err
		}
		state = record.State
		if cancel && state == model.OrderPreparing {
			if err := tx.Model(&model.OrderTransaction{}).Where("order_sn = ?", number).Update("state", model.OrderCancelled).Error; err != nil {
				return err
			}
			state = model.OrderCancelled
		}
		if state < model.OrderPreparing || state > model.OrderCancelled {
			return errors.New("invalid order transaction state")
		}
		if cancel && state == model.OrderCancelled {
			return addOrderEvent(tx, number, "order-reback", 0)
		}
		return nil
	})
	return state, err
}

// A durable cancellation decision is required before releasing the compensation message.
func cancelOrderMessage(ctx context.Context, number string) primitive.LocalTransactionState {
	state, err := orderDecision(ctx, number, true)
	if err != nil {
		zap.S().Errorw("订单事务决定写入失败，等待回查", "order_sn", number, "error", err)
		return primitive.UnkonwnState
	}
	if state == model.OrderCommitted {
		return primitive.RollbackMessageState
	}
	return primitive.CommitMessageState
}

func addOrderEvent(tx *gorm.DB, number, topic string, delay int) error {
	body, err := json.Marshal(struct{ OrderSn string }{number})
	if err != nil {
		return err
	}
	event := model.OrderEvent{OrderSn: number, Topic: topic, Body: string(body), DelayLevel: delay}
	return tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"order_sn"})}).Create(&event).Error
}

// publishOrderEvent runs after the business transaction. A lost acknowledgment can
// duplicate a message, so consumers must be idempotent. A failed send leaves it pending.
func publishOrderEvent(ctx context.Context, number, topic string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return global.DB.WithContext(requestCtx).Transaction(func(tx *gorm.DB) error {
		if topic == "order-reback" {
			var order model.OrderInfo
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", number).First(&order).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if order.Status != "TRADE_CLOSED" {
					return status.Error(codes.FailedPrecondition, "未关闭订单不能返库")
				}
				if err := ensureRefundAllowed(tx, order); err != nil {
					return err
				}
			}
		}
		var event model.OrderEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ? AND topic = ?", number, topic).First(&event).Error; err != nil {
			return err
		}
		if event.Published {
			return nil
		}
		producer := global.MQOrder
		if topic == "order-reback" {
			producer = global.MQInventory
		}
		if producer == nil {
			return status.Error(codes.Unavailable, "消息生产者尚未就绪")
		}
		msg := primitive.NewMessage(topic, []byte(event.Body))
		msg.WithKeys([]string{number})
		if event.DelayLevel > 0 {
			msg.WithDelayTimeLevel(event.DelayLevel)
		}
		response, err := producer.SendSync(requestCtx, msg)
		if err != nil {
			return err
		}
		if response == nil || response.Status != primitive.SendOK {
			return errors.New("broker did not confirm message storage")
		}
		return tx.Model(&model.OrderEvent{}).Where("id = ?", event.ID).Update("published", true).Error
	})
}

// DispatchOrderEvents resumes pending sends after failures/restarts. The MQ runtime
// must schedule this before opening order creation; startup wiring remains a separate batch.
func DispatchOrderEvents(ctx context.Context, limit int) error {
	if limit < 1 || limit > 1000 {
		return status.Error(codes.InvalidArgument, "无效的消息批次大小")
	}
	var events []model.OrderEvent
	if err := global.DB.WithContext(ctx).Table("order_event AS e").Select("e.*").Where("e.published = ?", false).
		Where("NOT (e.topic = ? AND (EXISTS (SELECT 1 FROM orderinfo o WHERE o.order_sn = e.order_sn AND o.status <> ?) OR EXISTS (SELECT 1 FROM order_payment p WHERE p.order_sn = e.order_sn AND (p.review = ? OR p.status IN ?))))", "order-reback", "TRADE_CLOSED", true, []string{"TRADE_SUCCESS", "TRADE_FINISHED"}).
		Order("e.last_attempt_at, e.id").Limit(limit).Find(&events).Error; err != nil {
		return err
	}
	var failures error
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if err := global.DB.WithContext(ctx).Model(&model.OrderEvent{}).Where("id = ? AND published = ?", event.ID, false).UpdateColumn("last_attempt_at", time.Now()).Error; err != nil {
			return errors.Join(failures, err)
		}
		if err := publishOrderEvent(ctx, event.OrderSn, event.Topic); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

// Demo close and refund intent commit atomically. Alipay first persists CLOSING,
// then confirms the channel result outside the database transaction.
func closeOrder(ctx context.Context, number string) (bool, error) {
	refund, reconcile := false, false
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", number).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				var decision model.OrderTransaction
				if e := tx.Where("order_sn = ?", number).First(&decision).Error; e != nil {
					return e
				}
				if decision.State == model.OrderCancelled {
					return nil
				}
				return status.Error(codes.Unavailable, "订单尚未确定，稍后重试")
			}
			return err
		}
		if paidStatus(order.Status) {
			return nil
		}
		if order.PayType != "demo" && order.PayType != "alipay" {
			return status.Error(codes.FailedPrecondition, "订单支付方式需要人工核对")
		}
		if order.Status == "TRADE_CLOSED" {
			if err := ensureRefundAllowed(tx, order); err != nil {
				return err
			}
			if err := addOrderEvent(tx, number, "order-reback", 0); err != nil {
				return err
			}
			refund = true
			return nil
		}
		switch order.Status {
		case "", "PAYING", "WAIT_BUYER_PAY", "CLOSING":
		case "PAYMENT_REVIEW":
			var review model.OrderCloseReview
			if err := tx.Where("order_sn = ? AND resolved_at IS NULL", number).First(&review).Error; err != nil {
				return status.Error(codes.FailedPrecondition, "该付款异常不能自动关闭")
			}
			if time.Now().Before(review.NextCheckAt) {
				return nil
			}
		default:
			return status.Error(codes.FailedPrecondition, "订单状态不能自动关闭")
		}
		var decision model.OrderTransaction
		if err := tx.Where("order_sn = ?", number).First(&decision).Error; err != nil {
			return err
		}
		if decision.State != model.OrderCommitted || decision.ExpiresAt == nil {
			return errors.New("order has no expiry")
		}
		if time.Now().Before(*decision.ExpiresAt) {
			return status.Error(codes.Unavailable, "订单尚未到期")
		}
		if order.PayType == "alipay" {
			if global.ServerConfig != nil {
				if err := validateCloseReviewMerchant(tx, number, global.ServerConfig.Alipay.AppID, global.ServerConfig.Alipay.SellerID); err != nil {
					return err
				}
			}
			if err := ensureRefundAllowed(tx, order); err != nil {
				return err
			}
			if order.Status != "PAYMENT_REVIEW" {
				if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "CLOSING").Error; err != nil {
					return err
				}
			}
			reconcile = true
			return nil
		}
		if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "TRADE_CLOSED").Error; err != nil {
			return err
		}
		if err := addOrderEvent(tx, number, "order-reback", 0); err != nil {
			return err
		}
		refund = true
		return nil
	})
	if err != nil || !reconcile {
		return refund, err
	}
	refund, err = settleClosingOrder(ctx, number)
	if err != nil {
		return false, recordCloseFailure(ctx, number, err)
	}
	return refund, nil
}

// An exceptional payment observed after closure is an operator review item, not
// permission to publish a new refund. Already completed inventory releases are
// retained as facts and are never silently reversed here.
func ensureRefundAllowed(tx *gorm.DB, order model.OrderInfo) error {
	if paidStatus(order.Status) || order.PayTime != nil {
		return status.Error(codes.FailedPrecondition, "已支付订单不能自动返库")
	}
	if order.PayType != "alipay" {
		return nil
	}
	var count int64
	if err := tx.Model(&model.OrderPayment{}).Where("order_sn = ? AND (review = ? OR paid_at IS NOT NULL OR status IN ?)", order.OrderSn, true, []string{"TRADE_SUCCESS", "TRADE_FINISHED"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return status.Error(codes.FailedPrecondition, "支付结果需要人工核对，暂停自动返库")
	}
	return nil
}
