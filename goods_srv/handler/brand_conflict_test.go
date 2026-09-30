package handler_test

import (
	"context"
	"fmt"
	"testing"

	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBrandNameConflictsHaveOneSafeResult(t *testing.T) {
	for _, stage := range []string{"precheck", "insert", "update"} {
		t.Run(stage, func(t *testing.T) {
			mock := catalogDatabase(t)
			rows := sqlmock.NewRows([]string{"id", "name"})
			if stage != "insert" {
				rows.AddRow(7, "Existing")
			}
			mock.ExpectQuery("SELECT").WillReturnRows(rows)
			if stage != "precheck" {
				mock.ExpectBegin()
				write := "INSERT"
				if stage == "update" {
					write = "UPDATE"
				}
				mock.ExpectExec(write).WillReturnError(fmt.Errorf("wrapped: %w", &mysql.MySQLError{
					Number: 1062, Message: "private duplicate value and index name",
				}))
				mock.ExpectRollback()
			}
			server := &handler.GoodsServer{}
			var err error
			if stage == "update" {
				_, err = server.UpdateBrand(context.Background(), &proto.BrandRequest{Id: 7, Name: "Taken"})
			} else {
				_, err = server.CreateBrand(context.Background(), &proto.BrandRequest{Name: "Taken"})
			}
			if status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != "品牌名称已存在" {
				t.Fatalf("name conflict must be recognizable without SQL details: %v", err)
			}
		})
	}
}
