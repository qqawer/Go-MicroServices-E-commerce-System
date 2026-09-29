package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"grpctest/order_srv/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOrderListOwnerCountAndStablePagination(t *testing.T) {
	mock := cartMock(t)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `orderinfo` WHERE `orderinfo`.`user` = \\? AND `orderinfo`.`deleted_at` IS NULL").WithArgs(41).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("SELECT \\* FROM `orderinfo` WHERE `orderinfo`.`user` = \\? AND `orderinfo`.`deleted_at` IS NULL ORDER BY id DESC LIMIT \\? OFFSET \\?").WithArgs(41, 2, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user", "order_sn", "status", "total_cents", "pay_type", "add_time"}).
			AddRow(7, 41, "order-7", "TRADE_SUCCESS", 4490, "alipay", time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)))
	rsp, err := (&OrderServer{}).OrderList(context.Background(), &proto.OrderFilterRequest{UserId: 41, Pages: 2, PagePerNums: 2})
	if err != nil || rsp.Total != 3 || len(rsp.Data) != 1 || rsp.Data[0].Id != 7 || rsp.Data[0].TotalCents != 4490 || rsp.Data[0].AddTime != "2026-09-29T01:02:03Z" {
		t.Fatalf("unexpected page: %v %v", rsp, err)
	}
}

func TestOrderListReadFailuresAndDeadlines(t *testing.T) {
	for _, stage := range []string{"count", "page"} {
		for _, timeout := range []bool{false, true} {
			t.Run(stage+"/"+map[bool]string{false: "failure", true: "deadline"}[timeout], func(t *testing.T) {
				mock := cartMock(t)
				query := mock.ExpectQuery("SELECT count\\(\\*\\) FROM `orderinfo`").WithArgs(41)
				if stage == "page" {
					query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
					query = mock.ExpectQuery("SELECT \\* FROM `orderinfo`").WithArgs(41, 10)
				}
				ctx := context.Background()
				want := codes.Unavailable
				if timeout {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
					defer cancel()
					query.WillDelayFor(time.Second).WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = codes.DeadlineExceeded
				} else {
					query.WillReturnError(errors.New("private database error"))
				}
				start := time.Now()
				rsp, err := (&OrderServer{}).OrderList(ctx, &proto.OrderFilterRequest{UserId: 41})
				if rsp != nil || status.Code(err) != want || time.Since(start) > 500*time.Millisecond {
					t.Fatalf("read failure became a page or ignored deadline: %v %v", rsp, err)
				}
			})
		}
	}
}

func TestOrderListInvalidSelectorsAndLegacyAdminScope(t *testing.T) {
	mock := cartMock(t)
	for _, req := range []*proto.OrderFilterRequest{nil, {UserId: -1}, {Pages: -1}, {PagePerNums: -1}, {Pages: 2147483647, PagePerNums: 100}} {
		if _, err := (&OrderServer{}).OrderList(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid selector accepted: %v %v", req, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&OrderServer{}).OrderList(ctx, &proto.OrderFilterRequest{}); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	// Existing administrative callers may omit the user. Defaults remain 1/10.
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `orderinfo` WHERE `orderinfo`.`deleted_at` IS NULL").WithArgs().
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT \\* FROM `orderinfo` WHERE `orderinfo`.`deleted_at` IS NULL ORDER BY id DESC LIMIT \\?").WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	rsp, err := (&OrderServer{}).OrderList(context.Background(), &proto.OrderFilterRequest{})
	if err != nil || rsp.Total != 0 || len(rsp.Data) != 0 {
		t.Fatalf("legacy empty page: %v %v", rsp, err)
	}
}
