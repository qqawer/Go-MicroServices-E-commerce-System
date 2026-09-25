//go:build integration

package user_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	goodsproto "grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	driver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
)

// The collection database is real MySQL; only the independently deployed goods
// dependency is replaced. Calls still cross protobuf and gRPC transport.
type favoriteGoodsService struct {
	goodsproto.UnimplementedGoodsServer
	mu                      sync.Mutex
	products                map[int32]*goodsproto.GoodsInfoResponse
	detailError, batchError error
	detailCalls, batchCalls int
	batchIDs                []int32
}

func (s *favoriteGoodsService) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.products = map[int32]*goodsproto.GoodsInfoResponse{}
	s.detailError, s.batchError = nil, nil
	s.detailCalls, s.batchCalls, s.batchIDs = 0, 0, nil
}

func (s *favoriteGoodsService) product(id int32, response *goodsproto.GoodsInfoResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if response == nil {
		delete(s.products, id)
		return
	}
	s.products[id] = gproto.Clone(response).(*goodsproto.GoodsInfoResponse)
}

func (s *favoriteGoodsService) fail(detail, batch error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailError, s.batchError = detail, batch
}

func (s *favoriteGoodsService) calls() (int, int, []int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detailCalls, s.batchCalls, append([]int32(nil), s.batchIDs...)
}

func (s *favoriteGoodsService) GetGoodsDetail(_ context.Context, request *goodsproto.GoodInfoRequest) (*goodsproto.GoodsInfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailCalls++
	if s.detailError != nil {
		return nil, s.detailError
	}
	product := s.products[request.Id]
	if product == nil {
		return nil, status.Error(codes.NotFound, "test product missing")
	}
	return gproto.Clone(product).(*goodsproto.GoodsInfoResponse), nil
}

