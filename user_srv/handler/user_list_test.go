package handler

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/user_srv/model/proto"
)

func TestUserListPageFailureDoesNotReturnUsers(t *testing.T) {
	mock := profileMockDB(t)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `user`").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT \\* FROM `user`.*LIMIT \\? OFFSET \\?").WithArgs(1, 1).WillReturnError(errors.New("private database failure"))
	rsp, err := (&UserServer{}).GetUserList(context.Background(), &proto.PageInfo{Pn: 2, PSize: 1})
	if rsp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("failed page returned users: response=%v code=%s", rsp, status.Code(err))
	}
}

func TestUserListCountAndStablePagination(t *testing.T) {
	for _, tc := range []struct {
		name         string
		req          *proto.PageInfo
		size, offset int
		total        int64
	}{
		{"second page", &proto.PageInfo{Pn: 2, PSize: 1}, 1, 1, 2},
		{"defaults", &proto.PageInfo{}, 10, 0, 0},
		{"capped size", &proto.PageInfo{PSize: math.MaxUint32}, 100, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := profileMockDB(t)
			mock.ExpectQuery("SELECT count\\(\\*\\) FROM `user` WHERE `user`.`deleted_at` IS NULL").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.total))
			rows := sqlmock.NewRows([]string{"id", "mobile", "password"})
			query := mock.ExpectQuery("SELECT \\* FROM `user` WHERE `user`.`deleted_at` IS NULL ORDER BY id ASC LIMIT \\?")
			if tc.offset > 0 {
				query.WithArgs(tc.size, tc.offset)
				rows.AddRow(42, "13000000042", "internal-hash")
			} else {
				query.WithArgs(tc.size)
			}
			query.WillReturnRows(rows)
			rsp, err := (&UserServer{}).GetUserList(context.Background(), tc.req)
			if err != nil || rsp.Total != int32(tc.total) {
				t.Fatalf("invalid list %v %v", rsp, err)
			}
			if tc.offset > 0 {
				if len(rsp.Data) != 1 || rsp.Data[0].Id != 42 || rsp.Data[0].Password != "internal-hash" {
					t.Fatal("page mapping changed", rsp)
				}
			} else if len(rsp.Data) != 0 {
				t.Fatal("empty list changed", rsp)
			}
		})
	}
}

func TestUserListFailuresAndCancellation(t *testing.T) {
	for _, stage := range []string{"count", "page"} {
		for _, timeout := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: " failure", true: " deadline"}[timeout], func(t *testing.T) {
				mock := profileMockDB(t)
				query := mock.ExpectQuery("SELECT count\\(\\*\\) FROM `user`")
				if stage == "page" {
					query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
					query = mock.ExpectQuery("SELECT \\* FROM `user`.*ORDER BY id ASC LIMIT \\?").WithArgs(10)
				}
				ctx := context.Background()
				want := codes.Unavailable
				if timeout {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
					defer cancel()
					query.WillDelayFor(time.Second).WillReturnRows(sqlmock.NewRows([]string{"count"}))
					want = codes.DeadlineExceeded
				} else {
					query.WillReturnError(errors.New("private SQL detail"))
				}
				start := time.Now()
				rsp, err := (&UserServer{}).GetUserList(ctx, &proto.PageInfo{})
				if rsp != nil || status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") || time.Since(start) > 500*time.Millisecond {
					t.Fatalf("failed read ignored error/timeout: %v %v", rsp, err)
				}
			})
		}
	}
	t.Run("invalid and canceled before SQL", func(t *testing.T) {
		profileMockDB(t)
		for _, req := range []*proto.PageInfo{nil, {Pn: math.MaxUint32, PSize: 1}} {
			if _, err := (&UserServer{}).GetUserList(context.Background(), req); status.Code(err) != codes.InvalidArgument {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (&UserServer{}).GetUserList(ctx, &proto.PageInfo{}); status.Code(err) != codes.Canceled {
			t.Fatal(err)
		}
	})
	t.Run("count overflow", func(t *testing.T) {
		mock := profileMockDB(t)
		mock.ExpectQuery("SELECT count\\(\\*\\) FROM `user`").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(math.MaxInt32) + 1))
		if rsp, err := (&UserServer{}).GetUserList(context.Background(), &proto.PageInfo{}); rsp != nil || status.Code(err) != codes.OutOfRange {
			t.Fatalf("overflow accepted %v %v", rsp, err)
		}
	})
}
