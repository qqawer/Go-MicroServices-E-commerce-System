package mqruntime

import (
	"context"
	"sync"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
)

// The SDK stops its transport on Shutdown but callbacks may still be running.
// Stop admitting callbacks and join existing ones before closing their DB/RPCs.
type callbackGroup struct {
	mu      sync.Mutex
	closing bool
	active  sync.WaitGroup
}

func (g *callbackGroup) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.active.Add(1)
	return true
}
func (g *callbackGroup) close() { g.mu.Lock(); g.closing = true; g.mu.Unlock() }

type drainingConsumer struct {
	rocketmq.PushConsumer
	callbacks callbackGroup
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	err       error
}

func (c *drainingConsumer) Shutdown() error {
	c.once.Do(func() { c.callbacks.close(); c.cancel(); c.err = c.PushConsumer.Shutdown(); c.callbacks.active.Wait() })
	return c.err
}
func (c *drainingConsumer) wrap(callback func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)) func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	return func(parent context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		if !c.callbacks.enter() {
			return consumer.ConsumeRetryLater, nil
		}
		defer c.callbacks.active.Done()
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(c.ctx, cancel)
		defer stop()
		defer cancel()
		result, err := callback(ctx, msgs...)
		if err != nil || result != consumer.ConsumeSuccess {
			return consumer.ConsumeRetryLater, nil
		}
		return consumer.ConsumeSuccess, nil
	}
}

type drainingTransaction struct {
	rocketmq.TransactionProducer
	listener  primitive.TransactionListener
	callbacks callbackGroup
	once      sync.Once
	err       error
}

func (p *drainingTransaction) ExecuteLocalTransaction(msg *primitive.Message) primitive.LocalTransactionState {
	if !p.callbacks.enter() {
		return primitive.UnkonwnState
	}
	defer p.callbacks.active.Done()
	return p.listener.ExecuteLocalTransaction(msg)
}
func (p *drainingTransaction) CheckLocalTransaction(msg *primitive.MessageExt) primitive.LocalTransactionState {
	if !p.callbacks.enter() {
		return primitive.UnkonwnState
	}
	defer p.callbacks.active.Done()
	return p.listener.CheckLocalTransaction(msg)
}
func (p *drainingTransaction) Shutdown() error {
	p.once.Do(func() { p.callbacks.close(); p.err = p.TransactionProducer.Shutdown(); p.callbacks.active.Wait() })
	return p.err
}
