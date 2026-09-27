package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/order_srv/proto"
)

func TestOrderDetailDatabaseErrorsDoNotBecomeNotFound(t *testing.T) {
	for _, stage := range []string{"order", "decision", "goods"} {
		t.Run(stage, func(t *testing.T) {
			mock := cartMock(t)
			order := mock.ExpectQuery("SELECT .* FROM `orderinfo`").WithArgs(7, 41, 1)
			if stage == "order" {
				order.WillReturnError(errors.New("private database failure"))
			} else {
				order.WillReturnRows(sqlmock.NewRows([]string{"id", "user", "order_sn"}).AddRow(7, 41, "detail-order"))
				decision := mock.ExpectQuery("SELECT .* FROM `order_transaction`").WithArgs("detail-order", 1)
				if stage == "decision" {
					decision.WillReturnError(errors.New("private database failure"))
				} else {
					decision.WillReturnRows(sqlmock.NewRows([]string{"order_sn"}))
					mock.ExpectQuery("SELECT .* FROM `ordergoods`").WithArgs(7).WillReturnError(errors.New("private database failure"))
				}
			}
			_, err := (&OrderServer{}).OrderDetail(context.Background(), &proto.OrderRequest{Id: 7, UserId: 41})
			if status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private database failure" {
				t.Fatal(err)
			}
		})
	}
}

func TestOrderDetailContextCancelsEveryDatabaseRead(t *testing.T) {
	for _, stage := range []string{"order", "decision", "goods"} {
		t.Run(stage, func(t *testing.T) {
			mock := cartMock(t)
			query := mock.ExpectQuery("SELECT .* FROM `orderinfo`")
			if stage != "order" {
				query.WillReturnRows(sqlmock.NewRows([]string{"id", "user", "order_sn"}).AddRow(7, 41, "detail-order"))
				query = mock.ExpectQuery("SELECT .* FROM `order_transaction`")
				if stage == "goods" {
					query.WillReturnRows(sqlmock.NewRows([]string{"order_sn"}))
					query = mock.ExpectQuery("SELECT .* FROM `ordergoods`")
				}
			}
			query.WillDelayFor(time.Second).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := (&OrderServer{}).OrderDetail(ctx, &proto.OrderRequest{Id: 7, UserId: 41})
			if status.Code(err) != codes.DeadlineExceeded || time.Since(start) > 500*time.Millisecond {
				t.Fatal("database read ignored deadline", stage, err)
			}
		})
	}
}

func TestOrderDetailOwnerFilterAndNotFound(t *testing.T) {
	mock := cartMock(t)
	mock.ExpectQuery("SELECT .* FROM `orderinfo`").WithArgs(7, 42, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err := (&OrderServer{}).OrderDetail(context.Background(), &proto.OrderRequest{Id: 7, UserId: 42})
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	for _, req := range []*proto.OrderRequest{nil, {}, {Id: -1}, {Id: 7, UserId: -1}} {
		if _, err := (&OrderServer{}).OrderDetail(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal("invalid selector accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&OrderServer{}).OrderDetail(ctx, &proto.OrderRequest{Id: 7}); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
}
