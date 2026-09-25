package handler

import (
	"context"
	"errors"
	"math"
	"reflect"
	"regexp"
	"testing"
	"time"

	goodsproto "grpctest/goods_srv/model/proto"
	"grpctest/user_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type favoriteGoodsStub struct {
	goodsproto.GoodsClient
	detail func(context.Context, *goodsproto.GoodInfoRequest) (*goodsproto.GoodsInfoResponse, error)
	batch  func(context.Context, *goodsproto.BatchGoodsIdInfo) (*goodsproto.GoodsListResponse, error)
}

func (s favoriteGoodsStub) GetGoodsDetail(ctx context.Context, req *goodsproto.GoodInfoRequest, _ ...grpc.CallOption) (*goodsproto.GoodsInfoResponse, error) {
	return s.detail(ctx, req)
}

func (s favoriteGoodsStub) BatchGetGoods(ctx context.Context, req *goodsproto.BatchGoodsIdInfo, _ ...grpc.CallOption) (*goodsproto.GoodsListResponse, error) {
	return s.batch(ctx, req)
}

func favoriteLookup(mock sqlmock.Sqlmock, found bool) {
	rows := sqlmock.NewRows([]string{"id"})
	if found {
		rows.AddRow(17)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `user_favorite` WHERE user_id = ? AND goods_id = ? LIMIT ?")).WithArgs(41, 421, 1).WillReturnRows(rows)
}

func TestFavoritesRejectInvalidIdentityBeforeDatabaseAccess(t *testing.T) {
	profileMockDB(t)
	server := &FavoritesServer{}
	for _, req := range []*proto.FavoriteKey{nil, {}, {UserId: 41}, {UserId: -1, GoodsId: 421}, {UserId: 41, GoodsId: -1}} {
		for _, method := range []func(context.Context, *proto.FavoriteKey) error{
			func(ctx context.Context, key *proto.FavoriteKey) error {
				_, err := server.AddFavorite(ctx, key)
				return err
			},
			func(ctx context.Context, key *proto.FavoriteKey) error {
				_, err := server.GetFavoriteStatus(ctx, key)
				return err
			},
			func(ctx context.Context, key *proto.FavoriteKey) error {
				_, err := server.RemoveFavorite(ctx, key)
				return err
			},
		} {
			if status.Code(method(context.Background(), req)) != codes.InvalidArgument {
				t.Fatal("invalid identity must fail before database access")
			}
		}
	}
	for _, req := range []*proto.FavoriteListRequest{nil, {}, {UserId: 41, Pn: -1}, {UserId: 41, Psize: 101}, {UserId: 41, Pn: math.MaxInt32, Psize: 100}} {
		if _, err := server.ListFavorites(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal("invalid list owner or page must fail before database access")
		}
	}
}

func TestFavoriteRetryAndRemovalDoNotRequireGoods(t *testing.T) {
	mock := profileMockDB(t)
	server := &FavoritesServer{}
	favoriteLookup(mock, true)
	state, err := server.AddFavorite(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421})
	if err != nil || !state.Favorited || state.UserId != 41 || state.GoodsId != 421 {
		t.Fatalf("repeat add must succeed without a goods client: %v", err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `user_favorite` WHERE user_id = ? AND goods_id = ?")).WithArgs(41, 421).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if _, err := server.RemoveFavorite(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421}); err != nil {
		t.Fatalf("removing an absent favorite must succeed: %v", err)
	}
}

func TestFavoriteDatabaseFailuresDoNotBecomeSuccess(t *testing.T) {
	failure := errors.New("private database detail")
	for _, method := range []string{"list", "status", "remove", "insert", "commit"} {
		t.Run(method, func(t *testing.T) {
			mock := profileMockDB(t)
			server := &FavoritesServer{Goods: favoriteGoodsStub{detail: func(context.Context, *goodsproto.GoodInfoRequest) (*goodsproto.GoodsInfoResponse, error) {
				return &goodsproto.GoodsInfoResponse{Id: 421}, nil
			}}}
			var err error
			switch method {
			case "list":
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT count.*FROM `user_favorite` WHERE user_id = \\?").WithArgs(41).WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.ListFavorites(context.Background(), &proto.FavoriteListRequest{UserId: 41})
			case "status":
				mock.ExpectQuery("SELECT `id` FROM `user_favorite` WHERE user_id = \\? AND goods_id = \\?").WithArgs(41, 421, 1).WillReturnError(failure)
				_, err = server.GetFavoriteStatus(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421})
			case "remove":
				mock.ExpectBegin()
				mock.ExpectExec("DELETE FROM `user_favorite` WHERE user_id = \\? AND goods_id = \\?").WithArgs(41, 421).WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.RemoveFavorite(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421})
			case "insert", "commit":
				favoriteLookup(mock, false)
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT `id` FROM `user`.*FOR SHARE").WithArgs(41, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
				insert := mock.ExpectExec("INSERT INTO `user_favorite`.*ON DUPLICATE KEY UPDATE `id`=`id`").WithArgs(41, 421, sqlmock.AnyArg())
				if method == "insert" {
					insert.WillReturnError(failure)
					mock.ExpectRollback()
				} else {
					insert.WillReturnResult(sqlmock.NewResult(17, 1))
					mock.ExpectCommit().WillReturnError(failure)
				}
				_, err = server.AddFavorite(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421})
			}
			if status.Code(err) != codes.Internal || status.Convert(err).Message() != "收藏操作失败" {
				t.Fatalf("expected safe Internal error, got %v", err)
			}
		})
	}
}

