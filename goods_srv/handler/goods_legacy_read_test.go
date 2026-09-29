package handler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
)

func TestLegacyGoodsListHonorsContextAndReadFailures(t *testing.T) {
	for _, stage := range []string{"category", "count", "page", "canceled", "expired"} {
		t.Run(stage, func(t *testing.T) {
			mock := batchGoodsMockDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := &proto.GoodsFilterRequest{}
			want := codes.Internal
			failure := errors.New("private database detail")
			switch stage {
			case "category":
				request.TopCategory = 1
				mock.ExpectQuery("SELECT .* FROM `category`").WillReturnError(failure)
			case "count":
				mock.ExpectQuery("SELECT count").WillReturnError(failure)
			case "page":
				mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("SELECT .* FROM `goods`").WillReturnError(failure)
			case "canceled":
				cancel()
				want = codes.Canceled
			case "expired":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				want = codes.DeadlineExceeded
			}
			got, err := (&handler.GoodsServer{}).GoodsList(ctx, request)
			if got != nil || status.Code(err) != want {
				t.Fatalf("legacy list must propagate failure: got %v / %v, want %v", got, err, want)
			}
			if want == codes.Internal && status.Convert(err).Message() != "查询商品列表失败" {
				t.Fatal("raw database error exposed")
			}
		})
	}
}
