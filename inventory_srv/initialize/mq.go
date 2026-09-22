package initialize

import (
	"context"
	"grpctest/internal/mqruntime"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
)

func InitMQ(startup context.Context) (func(), error) {
	c := global.ServerConfig.MqInfo
	if err := c.Validate(false); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return func() {}, nil
	}
	if err := mqruntime.WaitTopics(startup, c, "order-reback"); err != nil {
		return nil, err
	}
	consumer, err := mqruntime.Consumer(c, "grpctest-inventory-reback", "order-reback", handler.AutoReback)
	if err != nil {
		return nil, err
	}
	return func() { _ = consumer.Shutdown() }, nil
}
