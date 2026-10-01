package handler_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
)

func TestCategoryHierarchyChecksPropagateReadFailures(t *testing.T) {
	for _, stage := range []string{"parent", "children"} {
		t.Run(stage, func(t *testing.T) {
			mock := catalogDatabase(t)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT.*FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"id", "level", "parent_category_id"}).AddRow(7, 1, nil))
			parentQuery := mock.ExpectQuery("SELECT.*FOR SHARE")
			failure := errors.New("private hierarchy SQL failure")
			if stage == "parent" {
				parentQuery.WillReturnError(failure)
			} else {
				parentQuery.WillReturnRows(sqlmock.NewRows([]string{"id", "level"}).AddRow(8, 1))
				mock.ExpectQuery("SELECT count").WillReturnError(failure)
			}
			mock.ExpectRollback()
			_, err := (&handler.GoodsServer{}).UpdateCategory(context.Background(), &proto.CategoryInfoRequest{Id: 7, ParentCategory: 8, Level: 2})
			if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("got %v, want safe Internal", err)
			}
		})
	}
}
