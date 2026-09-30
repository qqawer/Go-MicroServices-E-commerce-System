package handler_test

import (
	"context"
	"regexp"
	"testing"

	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestCreateBannerPreservesZeroIndex(t *testing.T) {
	mock := catalogDatabase(t)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `banner`").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), nil, false, "https://example.invalid/banner.png", "https://example.invalid", int32(0)).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectCommit()
	got, err := (&handler.GoodsServer{}).CreateBanner(context.Background(), &proto.BannerRequest{
		Image: "https://example.invalid/banner.png", Url: "https://example.invalid", Index: 0,
	})
	if err != nil || got.GetId() != 7 || got.GetIndex() != 0 {
		t.Fatalf("explicit zero must be inserted and returned unchanged: %v, %v", got, err)
	}
}

func TestBannerListOrdersByIndexThenID(t *testing.T) {
	mock := catalogDatabase(t)
	mock.ExpectQuery("SELECT .*" + regexp.QuoteMeta("ORDER BY `index`,`id`")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "index"}).AddRow(9, 0).AddRow(12, 0).AddRow(7, 5))
	got, err := (&handler.GoodsServer{}).BannerList(context.Background(), &emptypb.Empty{})
	if err != nil || got.GetTotal() != 3 {
		t.Fatalf("list must order by priority, then ID: %v, %v", got, err)
	}
	for i, id := range []int32{9, 12, 7} {
		if got.Data[i].Id != id {
			t.Fatalf("list changed database order: %v", got.Data)
		}
	}
}
