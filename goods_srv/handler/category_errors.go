package handler

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func categoryDBError(ctx context.Context, err error, message string) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		switch mysqlError.Number {
		case 1062:
			return status.Error(codes.AlreadyExists, "分类与品牌关联已存在")
		case 1452:
			return status.Error(codes.InvalidArgument, "关联的分类或品牌不存在")
		}
	}
	zap.S().Errorw(message, "error", err)
	return status.Error(codes.Internal, message)
}
