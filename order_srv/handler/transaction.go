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
	if err := global.DB.WithContext(ctx).Where("published = ?", false).Order("id").Limit(limit).Find(&events).Error; err != nil {
		return err
	}
	var failures error
	for _, event := range events {
		if err := publishOrderEvent(ctx, event.OrderSn, event.Topic); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

// Closing state and refund intent commit atomically. A retry resumes the persisted event.
func closeOrder(ctx context.Context, number string) (bool, error) {
	refund := false
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
		switch order.Status {
		case "TRADE_SUCCESS", "TRADE_FINISHED":
			return nil
		case "", "PAYING", "WAIT_BUYER_PAY", "TRADE_CLOSED":
		default:
			return status.Error(codes.FailedPrecondition, "订单状态不能自动关闭")
		}
		if order.PayType == "demo" && order.Status != "TRADE_CLOSED" {
			var decision model.OrderTransaction
			if err := tx.Where("order_sn = ?", number).First(&decision).Error; err != nil {
				return err
			}
			if decision.ExpiresAt == nil {
				return errors.New("demo order has no expiry")
			}
			if time.Now().Before(*decision.ExpiresAt) {
				return status.Error(codes.Unavailable, "订单尚未到期")
			}
		}
		if order.Status != "TRADE_CLOSED" {
			if err := tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", "TRADE_CLOSED").Error; err != nil {
				return err
			}
		}
		if err := addOrderEvent(tx, number, "order-reback", 0); err != nil {
			return err
		}
		refund = true
		return nil
	})
	return refund, err
}
