package handler_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func expectGoodsListRelations(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT .* FROM `brands`").WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "logo"}).AddRow(1, "Test brand", "https://example.invalid/brand.png"))
	mock.ExpectQuery("SELECT .* FROM `category`").WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "Test category"))
}

func TestGoodsListStoreVisibilityFiltersCountAndPage(t *testing.T) {
	mock := batchGoodsMockDB(t)
	where := " WHERE (on_sale = ? AND inventory_ready = ?) AND name LIKE ? AND shop_price>=? AND shop_price<=? AND brands_id=? AND `goods`.`deleted_at` IS NULL"
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT count(*) FROM `goods`"+where)+"$").
		WithArgs(true, true, "%test%", 10, 30, 1).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT * FROM `goods`"+where+" LIMIT ? OFFSET ?")+"$").
		WithArgs(true, true, "%test%", 10, 30, 1, 2, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "shop_price", "category_id", "brands_id", "on_sale", "inventory_ready"}).
			AddRow(425, "test visible one", 19.5, 1, 1, true, true).
			AddRow(428, "test visible two", 25, 1, 1, true, true))
	expectGoodsListRelations(mock)
	response, err := (&handler.GoodsServer{}).GoodsList(context.Background(), &proto.GoodsFilterRequest{
		StoreVisibleOnly: true, KeyWords: "test", PriceMin: 10, PriceMax: 30, Brand: 1, Pages: 2, PagePerNums: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 5 || len(response.Data) != 2 {
		t.Fatalf("total must count visible matches before pagination: %v", response)
	}
	for _, goods := range response.Data {
		if !goods.OnSale || !goods.InventoryReady || goods.Category.GetId() != 1 || goods.Category.GetName() != "Test category" ||
			goods.Brand.GetId() != 1 || goods.Brand.GetName() != "Test brand" || goods.Brand.GetLogo() != "https://example.invalid/brand.png" {
			t.Fatalf("visible goods must retain their loaded associations: %v", goods)
		}
	}
}

func TestGoodsListDefaultKeepsAllSaleAndReadinessStates(t *testing.T) {
	mock := batchGoodsMockDB(t)
	mock.ExpectQuery("^" + regexp.QuoteMeta("SELECT count(*) FROM `goods` WHERE `goods`.`deleted_at` IS NULL") + "$").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery("^" + regexp.QuoteMeta("SELECT * FROM `goods` WHERE `goods`.`deleted_at` IS NULL LIMIT ?") + "$").WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "category_id", "brands_id", "on_sale", "inventory_ready"}).
			AddRow(421, 1, 1, false, false).
			AddRow(422, 1, 1, false, true).
			AddRow(423, 1, 1, true, false).
			AddRow(424, 1, 1, true, true))
	expectGoodsListRelations(mock)
	response, err := (&handler.GoodsServer{}).GoodsList(context.Background(), &proto.GoodsFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 4 || len(response.Data) != 4 {
		t.Fatalf("default request must keep off-sale and pending goods: %v", response)
	}
	for index, goods := range response.Data {
		if goods.Id != int32(421+index) || goods.OnSale != (index >= 2) || goods.InventoryReady != (index%2 == 1) {
			t.Fatalf("default request changed product state: %v", goods)
		}
	}
}

func TestGoodsListStoreVisibilityEmptyPageRetainsFilteredTotal(t *testing.T) {
	mock := batchGoodsMockDB(t)
	where := " WHERE (on_sale = ? AND inventory_ready = ?) AND `goods`.`deleted_at` IS NULL"
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT count(*) FROM `goods`"+where)+"$").WithArgs(true, true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT * FROM `goods`"+where+" LIMIT ? OFFSET ?")+"$").WithArgs(true, true, 2, 4).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	response, err := (&handler.GoodsServer{}).GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true, Pages: 3, PagePerNums: 2})
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 3 || len(response.Data) != 0 {
		t.Fatalf("out-of-range page must preserve filtered total: %v", response)
	}
}

func TestGoodsListStoreVisibilityDatabaseFailures(t *testing.T) {
	for _, stage := range []string{"category lookup", "count", "goods", "brands", "category preload"} {
		t.Run(stage, func(t *testing.T) {
			mock := batchGoodsMockDB(t)
			request := &proto.GoodsFilterRequest{StoreVisibleOnly: true, Pages: 1, PagePerNums: 10}
			failure := errors.New("private database failure at " + stage)
			if stage == "category lookup" {
				request.TopCategory = 1
				mock.ExpectQuery("SELECT .* FROM `category`").WithArgs(1, 1).WillReturnError(failure)
			} else if stage == "count" {
				mock.ExpectQuery("SELECT count\\(\\*\\) FROM `goods`").WithArgs(true, true).WillReturnError(failure)
			} else {
				mock.ExpectQuery("SELECT count\\(\\*\\) FROM `goods`").WithArgs(true, true).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				query := mock.ExpectQuery("SELECT \\* FROM `goods`").WithArgs(true, true, 10)
				if stage == "goods" {
					query.WillReturnError(failure)
				} else {
					query.WillReturnRows(sqlmock.NewRows([]string{"id", "category_id", "brands_id", "on_sale", "inventory_ready"}).AddRow(421, 1, 1, true, true))
					brands := mock.ExpectQuery("SELECT .* FROM `brands`").WithArgs(1)
					if stage == "brands" {
						brands.WillReturnError(failure)
					} else {
						brands.WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "Test brand"))
						mock.ExpectQuery("SELECT .* FROM `category`").WithArgs(1).WillReturnError(failure)
					}
				}
			}
			response, err := (&handler.GoodsServer{}).GoodsList(context.Background(), request)
			if response != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "查询商品列表失败" {
				t.Fatalf("%s failure must return a generic Internal error, got %v / %v", stage, response, err)
			}
		})
	}
}

func TestGoodsListStoreVisibilityHonorsCanceledContexts(t *testing.T) {
	for _, category := range []int32{0, 1} {
		for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
			t.Run(code.String()+" category="+fmt.Sprint(category), func(t *testing.T) {
				batchGoodsMockDB(t)
				var ctx context.Context
				var cancel context.CancelFunc
				if code == codes.Canceled {
					ctx, cancel = context.WithCancel(context.Background())
					cancel()
				} else {
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				}
				defer cancel()
				response, err := (&handler.GoodsServer{}).GoodsList(ctx, &proto.GoodsFilterRequest{StoreVisibleOnly: true, TopCategory: category})
				if response != nil || status.Code(err) != code {
					t.Fatalf("expired context must stop before SQL, got %v / %v", response, err)
				}
			})
		}
	}
}
