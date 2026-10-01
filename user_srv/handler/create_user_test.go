package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
)

func TestCreateUserStopsAfterFailedLookup(t *testing.T) {
	for _, outcome := range []string{"database failure", "canceled", "expired", "query deadline", "exists"} {
		t.Run(outcome, func(t *testing.T) {
			mock := profileMockDB(t)
			writes := 0
			if err := global.DB.Callback().Create().Before("gorm:begin_transaction").Register("test:reject-unexpected-create", func(tx *gorm.DB) {
				writes++
				tx.AddError(errors.New("unexpected create after lookup failure"))
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := codes.Internal
			switch outcome {
			case "canceled":
				cancel()
				want = codes.Canceled
			case "expired":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				want = codes.DeadlineExceeded
			default:
				query := mock.ExpectQuery("SELECT .* FROM `user`").WithArgs("13000000003", 1)
				switch outcome {
				case "database failure":
					query.WillReturnError(errors.New("private SQL detail"))
				case "exists":
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
					want = codes.AlreadyExists
				case "query deadline":
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
					defer stop()
					query.WillDelayFor(300 * time.Millisecond).WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = codes.DeadlineExceeded
				}
			}
			got, err := (&UserServer{}).CreateUser(ctx, &proto.CreateUserInfo{Mobile: "13000000003", NickName: "New user", PassWord: "test-only"})
			if status.Code(err) != want || writes != 0 || got != nil || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("got code=%s writes=%d response=%v, want %s without writes", status.Code(err), writes, got, want)
			}
		})
	}
}

func TestCreateUserClassifiesWriteFailures(t *testing.T) {
	for _, outcome := range []string{"duplicate", "write failure", "commit failure"} {
		t.Run(outcome, func(t *testing.T) {
			mock := profileMockDB(t)
			mock.ExpectQuery("SELECT .* FROM `user`").WithArgs("13000000003", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectBegin()
			write := mock.ExpectExec("INSERT INTO `user`")
			want := codes.Internal
			switch outcome {
			case "duplicate":
				write.WillReturnError(fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1062, Message: "private mobile already exists"}))
				mock.ExpectRollback()
				want = codes.AlreadyExists
			case "write failure":
				write.WillReturnError(errors.New("private SQL detail"))
				mock.ExpectRollback()
			case "commit failure":
				write.WillReturnResult(sqlmock.NewResult(42, 1))
				mock.ExpectCommit().WillReturnError(errors.New("private commit failure"))
			}
			got, err := (&UserServer{}).CreateUser(context.Background(), &proto.CreateUserInfo{Mobile: "13000000003", NickName: "New user", PassWord: "test-only"})
			if status.Code(err) != want || got != nil || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("got %v response=%v, want safe %s", err, got, want)
			}
		})
	}
}

func TestCreateUserCancellationBeforeInsert(t *testing.T) {
	mock := profileMockDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock.ExpectQuery("SELECT .* FROM `user`").WithArgs("13000000003", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	// Cancel after hashing, just before GORM starts the INSERT transaction.
	if err := global.DB.Callback().Create().Before("gorm:begin_transaction").Register("test:cancel-create", func(tx *gorm.DB) {
		if tx.Statement.Context != ctx {
			t.Error("INSERT lost the caller's context")
		}
		cancel()
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (&UserServer{}).CreateUser(ctx, &proto.CreateUserInfo{Mobile: "13000000003", PassWord: "test-only"})
	if status.Code(err) != codes.Canceled || got != nil {
		t.Fatalf("got %v response=%v, want canceled without SQL writes", err, got)
	}
}
