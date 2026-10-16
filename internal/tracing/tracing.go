// Package tracing configures tracing once per service process.
package tracing

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init enables OTLP/HTTP only when an explicit traces endpoint is supplied.
// It is called at startup, never once per request. Call the result on shutdown.
func Init(service string) (func(), error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" || os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return func() {}, nil
	}
	ratio := 0.1
	if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
		var err error
		ratio, err = strconv.ParseFloat(raw, 64)
		if err != nil || !(ratio >= 0 && ratio <= 1) {
			return nil, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG must be a ratio between 0 and 1")
		}
	}
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithTimeout(2*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(2*time.Second)))
	otel.SetTracerProvider(provider)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			log.Printf("tracing shutdown: %v", err)
		}
	}, nil
}

// End records a bounded description; raw DB/HTTP errors may contain input data.
func End(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "operation failed")
	}
	span.End()
}
