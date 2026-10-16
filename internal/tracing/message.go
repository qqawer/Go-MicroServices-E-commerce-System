package tracing

import (
	"context"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// InjectMessage carries only the configured trace context, not business data.
func InjectMessage(ctx context.Context, msg *primitive.Message) {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	for k, v := range carrier {
		msg.WithProperty(k, v)
	}
}

// MessageContext keeps the callback's independent cancellation/deadline policy.
func MessageContext(ctx context.Context, msg *primitive.Message) context.Context {
	if msg == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(msg.GetProperties()))
}
