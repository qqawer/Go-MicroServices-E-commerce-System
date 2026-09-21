package handler_test

import (
	"context"
	"errors"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGoodsStatusDatabaseErrors(t *testing.T) {
	for _, stage := range []string{"lookup", "write", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
			})
			if err != nil {
				t.Fatal(err)
			}
			previous := global.DB
			global.DB = orm
			t.Cleanup(func() { global.DB = previous })
			failure := errors.New("database failure injected at " + stage)
			mock.ExpectBegin()
			if stage == "lookup" {
				mock.ExpectQuery("SELECT").WillReturnError(failure)
				mock.ExpectRollback()
			} else {
				mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(421))
				if stage == "write" {
					mock.ExpectExec("UPDATE").WillReturnError(failure)
					mock.ExpectRollback()
				} else {
					mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit().WillReturnError(failure)
				}
			}
			_, err = (&handler.GoodsServer{}).UpdateGoodsStatus(context.Background(), &proto.UpdateGoodsStatusRequest{
				Id: 421, IsHot: gproto.Bool(false),
			})
			if status.Code(err) != codes.Internal || status.Convert(err).Message() != "更新商品失败" {
				t.Fatalf("database failure must return a generic Internal error, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
