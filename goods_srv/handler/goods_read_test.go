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
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestGoodsDetailDatabaseErrors(t *testing.T) {
	for _, stage := range []string{"goods", "brands", "category"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
				NamingStrategy: schema.NamingStrategy{SingularTable: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			previous := global.DB
			global.DB = orm
			t.Cleanup(func() { global.DB = previous })
			failure := errors.New("database failure injected at " + stage)
			if stage == "goods" {
				mock.ExpectQuery("SELECT .* FROM `goods`").WillReturnError(failure)
			} else {
				mock.ExpectQuery("SELECT .* FROM `goods`").WillReturnRows(sqlmock.NewRows([]string{"id", "category_id", "brands_id"}).AddRow(421, 1, 1))
				if stage == "brands" {
					mock.ExpectQuery("SELECT .* FROM `brands`").WillReturnError(failure)
				} else {
					mock.ExpectQuery("SELECT .* FROM `brands`").WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "Test brand"))
					mock.ExpectQuery("SELECT .* FROM `category`").WillReturnError(failure)
				}
			}
			got, err := (&handler.GoodsServer{}).GetGoodsDetail(context.Background(), &proto.GoodInfoRequest{Id: 421})
			if got != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "查询商品详情失败" {
				t.Fatalf("database failure must return a generic Internal error without a partial product, got %v, %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
