package handler

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func profileMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previous := global.DB
	t.Cleanup(func() {
		global.DB = previous
		_ = db.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	global.DB, err = gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true,
		NamingStrategy:       schema.NamingStrategy{SingularTable: true},
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mock
}

func TestPatchUserDatabaseOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		queryErr  error
		missing   bool
		updateErr error
		want      codes.Code
	}{
		{name: "unchanged value succeeds with zero affected rows", want: codes.OK},
		{name: "missing user", missing: true, want: codes.NotFound},
		{name: "read error is not mistaken for missing user", queryErr: errors.New("private database detail"), want: codes.Internal},
		{name: "write error rolls back", updateErr: errors.New("private database detail"), want: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := profileMockDB(t)
			mock.ExpectBegin()
			query := mock.ExpectQuery("SELECT `id` FROM `user`.*FOR UPDATE").WithArgs(41, 1)
			if tc.queryErr != nil {
				query.WillReturnError(tc.queryErr)
			} else {
				rows := sqlmock.NewRows([]string{"id"})
				if !tc.missing {
					rows.AddRow(41)
				}
				query.WillReturnRows(rows)
			}
			if tc.queryErr == nil && !tc.missing {
				// An exact column list catches reintroducing Save or another field.
				update := mock.ExpectExec(regexp.QuoteMeta("UPDATE `user` SET `nick_name`=?,`update_time`=? WHERE id = ? AND `user`.`deleted_at` IS NULL")).WithArgs("小李", sqlmock.AnyArg(), 41)
				if tc.updateErr != nil {
					update.WillReturnError(tc.updateErr)
				} else {
					update.WillReturnResult(sqlmock.NewResult(0, 0))
				}
			}
			if tc.want == codes.OK {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			nickname := " 小李 "
			_, err := (&UserServer{}).PatchUser(context.Background(), &proto.PatchUserInfo{Id: 41, NickName: &nickname})
			if status.Code(err) != tc.want {
				t.Fatalf("code=%s want=%s", status.Code(err), tc.want)
			}
			if tc.want == codes.Internal && status.Convert(err).Message() != "用户资料操作失败" {
				t.Fatal("database error details leaked")
			}
		})
	}
}

func TestProfileContextAndReadError(t *testing.T) {
	mock := profileMockDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nickname := "小李"
	_, err := (&UserServer{}).PatchUser(ctx, &proto.PatchUserInfo{Id: 41, NickName: &nickname})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled request code=%s", status.Code(err))
	}
	mock.ExpectQuery("SELECT .* FROM `user`").WithArgs(41, 1).WillReturnError(errors.New("private database detail"))
	_, err = (&UserServer{}).GetUserById(context.Background(), &proto.IdRequest{Id: 41})
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "用户资料操作失败" {
		t.Fatal("profile read must distinguish a database failure from a missing user without exposing details")
	}
}

func TestProfileBirthdayRetainsCalendarDateAcrossServerTimezones(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	for _, offset := range []int{-12 * 60 * 60, 14 * 60 * 60} {
		time.Local = time.FixedZone("test-server", offset)
		for _, date := range []string{"1960-07-08", "1970-01-01", "1998-05-16"} {
			birthday, err := parseProfileBirthday(date)
			if err != nil {
				t.Fatal(err)
			}
			// A DATETIME calendar date can be decoded in any DSN location.
			stored := time.Date(birthday.Year(), birthday.Month(), birthday.Day(), 0, 0, 0, 0, time.Local)
			response := ModelToResponse(model.User{Birthday: &stored})
			if response.GetBirthdayDate() != date {
				t.Fatalf("calendar date changed at offset %d", offset)
			}
		}
	}
}
