package handler

import (
	"context"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func catalogDBError(ctx context.Context, err error, message string) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	zap.S().Errorw(message, "error", err)
	return status.Error(codes.Internal, message)
}
