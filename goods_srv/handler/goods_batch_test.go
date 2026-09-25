package handler_test

import (
	"context"
	"errors"
	"regexp"
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

func batchGoodsMockDB(t *testing.T) sqlmock.Sqlmock {
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
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mock
}

func TestBatchGetGoodsDoesNotQueryAllProductsForEmptyIDs(t *testing.T) {
	batchGoodsMockDB(t)
	response, err := (&handler.GoodsServer{}).BatchGetGoods(context.Background(), &proto.BatchGoodsIdInfo{})
	if err != nil || response.Total != 0 || response.Data == nil || len(response.Data) != 0 {
		t.Fatalf("empty request must return an empty response without SQL: %v / %v", response, err)
	}
}

func TestBatchGetGoodsRejectsInvalidRequests(t *testing.T) {
	batchGoodsMockDB(t)
	for _, request := range []*proto.BatchGoodsIdInfo{nil, {Id: []int32{0}}, {Id: []int32{421, -1}}, {Id: make([]int32, 1001)}} {
		response, err := (&handler.GoodsServer{}).BatchGetGoods(context.Background(), request)
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid IDs must fail before querying: %v / %v", response, err)
		}
	}
}

func TestBatchGetGoodsScopesIDsAndReturnsOnlyExistingProducts(t *testing.T) {
	mock := batchGoodsMockDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `goods` WHERE id IN (?,?) AND `goods`.`deleted_at` IS NULL")).WithArgs(421, 422).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "shop_price", "on_sale", "goods_front_image", "fav_num"}).AddRow(421, "测试商品", 19.5, false, "https://example.test/product.png", 9))
	response, err := (&handler.GoodsServer{}).BatchGetGoods(context.Background(), &proto.BatchGoodsIdInfo{Id: []int32{421, 422}})
	if err != nil || response.Total != 1 || len(response.Data) != 1 {
		t.Fatalf("unexpected response: %v / %v", response, err)
	}
	goods := response.Data[0]
	if goods.Id != 421 || goods.Name != "测试商品" || goods.ShopPrice != 19.5 || goods.OnSale || goods.FavNum != 9 || goods.GoodsFrontImage != "https://example.test/product.png" {
		t.Fatal("batch must preserve product fields, including off-sale goods")
	}
}

func TestBatchGetGoodsDatabaseFailureIsNotAnEmptySuccess(t *testing.T) {
	mock := batchGoodsMockDB(t)
	mock.ExpectQuery("SELECT .* FROM `goods`").WithArgs(421).WillReturnError(errors.New("private database detail"))
	response, err := (&handler.GoodsServer{}).BatchGetGoods(context.Background(), &proto.BatchGoodsIdInfo{Id: []int32{421}})
	if response != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "批量查询商品失败" {
		t.Fatalf("database failure must be a safe Internal error: %v / %v", response, err)
	}
}

func TestBatchGetGoodsHonorsCallerCancellation(t *testing.T) {
	batchGoodsMockDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := (&handler.GoodsServer{}).BatchGetGoods(ctx, &proto.BatchGoodsIdInfo{Id: []int32{421}})
	if response != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled request must not query the database: %v / %v", response, err)
	}
}
