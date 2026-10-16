package tracing

import (
	"context"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// InjectMessage carries only the configured trace context, not business data.
func InjectMessage(ctx context.Context, msg *primitive.Message) {
	for k, v := range Properties(ctx) {
		msg.WithProperty(k, v)
	}
}

// MessageContext keeps the callback's independent cancellation/deadline policy.
func MessageContext(ctx context.Context, msg *primitive.Message) context.Context {
	if msg == nil {
		return ctx
	}
	return PropertiesContext(ctx, msg.GetProperties())
}

// Properties can be persisted with an outbox event and restored after restart.
func Properties(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

func PropertiesContext(ctx context.Context, properties map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(properties))
}

// ProcessMessage creates a separate consumer span for each message, even in a batch.
// The runtime context retains its shutdown cancellation; only trace identity is extracted.
func ProcessMessage(ctx context.Context, msg *primitive.MessageExt, topic string, process func(context.Context) error) error {
	if msg != nil {
		ctx = MessageContext(ctx, &msg.Message)
	}
	ctx, span := otel.Tracer("grpctest/messaging").Start(ctx, topic+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "rocketmq"), attribute.String("messaging.destination.name", topic)))
	if msg != nil {
		span.SetAttributes(attribute.Int("rocketmq.reconsume_times", int(msg.ReconsumeTimes)))
	}
	err := process(ctx)
	End(span, err)
	return err
}