func TestAddFavoriteValidatesGoodsAndBoundsItsDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		data *goodsproto.GoodsInfoResponse
		err  error
		want codes.Code
	}{
		{name: "missing", err: status.Error(codes.NotFound, "private detail"), want: codes.NotFound},
		{name: "unavailable", err: status.Error(codes.Unavailable, "private detail"), want: codes.Unavailable},
		{name: "nil data", want: codes.Internal},
		{name: "different goods", data: &goodsproto.GoodsInfoResponse{Id: 422}, want: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := profileMockDB(t)
			favoriteLookup(mock, false)
			server := &FavoritesServer{Goods: favoriteGoodsStub{detail: func(ctx context.Context, req *goodsproto.GoodInfoRequest) (*goodsproto.GoodsInfoResponse, error) {
				deadline, exists := ctx.Deadline()
				if req.Id != 421 || !exists || time.Until(deadline) > 3*time.Second {
					t.Fatal("goods lookup must use the requested ID and a bounded deadline")
				}
				return tc.data, tc.err
			}}}
			_, err := server.AddFavorite(context.Background(), &proto.FavoriteKey{UserId: 41, GoodsId: 421})
			if status.Code(err) != tc.want {
				t.Fatalf("code=%s want=%s", status.Code(err), tc.want)
			}
		})
	}
}

func expectFavoritePage(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `user_favorite` WHERE user_id = ?")).WithArgs(41).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `user_favorite` WHERE user_id = ? ORDER BY id DESC LIMIT ?")).WithArgs(41, 10).WillReturnRows(rows)
	mock.ExpectCommit()
}

func TestFavoriteListReleasesDatabaseBeforeBatchAndPreservesMissingGoods(t *testing.T) {
	mock := profileMockDB(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expectFavoritePage(mock, sqlmock.NewRows([]string{"id", "user_id", "goods_id", "created_at"}).AddRow(18, 41, 422, created).AddRow(17, 41, 421, created))
	server := &FavoritesServer{Goods: favoriteGoodsStub{batch: func(ctx context.Context, req *goodsproto.BatchGoodsIdInfo) (*goodsproto.GoodsListResponse, error) {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("database transaction must commit before remote lookup: %v", err)
		}
		if !reflect.DeepEqual(req.Id, []int32{422, 421}) {
			t.Fatalf("one batch must contain this page's goods: %v", req.Id)
		}
		return &goodsproto.GoodsListResponse{Data: []*goodsproto.GoodsInfoResponse{{Id: 421, Name: "测试商品", ShopPrice: 10, OnSale: false}}}, nil
	}}}
	response, err := server.ListFavorites(context.Background(), &proto.FavoriteListRequest{UserId: 41})
	if err != nil || response.Total != 2 || len(response.Data) != 2 {
		t.Fatalf("unexpected favorite page: %v / %v", response, err)
	}
	if response.Data[0].GoodsId != 422 || !response.Data[0].GoodsMissing || response.Data[0].Goods != nil || response.Data[0].CreatedAt != created.Unix() {
		t.Fatal("missing goods must retain the favorite relation and creation time")
	}
	if response.Data[1].Goods == nil || response.Data[1].GoodsMissing || response.Data[1].Goods.OnSale || response.Data[1].Goods.Name != "测试商品" {
		t.Fatal("off-sale goods still have readable product information")
	}
}

func TestFavoriteListDoesNotMarkDependencyFailuresAsDeletedGoods(t *testing.T) {
	for _, tc := range []struct {
		name string
		data *goodsproto.GoodsListResponse
		err  error
		want codes.Code
	}{
		{name: "outage", err: status.Error(codes.Unavailable, "private detail"), want: codes.Unavailable},
		{name: "nil response", want: codes.Internal},
		{name: "nil goods", data: &goodsproto.GoodsListResponse{Data: []*goodsproto.GoodsInfoResponse{nil}}, want: codes.Internal},
		{name: "wrong goods", data: &goodsproto.GoodsListResponse{Data: []*goodsproto.GoodsInfoResponse{{Id: 9}}}, want: codes.Internal},
		{name: "duplicate goods", data: &goodsproto.GoodsListResponse{Data: []*goodsproto.GoodsInfoResponse{{Id: 421}, {Id: 421}}}, want: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := profileMockDB(t)
			expectFavoritePage(mock, sqlmock.NewRows([]string{"id", "user_id", "goods_id"}).AddRow(17, 41, 421))
			server := &FavoritesServer{Goods: favoriteGoodsStub{batch: func(context.Context, *goodsproto.BatchGoodsIdInfo) (*goodsproto.GoodsListResponse, error) {
				return tc.data, tc.err
			}}}
			result, err := server.ListFavorites(context.Background(), &proto.FavoriteListRequest{UserId: 41})
			if result != nil || status.Code(err) != tc.want {
				t.Fatalf("dependency failure must fail the page, got %v / %v", result, err)
			}
		})
	}
}

func TestFavoriteEmptyPageDoesNotRequireGoods(t *testing.T) {
	mock := profileMockDB(t)
	expectFavoritePage(mock, sqlmock.NewRows([]string{"id", "user_id", "goods_id"}))
	response, err := (&FavoritesServer{}).ListFavorites(context.Background(), &proto.FavoriteListRequest{UserId: 41})
	if err != nil || response.Total != 2 || response.Data == nil || len(response.Data) != 0 {
		t.Fatalf("empty page must not require goods service: %v / %v", response, err)
	}
}
