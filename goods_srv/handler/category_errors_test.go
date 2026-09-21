package handler_test

import (
	"context"
	"errors"
	"strings"
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

func TestCategoryDatabaseFailuresAreNotReportedAsSuccessOrMissing(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("private SQL failure must not reach the client")
	for _, operation := range []string{"category insert", "category commit", "relation lookup", "relation insert", "relation update", "relation delete", "relation list"} {
		t.Run(operation, func(t *testing.T) {
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
			server := &handler.GoodsServer{}
			lookup := func() { mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1)) }
			switch operation {
			case "category insert", "category commit":
				mock.ExpectBegin()
				if operation == "category insert" {
					mock.ExpectExec("INSERT").WillReturnError(failure)
					mock.ExpectRollback()
				} else {
					mock.ExpectExec("INSERT").WillReturnResult(sqlmock.NewResult(2, 1))
					mock.ExpectCommit().WillReturnError(failure)
				}
				_, err = server.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Test category", Level: 1})
			case "relation lookup":
				mock.ExpectQuery("SELECT").WillReturnError(failure)
				_, err = server.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: 1, BrandId: 1})
			case "relation insert":
				lookup()
				lookup()
				mock.ExpectBegin()
				mock.ExpectExec("INSERT").WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: 1, BrandId: 1})
			case "relation update":
				mock.ExpectBegin()
				lookup()
				lookup()
				lookup()
				mock.ExpectExec("UPDATE").WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.UpdateCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: 1, CategoryId: 1, BrandId: 1})
			case "relation delete":
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE").WillReturnError(failure) // Existing soft-delete policy.
				mock.ExpectRollback()
				_, err = server.DeleteCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: 1})
			case "relation list":
				mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("SELECT").WillReturnError(failure)
				_, err = server.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
			}
			if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "private SQL") {
				t.Errorf("database failure must be a generic Internal error, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
