package handler

import (
	"context"
	"errors"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

// RecoverOrders fences abandoned creation attempts, reconciles expired orders,
// and confirms inventory release independently of broker retries / dead letters.
// Each decision shares its lock with the foreground creation transaction.
func RecoverOrders(ctx context.Context) error {
	var failures error
	var preparing []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Where("state = ? AND created_at < ?", model.OrderPreparing, time.Now().Add(-time.Minute)).Order("updated_at, order_sn").Limit(100).Find(&preparing).Error; err != nil {
		return err
	}
	for _, record := range preparing {
		if err := markRecoveryAttempt(ctx, record.OrderSn); err != nil {
			return errors.Join(failures, err)
		}
		if cancelOrderMessage(ctx, record.OrderSn) == primitive.UnkonwnState {
			failures = errors.Join(failures, errors.New("cannot cancel abandoned order"))
		}
	}
	// Previously closed orders must progress even when all later channel queries
	// consume their deadline. A second pass below handles newly closed orders.
	budget := 3 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) / 4; remaining < budget {
			budget = remaining
		}
	}
	releaseCtx, releaseCancel := context.WithTimeout(ctx, budget)
	failures = errors.Join(failures, recoverReleasedInventory(releaseCtx))
	releaseCancel()
	if ctx.Err() != nil {
		return errors.Join(failures, ctx.Err())
	}
	var expired []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Table("order_transaction AS t").Select("t.*").Joins("JOIN orderinfo o ON o.order_sn = t.order_sn").Where("t.state = ? AND t.expires_at <= ? AND o.pay_type IN ? AND o.status IN ?", model.OrderCommitted, time.Now(), []string{"demo", "alipay"}, []string{"WAIT_BUYER_PAY", "PAYING", "CLOSING"}).Where("NOT EXISTS (SELECT 1 FROM order_payment p WHERE p.order_sn = t.order_sn AND (p.review = ? OR p.status IN ?))", true, []string{"TRADE_SUCCESS", "TRADE_FINISHED"}).Order("t.updated_at, t.order_sn").Limit(100).Find(&expired).Error; err != nil {
		return errors.Join(failures, err)
	}
	for _, record := range expired {
		if err := markRecoveryAttempt(ctx, record.OrderSn); err != nil {
			return errors.Join(failures, err)
		}
		if _, err := closeOrder(ctx, record.OrderSn); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return errors.Join(failures, recoverReleasedInventory(ctx))
}

func recoverReleasedInventory(ctx context.Context) error {
	var failures error
	var released []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Table("order_transaction AS t").Select("t.*").Joins("LEFT JOIN orderinfo o ON o.order_sn = t.order_sn").Where("t.stock_released = ? AND (t.state = ? OR (t.state = ? AND o.status = ? AND o.pay_type IN ?))", false, model.OrderCancelled, model.OrderCommitted, "TRADE_CLOSED", []string{"demo", "alipay"}).Where("NOT EXISTS (SELECT 1 FROM order_payment p WHERE p.order_sn = t.order_sn AND (p.review = ? OR p.status IN ?))", true, []string{"TRADE_SUCCESS", "TRADE_FINISHED"}).Order("t.updated_at, t.order_sn").Limit(100).Find(&released).Error; err != nil {
		return errors.Join(failures, err)
	}
	for _, record := range released {
		if err := markRecoveryAttempt(ctx, record.OrderSn); err != nil {
			return errors.Join(failures, err)
		}
		err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var order model.OrderInfo
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", record.OrderSn).First(&order).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if order.Status != "TRADE_CLOSED" {
					return errors.New("order is not closed; inventory release skipped")
				}
				if err := ensureRefundAllowed(tx, order); err != nil {
					return err
				}
			}
			requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			// MQ is primary; this idempotent RPC also fences a late inventory Sell.
			if global.InventorySrvClient == nil {
				return errors.New("inventory recovery client is unavailable")
			}
			_, err = global.InventorySrvClient.Reback(requestCtx, &proto.SellInfo{OrderSn: record.OrderSn})
			if err != nil {
				return err
			}
			return tx.Model(&model.OrderTransaction{}).Where("order_sn = ?", record.OrderSn).Updates(map[string]interface{}{"stock_released": true, "updated_at": time.Now()}).Error
		})
		if err != nil {
			failures = errors.Join(failures, err)
		}
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
	}
	return failures
}

// Touch before a network attempt, including attempts that time out or fail. The
// next recovery pass therefore reaches untouched work even when old trades stay
// permanently unknown. Cancelled contexts do not touch the unattempted tail.
func markRecoveryAttempt(ctx context.Context, number string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return global.DB.WithContext(ctx).Model(&model.OrderTransaction{}).Where("order_sn = ?", number).UpdateColumn("updated_at", time.Now()).Error
}
