package tracing

import (
	"context"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"testing"
)

func TestMessageContextSurvivesCancelledRequest(t *testing.T) {
	old := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(old)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	id, _ := trace.TraceIDFromHex("11111111111111111111111111111111")
	spanID, _ := trace.SpanIDFromHex("2222222222222222")
	request, cancel := context.WithCancel(trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: spanID, TraceFlags: trace.FlagsSampled})))
	msg := primitive.NewMessage("order-reback", []byte("private-business-payload"))
	InjectMessage(request, msg)
	cancel()
	callback := MessageContext(context.Background(), msg)
	if callback.Err() != nil {
		t.Fatal("callback inherited cancelled request")
	}
	sc := trace.SpanContextFromContext(callback)
	if sc.TraceID() != id || sc.SpanID() != spanID || !sc.IsSampled() {
		t.Fatal("lost message trace context")
	}
	if len(msg.GetProperties()) != 1 || msg.GetProperty("traceparent") == "" {
		t.Fatal("unexpected propagated metadata")
	}
}
