package handler

import (
	"context"
	"errors"
	"testing"

	"grpctest/order_srv/global"
	"grpctest/order_srv/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func cartMock(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := global.DB
	global.DB = orm
	t.Cleanup(func() {
		global.DB = previous
		_ = db.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return mock
}

func cartRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "user", "goods", "nums", "checked"}).AddRow(7, 41, 421, 2, true)
}

func TestCartDatabaseFailuresAreReported(t *testing.T) {
	broken := errors.New("injected database failure")
	for _, method := range []string{"list", "create", "update", "delete"} {
		stages := []string{"write", "commit"}
		if method == "list" {
			stages = []string{"read"}
		}
		if method == "create" || method == "update" {
			stages = append(stages, "read")
		}
		for _, stage := range stages {
			t.Run(method+"/"+stage, func(t *testing.T) {
				mock := cartMock(t)
				if method != "list" {
					mock.ExpectBegin()
				}
				if method != "delete" {
					query := mock.ExpectQuery("SELECT .* FROM `shoppingcart`")
					if stage == "read" {
						query.WillReturnError(broken)
					} else {
						query.WillReturnRows(cartRows())
					}
				}
				if stage != "read" {
					write := mock.ExpectExec("UPDATE `shoppingcart`")
					if stage == "write" {
						write.WillReturnError(broken)
					} else {
						write.WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
				if method != "list" {
					if stage == "commit" {
						mock.ExpectCommit().WillReturnError(broken)
					} else {
						mock.ExpectRollback()
					}
				}
				server, ctx := &OrderServer{}, context.Background()
				req := &proto.CartItemRequest{Id: 7, UserId: 41, GoodsId: 421, Nums: pb.Int32(3)}
				var err error
				switch method {
				case "list":
					_, err = server.CartItemList(ctx, &proto.UserInfo{Id: 41})
				case "create":
					_, err = server.CreateCarItem(ctx, req)
				case "update":
					_, err = server.UpdateCartItem(ctx, req)
				case "delete":
					_, err = server.DeleteCartItem(ctx, req)
				}
				if status.Code(err) != codes.Internal {
					t.Fatalf("got %v; want Internal", err)
				}
			})
		}
	}
	t.Run("insert", func(t *testing.T) {
		mock := cartMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .* FROM `shoppingcart`").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectExec("INSERT INTO `shoppingcart`").WillReturnError(broken)
		mock.ExpectRollback()
		_, err := (&OrderServer{}).CreateCarItem(context.Background(), &proto.CartItemRequest{UserId: 41, GoodsId: 421, Nums: pb.Int32(1)})
		if status.Code(err) != codes.Internal {
			t.Fatalf("got %v; want Internal", err)
		}
	})
}

func TestCartRejectsInvalidIdentityAndQuantityBeforeDatabase(t *testing.T) {
	cartMock(t) // No SQL is permitted for invalid input.
	server, ctx := &OrderServer{}, context.Background()
	for _, id := range []int32{0, -1} {
		if _, err := server.CartItemList(ctx, &proto.UserInfo{Id: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	for _, req := range []*proto.CartItemRequest{
		nil, {}, {UserId: 41, GoodsId: 421}, {UserId: 41, GoodsId: 421, Nums: pb.Int32(0)},
		{UserId: 41, GoodsId: 421, Nums: pb.Int32(-1)}, {UserId: 41, GoodsId: -1, Nums: pb.Int32(1)},
		{UserId: 0, GoodsId: 421, Nums: pb.Int32(1)},
	} {
		if _, err := server.CreateCarItem(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("create %v: %v", req, err)
		}
	}
	for _, req := range []*proto.CartItemRequest{
		nil, {}, {Id: 7, UserId: 41}, {Id: 7, UserId: 41, Nums: pb.Int32(0)}, {Id: 7, UserId: 41, Nums: pb.Int32(-1)},
		{UserId: 41, GoodsId: 421, Nums: pb.Int32(1)}, {Id: 7, UserId: 0, Nums: pb.Int32(1)},
	} {
		if _, err := server.UpdateCartItem(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("update %v: %v", req, err)
		}
	}
	for _, req := range []*proto.CartItemRequest{nil, {}, {UserId: 41, GoodsId: 421}, {Id: 7}, {Id: -1, UserId: 41}} {
		if _, err := server.DeleteCartItem(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("delete %v: %v", req, err)
		}
	}
}
