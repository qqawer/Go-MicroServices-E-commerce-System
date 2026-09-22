// Package mqruntime constructs shared RocketMQ clients with consistent topics and groups.
package mqruntime

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/admin"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
)

type Config struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	// Creation is demo-only until payment reconciliation is implemented.
	Mode            string `json:"mode"`
	OrderTTLSeconds int    `json:"order_ttl_seconds"`
}

func (c Config) Address() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }
func (c Config) Validate(order bool) error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" || c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("rocket host and port are required when enabled")
	}
	if order && (c.Mode != "demo" || c.OrderTTLSeconds < 30 || c.OrderTTLSeconds > 86400) {
		return fmt.Errorf("rocket requires mode=demo and order_ttl_seconds between 30 and 86400")
	}
	return nil
}
func Producer(c Config, group string) (rocketmq.Producer, error) {
	p, err := rocketmq.NewProducer(producer.WithNameServer([]string{c.Address()}), producer.WithGroupName(group), producer.WithRetry(0), producer.WithSendMsgTimeout(3*time.Second))
	if err != nil {
		return nil, err
	}
	if err = p.Start(); err != nil {
		_ = p.Shutdown()
		return nil, err
	}
	return p, nil
}
func TransactionProducer(c Config, l primitive.TransactionListener) (rocketmq.TransactionProducer, error) {
	drain := &drainingTransaction{listener: l}
	p, err := rocketmq.NewTransactionProducer(drain, producer.WithNameServer([]string{c.Address()}), producer.WithGroupName("grpctest-order-transaction"), producer.WithRetry(0), producer.WithSendMsgTimeout(3*time.Second))
	if err != nil {
		return nil, err
	}
	if err = p.Start(); err != nil {
		_ = p.Shutdown()
		return nil, err
	}
	drain.TransactionProducer = p
	return drain, nil
}
func Consumer(c Config, group, topic string, callback func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)) (rocketmq.PushConsumer, error) {
	p, err := rocketmq.NewPushConsumer(consumer.WithNameServer([]string{c.Address()}), consumer.WithGroupName(group), consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset), consumer.WithConsumeMessageBatchMaxSize(16), consumer.WithMaxReconsumeTimes(16), consumer.WithConsumeTimeout(time.Minute))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	drain := &drainingConsumer{PushConsumer: p, ctx: ctx, cancel: cancel}
	if err = p.Subscribe(topic, consumer.MessageSelector{}, drain.wrap(callback)); err != nil {
		cancel()
		return nil, err
	}

	if err = p.Start(); err != nil {
		_ = drain.Shutdown()
		return nil, err
	}
	return drain, nil
}

// A listening broker is not yet proof that its topics are registered. Wait for
// routes before constructing consumers, including after a broker restart.
func WaitTopics(parent context.Context, c Config, topics ...string) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	client, err := admin.NewAdmin(admin.WithResolver(primitive.NewPassthroughResolver([]string{c.Address()})))
	if err != nil {
		return err
	}
	defer client.Close()
	for {
		ready := true
		for _, topic := range topics {
			queues, err := client.FetchPublishMessageQueues(ctx, topic)
			if err != nil || len(queues) == 0 {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("MQ topic routes unavailable: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
