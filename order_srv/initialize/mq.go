package initialize

import (
	"context"
	"time"

	"go.uber.org/zap"
	"grpctest/internal/mqruntime"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
)

// InitMQ is part of the existing startup sequence, before accepting order RPCs.
// Its cleanup must run after RPC handlers have drained and before DB/clients close.
func InitMQ(startup context.Context) (func(), error) {
	c := global.ServerConfig.MqInfo
	if err := c.Validate(true); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return func() {}, nil
	}
	if err := mqruntime.WaitTopics(startup, c, "order-reback", "order_timeout"); err != nil {
		return nil, err
	}
	p, err := mqruntime.Producer(c, "grpctest-order-events")
	if err != nil {
		return nil, err
	}
	transaction, err := mqruntime.TransactionProducer(c, &handler.TransactionListener{})
	if err != nil {
		_ = p.Shutdown()
		return nil, err
	}
	// Publish dependencies once; never clear them while callbacks may still run.
	global.MQOrder, global.MQInventory, global.MQTransactions = p, p, transaction
	consumer, err := mqruntime.Consumer(c, "grpctest-order-timeout", "order_timeout", handler.TimeOutReback)
	if err != nil {
		_ = transaction.Shutdown()
		_ = p.Shutdown()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			cycle, stop := context.WithTimeout(ctx, 15*time.Second)
			if err := handler.RecoverOrders(cycle); err != nil && ctx.Err() == nil {
				zap.S().Errorw("订单恢复未完成，将自动重试", "error", err)
			}
			stop()
			cycle, stop = context.WithTimeout(ctx, 15*time.Second)
			if err := handler.DispatchOrderEvents(cycle, 100); err != nil && ctx.Err() == nil {
				zap.S().Errorw("订单消息待重发", "error", err)
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done; _ = consumer.Shutdown(); _ = transaction.Shutdown(); _ = p.Shutdown() }, nil
}
