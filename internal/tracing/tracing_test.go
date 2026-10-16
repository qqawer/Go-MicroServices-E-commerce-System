package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPShutdownFlushAndSafeError(t *testing.T) {
	received := make(chan *collector.ExportTraceServiceRequest, 2)
	collectorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		batch := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(data, batch); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path != "/v1/traces" {
			t.Errorf("unexpected export path: %s", r.URL.Path)
		}
		received <- batch
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collectorServer.Close()
	oldProvider, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(oldProvider)
	defer otel.SetTextMapPropagator(oldProp)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collectorServer.URL+"/v1/traces")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	shutdown, err := Init("goods-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier{
		"traceparent": "00-11111111111111111111111111111111-2222222222222222-01",
	})
	_, span := otel.Tracer("test").Start(ctx, "read")
	End(span, io.ErrUnexpectedEOF)
	shutdown()
	select {
	case batch := <-received:
		resource := batch.ResourceSpans[0]
		foundService := false
		for _, a := range resource.Resource.Attributes {
			if a.Key == "service.name" && a.Value.GetStringValue() == "goods-test" {
				foundService = true
			}
		}
		if !foundService {
			t.Fatal("missing service name")
		}
		spans := resource.ScopeSpans[0].Spans
		if len(spans) != 1 || spans[0].Name != "read" || spans[0].Status.Code != tracepb.Status_STATUS_CODE_ERROR || spans[0].Status.Message != "operation failed" {
			t.Fatalf("invalid flushed span: %v", spans)
		}
		if span.SpanContext().TraceID().String() != "11111111111111111111111111111111" {
			t.Fatal("lost propagated trace")
		}
		if spans[0].Status.Message == io.ErrUnexpectedEOF.Error() {
			t.Fatal("raw error exported")
		}
	default:
		t.Fatal("shutdown did not flush OTLP spans")
	}
}

func TestDisabledAndInvalidSampling(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	close, err := Init("disabled")
	if err != nil {
		t.Fatal(err)
	}
	close()
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:4318/v1/traces")
	for _, arg := range []string{"bad", "-1", "2", "NaN"} {
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", arg)
		if _, err := Init("bad"); err == nil {
			t.Fatalf("accepted sampling ratio %s", arg)
		}
	}
}
