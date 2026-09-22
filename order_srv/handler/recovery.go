package handler

import (
	"context"
	"errors"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

// RecoverOrders fences abandoned creation attempts, closes expired demo orders,
// and confirms inventory release independently of broker retries / dead letters.
// Each decision shares its lock with the foreground creation transaction.
func RecoverOrders(ctx context.Context) error {
	var failures error
	var preparing []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Where("state = ? AND created_at < ?", model.OrderPreparing, time.Now().Add(-time.Minute)).Order("created_at").Limit(100).Find(&preparing).Error; err != nil {
		return err
	}
	for _, record := range preparing {
		if cancelOrderMessage(ctx, record.OrderSn) == primitive.UnkonwnState {
			failures = errors.Join(failures, errors.New("cannot cancel abandoned order"))
		}
	}
	var expired []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Table("order_transaction AS t").Select("t.*").Joins("JOIN orderinfo o ON o.order_sn = t.order_sn").Where("t.state = ? AND t.expires_at <= ? AND o.pay_type = ? AND o.status = ?", model.OrderCommitted, time.Now(), "demo", "WAIT_BUYER_PAY").Order("t.expires_at").Limit(100).Find(&expired).Error; err != nil {
		return errors.Join(failures, err)
	}
	for _, record := range expired {
		if _, err := closeOrder(ctx, record.OrderSn); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	var released []model.OrderTransaction
	if err := global.DB.WithContext(ctx).Table("order_transaction AS t").Select("t.*").Joins("LEFT JOIN orderinfo o ON o.order_sn = t.order_sn").Where("t.stock_released = ? AND (t.state = ? OR (t.state = ? AND o.status = ? AND o.pay_type = ?))", false, model.OrderCancelled, model.OrderCommitted, "TRADE_CLOSED", "demo").Order("t.updated_at").Limit(100).Find(&released).Error; err != nil {
		return errors.Join(failures, err)
	}
	for _, record := range released {
		requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		// MQ remains the primary compensation path. This idempotent RPC is a fallback
		// for exhausted broker checks/retries or a consumer dead letter. It also writes
		// a cancellation tombstone when no deduction has committed yet.
		_, err := global.InventorySrvClient.Reback(requestCtx, &proto.SellInfo{OrderSn: record.OrderSn})
		cancel()
		update := map[string]interface{}{"updated_at": time.Now()}
		if err == nil {
			update["stock_released"] = true
		} else {
			failures = errors.Join(failures, err)
		}
		if err := global.DB.WithContext(ctx).Model(&model.OrderTransaction{}).Where("order_sn = ?", record.OrderSn).Updates(update).Error; err != nil {
			failures = errors.Join(failures, err)
		}
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
	}
	return failures
}
