package mqruntime

import (
	"context"
	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"testing"
	"time"
)

type stoppedConsumer struct{ rocketmq.PushConsumer }

func (*stoppedConsumer) Shutdown() error { return nil }
func TestShutdownCancelsAndJoinsConsumerCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &drainingConsumer{PushConsumer: &stoppedConsumer{}, ctx: ctx, cancel: cancel}
	entered, finished := make(chan struct{}), make(chan struct{})
	callback := c.wrap(func(ctx context.Context, _ ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		close(entered)
		<-ctx.Done()
		close(finished)
		return consumer.ConsumeRetryLater, nil
	})
	go callback(context.Background())
	<-entered
	done := make(chan error, 1)
	go func() { done <- c.Shutdown() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel callback")
	}
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned while callback active")
	}
	called := false
	if result, _ := c.wrap(func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		called = true
		return consumer.ConsumeSuccess, nil
	})(context.Background()); result != consumer.ConsumeRetryLater || called {
		t.Fatal("admitted callback after shutdown")
	}
}
