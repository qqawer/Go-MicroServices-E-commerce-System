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
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func catalogDatabase(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	previous := global.DB
	global.DB = orm
	t.Cleanup(func() {
		global.DB = previous
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return mock
}

func TestCatalogUpdatesReportDatabaseFailures(t *testing.T) {
	server := &handler.GoodsServer{}
	operations := map[string]func(context.Context) error{
		"category": func(ctx context.Context) error {
			_, err := server.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: 7, Name: "Edited", IsTab: false})
			return err
		},
		"brand": func(ctx context.Context) error {
			_, err := server.UpdateBrand(ctx, &proto.BrandRequest{Id: 7, Name: "Edited", Logo: "https://example.invalid/new.png"})
			return err
		},
		"banner": func(ctx context.Context) error {
			_, err := server.UpdateBanner(ctx, &proto.BannerRequest{Id: 7, Index: 2, Image: "https://example.invalid/new.png"})
			return err
		},
	}
	for name, call := range operations {
		for _, stage := range []string{"lookup", "write", "commit", "missing", "canceled"} {
			t.Run(name+"/"+stage, func(t *testing.T) {
				mock := catalogDatabase(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("private SQL failure")
				want := codes.Internal
				switch stage {
				case "lookup":
					mock.ExpectQuery("SELECT").WillReturnError(failure)
				case "missing":
					mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = codes.NotFound
				case "canceled":
					cancel()
					want = codes.Canceled
				default:
					mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "logo", "is_tab"}).AddRow(7, "Old", "https://example.invalid/old.png", true))
					mock.ExpectBegin()
					if stage == "write" {
						mock.ExpectExec("UPDATE").WillReturnError(failure)
						mock.ExpectRollback()
					} else {
						mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectCommit().WillReturnError(failure)
					}
				}
				if err := call(ctx); status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") {
					t.Fatalf("got %v, want generic %v", err, want)
				}
			})
		}
	}
}

func TestCatalogListsDoNotHideReadFailures(t *testing.T) {
	server := &handler.GoodsServer{}
	for name, call := range map[string]func(context.Context) error{
		"category": func(ctx context.Context) error {
			_, err := server.GetAllCategorysList(ctx, &emptypb.Empty{})
			return err
		},
		"banner": func(ctx context.Context) error {
			_, err := server.BannerList(ctx, &emptypb.Empty{})
			return err
		},
	} {
		for _, outcome := range []string{"failure", "empty", "canceled"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				mock := catalogDatabase(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := codes.Internal
				switch outcome {
				case "failure":
					mock.ExpectQuery("SELECT").WillReturnError(errors.New("private SQL failure"))
				case "empty":
					mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = codes.OK
				case "canceled":
					cancel()
					want = codes.Canceled
				}
				if err := call(ctx); status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") {
					t.Fatalf("got %v, want generic %v", err, want)
				}
			})
		}
	}
}
