package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestConsumersKeepIndependentMessageParentsAfterPersistence(t *testing.T) {
	oldProvider, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(oldProvider)
	defer otel.SetTextMapPropagator(oldProp)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	for index, id := range []string{"11111111111111111111111111111111", "33333333333333333333333333333333"} {
		parent := PropertiesContext(context.Background(), map[string]string{"traceparent": "00-" + id + "-2222222222222222-01"})
		// JSON round-trip represents the durable outbox, after the original context is gone.
		data, _ := json.Marshal(Properties(parent))
		var stored map[string]string
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		msg := &primitive.MessageExt{Message: *primitive.NewMessage("order-reback", []byte("private-payload")), ReconsumeTimes: int32(index)}
		InjectMessage(PropertiesContext(context.Background(), stored), &msg.Message)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := ProcessMessage(ctx, msg, "order-reback", func(ctx context.Context) error {
			if ctx.Err() != context.Canceled {
				t.Fatal("lost runtime shutdown cancellation")
			}
			if trace.SpanContextFromContext(ctx).TraceID().String() != id {
				t.Fatal("mixed message contexts")
			}
			if index == 1 {
				return errors.New("private-payload")
			}
			return nil
		})
		if (err != nil) != (index == 1) {
			t.Fatal("changed processing result")
		}
	}
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans", len(spans))
	}
	for index, span := range spans {
		if !span.Parent.IsRemote() || span.Parent.SpanID().String() != "2222222222222222" || span.SpanKind != trace.SpanKindConsumer {
			t.Fatal("invalid consumer parent/kind")
		}
		if index == 1 && (span.Status.Code != codes.Error || span.Status.Description != "operation failed") {
			t.Fatal("failed processing was not safely marked")
		}
	}
}