func (s *favoriteGoodsService) BatchGetGoods(_ context.Context, request *goodsproto.BatchGoodsIdInfo) (*goodsproto.GoodsListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchCalls++
	s.batchIDs = append([]int32(nil), request.Id...)
	if s.batchError != nil {
		return nil, s.batchError
	}
	// Return ascending goods IDs, independently of favorite insertion order.
	ids := append([]int32(nil), request.Id...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	response := &goodsproto.GoodsListResponse{}
	for _, id := range ids {
		if product := s.products[id]; product != nil {
			response.Data = append(response.Data, gproto.Clone(product).(*goodsproto.GoodsInfoResponse))
		}
	}
	response.Total = int32(len(response.Data))
	return response, nil
}

func TestFavorites(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_user_srv")
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	goods := &favoriteGoodsService{}
	service := &handler.FavoritesServer{}
	_, conn := testdb.Client(t, func(server *grpc.Server) {
		goodsproto.RegisterGoodsServer(server, goods)
		proto.RegisterFavoritesServer(server, service)
	})
	service.Goods = goodsproto.NewGoodsClient(conn)
	client := proto.NewFavoritesClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	requireCode := func(t *testing.T, err error, want codes.Code) {
		t.Helper()
		if status.Code(err) != want {
			t.Fatalf("favorite RPC code=%s, want %s", status.Code(err), want)
		}
	}
	sequence := 0
	owner := func(t *testing.T) int32 {
		t.Helper()
		sequence++
		user := model.User{Mobile: fmt.Sprintf("143%08d", sequence), NickName: "收藏测试用户", Password: "test-only-favorite-fixture"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal("cannot create isolated favorite owner")
		}
		t.Cleanup(func() {
			if err := db.Where("user_id = ?", user.ID).Delete(&model.Favorite{}).Error; err != nil {
				t.Error("cannot remove isolated favorite rows")
			}
			if err := db.Unscoped().Delete(&model.User{}, user.ID).Error; err != nil {
				t.Error("cannot remove isolated favorite owner")
			}
		})
		return user.ID
	}
	product := func(id int32, onSale bool) {
		goods.product(id, &goodsproto.GoodsInfoResponse{Id: id, Name: fmt.Sprintf("商品%d", id), ShopPrice: 12.5, GoodsFrontImage: "https://example.invalid/product.png", OnSale: onSale})
	}
	add := func(t *testing.T, userID, goodsID int32) {
		t.Helper()
		state, err := client.AddFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: goodsID})
		requireCode(t, err, codes.OK)
		if state.GetUserId() != userID || state.GetGoodsId() != goodsID || !state.GetFavorited() {
			t.Fatal("successful favorite must report the persisted owner/product pair")
		}
	}
	checkState := func(t *testing.T, userID, goodsID int32, want bool) {
		t.Helper()
		state, err := client.GetFavoriteStatus(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: goodsID})
		requireCode(t, err, codes.OK)
		if state.GetUserId() != userID || state.GetGoodsId() != goodsID || state.GetFavorited() != want {
			t.Fatal("favorite status does not match the user's persisted relationship")
		}
	}
	count := func(t *testing.T, userID, goodsID int32) int64 {
		t.Helper()
		var value int64
		if err := db.Model(&model.Favorite{}).Where("user_id = ? AND goods_id = ?", userID, goodsID).Count(&value).Error; err != nil {
			t.Fatal("cannot count isolated favorites")
		}
		return value
	}

	t.Run("owners are isolated and hard deletion allows favorite again", func(t *testing.T) {
		goods.reset()
		product(1001, true)
		userA, userB := owner(t), owner(t)
		checkState(t, userA, 1001, false)
		add(t, userA, 1001)
		add(t, userB, 1001)
		list, err := client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userA})
		requireCode(t, err, codes.OK)
		if list.Total != 1 || len(list.Data) != 1 || list.Data[0].UserId != userA || list.Data[0].GoodsId != 1001 || list.Data[0].CreatedAt <= 0 {
			t.Fatal("favorite list leaked another owner or omitted persisted creation time")
		}
		var original model.Favorite
		if err := db.Where("user_id = ? AND goods_id = ?", userA, 1001).First(&original).Error; err != nil {
			t.Fatal("cannot inspect original favorite")
		}
		for i := 0; i < 2; i++ {
			_, err = client.RemoveFavorite(ctx, &proto.FavoriteKey{UserId: userA, GoodsId: 1001})
			requireCode(t, err, codes.OK)
		}
		if count(t, userA, 1001) != 0 || count(t, userB, 1001) != 1 {
			t.Fatal("remove must hard delete only the requesting owner's pair")
		}
		checkState(t, userA, 1001, false)
		checkState(t, userB, 1001, true)
		add(t, userA, 1001)
		var recreated model.Favorite
		if err := db.Where("user_id = ? AND goods_id = ?", userA, 1001).First(&recreated).Error; err != nil || recreated.ID <= original.ID {
			t.Fatal("a removed favorite must be insertable as a new relationship")
		}
	})

	t.Run("fifty concurrent additions and database uniqueness retain one pair", func(t *testing.T) {
		goods.reset()
		product(1002, true)
		userID := owner(t)
		start, results := make(chan struct{}), make(chan error, 50)
		for i := 0; i < 50; i++ {
			go func() {
				<-start
				state, err := client.AddFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: 1002})
				if err == nil && (state.GetUserId() != userID || state.GetGoodsId() != 1002 || !state.GetFavorited()) {
					err = status.Error(codes.Internal, "incorrect concurrent response")
				}
				results <- err
			}()
		}
		close(start)
		completed := make([]error, 0, 50)
		for i := 0; i < 50; i++ {
			completed = append(completed, <-results)
		}
		for _, err := range completed {
			requireCode(t, err, codes.OK)
		}
		if count(t, userID, 1002) != 1 {
			t.Fatal("concurrent favorite insertion duplicated a relationship")
		}
		err := db.Create(&model.Favorite{UserID: userID, GoodsID: 1002}).Error
		var duplicate *driver.MySQLError
		if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
			t.Fatal("real MySQL must independently enforce unique owner/product pairs")
		}
	})

	t.Run("deleted products retain status and idempotent retries without goods calls", func(t *testing.T) {
		goods.reset()
		product(1003, true)
		userID := owner(t)
		add(t, userID, 1003)
		goods.product(1003, nil)
		goods.fail(status.Error(codes.Unavailable, "goods offline"), status.Error(codes.Unavailable, "goods offline"))
		before, batchBefore, _ := goods.calls()
		add(t, userID, 1003)
		checkState(t, userID, 1003, true)
		checkState(t, userID, 1004, false)
		_, err := client.RemoveFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: 1003})
		requireCode(t, err, codes.OK)
		checkState(t, userID, 1003, false)
		after, batchAfter, _ := goods.calls()
		if after != before || batchAfter != batchBefore || count(t, userID, 1003) != 0 {
			t.Fatal("retry, status and removal of existing relationships must not depend on goods availability")
		}
		goods.fail(nil, nil)
		_, err = client.AddFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: 1003})
		requireCode(t, err, codes.NotFound)
		if count(t, userID, 1003) != 0 {
			t.Fatal("a nonexistent product must not create a new favorite")
		}
	})

	t.Run("pagination preserves relationship order and missing or off-sale products", func(t *testing.T) {
		goods.reset()
		userA, userB := owner(t), owner(t)
		for id := int32(1101); id <= 1112; id++ {
			product(id, true)
			add(t, userA, id)
		}
		add(t, userB, 1112)
		goods.product(1111, nil)
		product(1112, false)
		_, before, _ := goods.calls()
		first, err := client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userA})
		requireCode(t, err, codes.OK)
		_, after, requested := goods.calls()
		if first.Total != 12 || len(first.Data) != 10 || after != before+1 || len(requested) != 10 {
			t.Fatal("default pagination must aggregate exactly one page with one batch RPC")
		}
		for i, favorite := range first.Data {
			if favorite.UserId != userA || favorite.GoodsId != 1112-int32(i) {
				t.Fatal("favorite page must keep descending relationship IDs despite unordered goods results")
			}
		}
		if first.Data[0].GoodsMissing || first.Data[0].Goods == nil || first.Data[0].Goods.OnSale || first.Data[0].Goods.Id != 1112 || first.Data[0].Goods.ShopPrice != 12.5 {
			t.Fatal("off-sale product must retain its current product information")
		}
		if !first.Data[1].GoodsMissing || first.Data[1].Goods != nil {
			t.Fatal("deleted product must remain explicitly missing in the owner's favorite list")
		}
		second, err := client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userA, Pn: 2, Psize: 10})
		requireCode(t, err, codes.OK)
		if second.Total != 12 || len(second.Data) != 2 || second.Data[0].GoodsId != 1102 || second.Data[1].GoodsId != 1101 {
			t.Fatal("second page must preserve the total including missing products")
		}
		_, before, _ = goods.calls()
		for _, req := range []*proto.FavoriteListRequest{{UserId: userA, Pn: 3, Psize: 10}, {UserId: owner(t)}} {
			page, err := client.ListFavorites(ctx, req)
			requireCode(t, err, codes.OK)
			if len(page.Data) != 0 {
				t.Fatal("empty favorite page unexpectedly returned rows")
			}
		}
		_, after, _ = goods.calls()
		if before != after {
			t.Fatal("empty favorite pages must not issue a goods batch query")
		}
		maximum, err := client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userA, Psize: 100})
		requireCode(t, err, codes.OK)
		if maximum.Total != 12 || len(maximum.Data) != 12 {
			t.Fatal("maximum allowed page size must be accepted")
		}
	})

	t.Run("goods failures do not create rows or masquerade as deleted products", func(t *testing.T) {
		goods.reset()
		userID := owner(t)
		product(1201, true)
		add(t, userID, 1201)
		goods.fail(status.Error(codes.Unavailable, "goods offline"), status.Error(codes.Unavailable, "goods offline"))
		_, err := client.AddFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: 1202})
		requireCode(t, err, codes.Unavailable)
		_, err = client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userID})
		requireCode(t, err, codes.Unavailable)
		if count(t, userID, 1201) != 1 || count(t, userID, 1202) != 0 {
			t.Fatal("goods errors changed existing relationships or persisted an unvalidated product")
		}
		goods.fail(nil, nil)
		goods.product(1203, &goodsproto.GoodsInfoResponse{Id: 1299})
		_, err = client.AddFavorite(ctx, &proto.FavoriteKey{UserId: userID, GoodsId: 1203})
		requireCode(t, err, codes.Internal)
		if count(t, userID, 1203) != 0 {
			t.Fatal("mismatched product response must not be persisted")
		}
	})

	t.Run("invalid keys pagination and inactive owners cannot create relationships", func(t *testing.T) {
		goods.reset()
		product(1301, true)
		userID := owner(t)
		for _, request := range []*proto.FavoriteKey{{UserId: 0, GoodsId: 1301}, {UserId: -1, GoodsId: 1301}, {UserId: userID, GoodsId: 0}, {UserId: userID, GoodsId: -1}} {
			_, err := client.AddFavorite(ctx, request)
			requireCode(t, err, codes.InvalidArgument)
			_, err = client.GetFavoriteStatus(ctx, request)
			requireCode(t, err, codes.InvalidArgument)
			_, err = client.RemoveFavorite(ctx, request)
			requireCode(t, err, codes.InvalidArgument)
		}
		for _, request := range []*proto.FavoriteListRequest{
			{UserId: 0}, {UserId: -1}, {UserId: userID, Pn: -1}, {UserId: userID, Psize: -1},
			{UserId: userID, Psize: 101}, {UserId: userID, Pn: math.MaxInt32, Psize: 100},
		} {
			_, err := client.ListFavorites(ctx, request)
			requireCode(t, err, codes.InvalidArgument)
		}
		deletedOwner := owner(t)
		if err := db.Delete(&model.User{}, deletedOwner).Error; err != nil {
			t.Fatal("cannot prepare inactive favorite owner")
		}
		for _, id := range []int32{deletedOwner, math.MaxInt32} {
			_, err := client.AddFavorite(ctx, &proto.FavoriteKey{UserId: id, GoodsId: 1301})
			requireCode(t, err, codes.NotFound)
			if count(t, id, 1301) != 0 {
				t.Fatal("favorite for an inactive owner must not persist")
			}
		}
		if count(t, userID, 1301) != 0 {
			t.Fatal("invalid requests changed the valid owner's favorites")
		}
		// A very large but still representable offset is valid and empty.
		page, err := client.ListFavorites(ctx, &proto.FavoriteListRequest{UserId: userID, Pn: math.MaxInt32, Psize: 1})
		requireCode(t, err, codes.OK)
		if page.Total != 0 || len(page.Data) != 0 {
			t.Fatal("valid maximum offset should return an empty page")
		}
	})
}
