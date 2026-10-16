package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/goods_srv/model/proto"
	"grpctest/internal/tracing"
)

// labQueryDelay is opt-in only in this isolated runner; no request can set the delay.
func labQueryDelay(path string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		query, ok := req.(*proto.GoodsFilterRequest)
		if path == "" || info.FullMethod != "/Goods/GoodsList" || !ok || !query.GetStoreVisibleOnly() {
			return handler(ctx, req)
		}
		body, err := os.ReadFile(path)
		ms, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
		if err != nil || parseErr != nil || ms < 0 || ms > 5000 {
			return nil, status.Error(codes.Internal, "invalid lab delay control")
		}
		if ms == 0 {
			return handler(ctx, req)
		}
		waitCtx, span := otel.Tracer("grpctest/lab").Start(ctx, "lab.goods.delay")
		span.SetAttributes(attribute.Int("lab.delay_ms", ms))
		timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-waitCtx.Done():
			tracing.End(span, waitCtx.Err())
			return nil, status.FromContextError(waitCtx.Err()).Err()
		case <-timer.C:
			span.End()
			return handler(ctx, req)
		}
	}
}
