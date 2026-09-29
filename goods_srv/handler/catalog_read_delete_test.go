package handler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBrandListChecksEveryReadAndCancellation(t *testing.T) {
	for _, outcome := range []string{"count", "page", "canceled", "deadline", "empty", "page data"} {
		t.Run(outcome, func(t *testing.T) {
			mock := catalogDatabase(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := codes.Internal
			switch outcome {
			case "canceled":
				cancel()
				want = codes.Canceled
			case "deadline":
				var done context.CancelFunc
				ctx, done = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer done()
				want = codes.DeadlineExceeded
			case "count":
				mock.ExpectQuery("SELECT count").WillReturnError(errors.New("private SQL count failure"))
			default:
				mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(30))
				query := mock.ExpectQuery("SELECT .*FROM `brands`.*ORDER BY id.*LIMIT \\? OFFSET \\?").WithArgs(10, 10)
				if outcome == "page" {
					query.WillReturnError(errors.New("private SQL page failure"))
				} else {
					rows := sqlmock.NewRows([]string{"id", "name", "logo"})
					if outcome == "page data" {
						rows.AddRow(11, "Example", "image")
					}
					query.WillReturnRows(rows)
					want = codes.OK
				}
			}
			rsp, err := (&handler.GoodsServer{}).BrandList(ctx, &proto.BrandFilterRequest{Pages: 2, PagePerNums: 10})
			if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") {
				t.Fatalf("got %v, want %v", err, want)
			}
			if want == codes.OK && (rsp.Total != 30 || (outcome == "empty" && len(rsp.Data) != 0) || (outcome == "page data" && (len(rsp.Data) != 1 || rsp.Data[0].Id != 11))) {
				t.Fatalf("bad pagination result: %v", rsp)
			}
		})
	}
}

func TestSubCategoryChecksEveryReadAndCancellation(t *testing.T) {
	for _, outcome := range []string{"lookup", "children", "association", "missing", "canceled", "deadline", "empty"} {
		t.Run(outcome, func(t *testing.T) {
			mock := catalogDatabase(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := codes.Internal
			switch outcome {
			case "canceled":
				cancel()
				want = codes.Canceled
			case "deadline":
				var done context.CancelFunc
				ctx, done = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer done()
				want = codes.DeadlineExceeded
			case "lookup":
				mock.ExpectQuery("SELECT").WillReturnError(errors.New("private SQL lookup failure"))
			case "missing":
				mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
				want = codes.NotFound
			default:
				mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "level"}).AddRow(7, "Parent", 2))
				if outcome == "children" {
					mock.ExpectQuery("SELECT").WillReturnError(errors.New("private SQL children failure"))
				} else {
					rows := sqlmock.NewRows([]string{"id", "name", "level", "parent_category_id"})
					if outcome == "association" {
						rows.AddRow(8, "Child", 3, 7)
					}
					mock.ExpectQuery("SELECT").WillReturnRows(rows)
					if outcome == "association" {
						mock.ExpectQuery("SELECT").WillReturnError(errors.New("private SQL preload failure"))
					} else {
						want = codes.OK
					}
				}
			}
			rsp, err := (&handler.GoodsServer{}).GetSubCategory(ctx, &proto.CategoryListRequest{Id: 7})
			if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") {
				t.Fatalf("got %v, want %v", err, want)
			}
			if want == codes.OK && (rsp.Info.Id != 7 || len(rsp.SubCategorys) != 0) {
				t.Fatalf("bad empty category result: %v", rsp)
			}
		})
	}
}

func TestCatalogDeletesCheckDatabaseOutcome(t *testing.T) {
	for _, entity := range []string{"brand", "banner", "category"} {
		for _, outcome := range []string{"begin", "write", "commit", "missing", "success", "canceled", "deadline"} {
			t.Run(entity+"/"+outcome, func(t *testing.T) {
				mock := catalogDatabase(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("private SQL failure")
				want := codes.Internal
				switch outcome {
				case "canceled":
					cancel()
					want = codes.Canceled
				case "deadline":
					var done context.CancelFunc
					ctx, done = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer done()
					want = codes.DeadlineExceeded
				case "begin":
					mock.ExpectBegin().WillReturnError(failure)
				default:
					mock.ExpectBegin()
					if outcome == "write" {
						mock.ExpectExec("UPDATE").WillReturnError(failure)
						mock.ExpectRollback()
					} else {
						affected := int64(1)
						if outcome == "missing" {
							affected = 0
							want = codes.NotFound
						} else if outcome == "success" {
							want = codes.OK
						}
						mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, affected))
						if outcome == "commit" {
							mock.ExpectCommit().WillReturnError(failure)
						} else {
							mock.ExpectCommit()
						}
					}
				}
				var err error
				server := &handler.GoodsServer{}
				switch entity {
				case "brand":
					_, err = server.DeleteBrand(ctx, &proto.BrandRequest{Id: 7})
				case "banner":
					_, err = server.DeleteBanner(ctx, &proto.BannerRequest{Id: 7})
				case "category":
					_, err = server.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: 7})
				}
				if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
	}
}

func TestCatalogCreatesKeepContextAndCheckErrors(t *testing.T) {
	for _, entity := range []string{"brand", "banner"} {
		for _, outcome := range []string{"canceled", "lookup", "exists", "write", "commit", "success"} {
			t.Run(entity+"/"+outcome, func(t *testing.T) {
				mock := catalogDatabase(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("private SQL failure")
				want := codes.Internal
				switch outcome {
				case "canceled":
					cancel()
					want = codes.Canceled
				case "lookup":
					mock.ExpectQuery("SELECT").WillReturnError(failure)
				case "exists":
					mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
					want = codes.InvalidArgument
				default:
					if entity == "brand" {
						mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
					}
					mock.ExpectBegin()
					if outcome == "write" {
						mock.ExpectExec("INSERT").WillReturnError(failure)
						mock.ExpectRollback()
					} else {
						mock.ExpectExec("INSERT").WillReturnResult(sqlmock.NewResult(7, 1))
						if outcome == "commit" {
							mock.ExpectCommit().WillReturnError(failure)
						} else {
							mock.ExpectCommit()
							want = codes.OK
						}
					}
				}
				var err error
				var id int32
				server := &handler.GoodsServer{}
				if entity == "brand" {
					rsp, failure := server.CreateBrand(ctx, &proto.BrandRequest{Name: "Example"})
					err = failure
					if rsp != nil {
						id = rsp.Id
					}
				} else {
					request := &proto.BannerRequest{}
					if outcome == "lookup" || outcome == "exists" {
						request.Id = 7
					}
					rsp, failure := server.CreateBanner(ctx, request)
					err = failure
					if rsp != nil {
						id = rsp.Id
					}
				}
				if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private SQL") || (want == codes.OK && id != 7) {
					t.Fatalf("id=%d err=%v, want %v", id, err, want)
				}
			})
		}
	}
}
