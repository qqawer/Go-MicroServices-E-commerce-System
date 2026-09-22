//go:build integration

package goods_test

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"
	invglobal "grpctest/inventory_srv/global"
	invhandler "grpctest/inventory_srv/handler"
	invproto "grpctest/inventory_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
)

func creationClient(t *testing.T) (context.Context, proto.GoodsClient, invproto.InventoryClient, *handler.GoodsServer, *proto.CreateGoodsInfo) {
	t.Helper()
	previousGoods, previousInventory := global.DB, invglobal.DB
	goodsDB, inventoryDB := testdb.Open(t, "grpctest_goods_srv"), testdb.Open(t, "grpctest_inventory_srv")
	global.DB, invglobal.DB = goodsDB, inventoryDB
	key := fmt.Sprintf("creation-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		var ids []int32
		goodsDB.Table("goods_creation").Where("request_key = ?", key).Pluck("goods_id", &ids)
		for _, id := range ids {
			inventoryDB.Exec("DELETE FROM inventory_initialization WHERE goods = ?", id)
			inventoryDB.Exec("DELETE FROM inventory WHERE goods = ?", id)
			goodsDB.Exec("DELETE FROM goods WHERE id = ?", id)
		}
		goodsDB.Exec("DELETE FROM goods_creation WHERE request_key = ?", key)
		global.DB, invglobal.DB = previousGoods, previousInventory
	})
	server := &handler.GoodsServer{}
	ctx, conn := testdb.Client(t, func(grpcServer *grpc.Server) {
		proto.RegisterGoodsServer(grpcServer, server)
		invproto.RegisterInventoryServer(grpcServer, &invhandler.InventoryServer{})
	})
	inventory := invproto.NewInventoryClient(conn)
	server.Inventory = inventory
	req := &proto.CreateGoodsInfo{
		RequestKey: key, Name: key, GoodsSn: "CREATE-TEST", CategoryId: 1, BrandId: 1,
		Stocks: 100, MarketPrice: 20, ShopPrice: 10, GoodsBrief: "Creation fixture",
		Images: []string{"https://example.invalid/goods.png"}, DescImages: []string{},
		GoodsFrontImage: "https://example.invalid/goods.png", OnSale: true,
	}
	return ctx, proto.NewGoodsClient(conn), inventory, server, req
}

func TestCreateGoodsRetryPreservesCurrentInventory(t *testing.T) {
	ctx, client, inventory, _, req := creationClient(t)
	created, err := client.CreateGoods(ctx, req)
	if err != nil || created.GetId() <= 0 || !created.GetInventoryReady() {
		t.Fatalf("create must persist goods and initialize inventory: %v, %v", created, err)
	}
	stock, err := client.GetGoodsInventory(ctx, &proto.GoodInfoRequest{Id: created.Id})
	if err != nil || !stock.GetReady() || stock.GetStocks() != 100 {
		t.Fatalf("new stock: %v, %v", stock, err)
	}
	if _, err := inventory.SetInv(ctx, &invproto.GoodsInventoryInfo{GoodsId: created.Id, Num: 90}); err != nil {
		t.Fatal(err)
	}
	retry, err := client.CreateGoods(ctx, req)
	if err != nil || retry.GetId() != created.Id || !retry.GetInventoryReady() {
		t.Fatalf("retry created another product: %v, %v", retry, err)
	}
	stock, err = client.GetGoodsInventory(ctx, &proto.GoodInfoRequest{Id: created.Id})
	if err != nil || stock.GetStocks() != 90 {
		t.Fatalf("retry reset live inventory: %v, %v", stock, err)
	}
	list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{KeyWords: req.Name, Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 1 {
		t.Fatalf("retry duplicated product rows: %v, %v", list, err)
	}
}

// Commit in inventory, then lose the reply before goods can confirm readiness.
type lostReplyInventory struct {
	invproto.InventoryClient
	lost atomic.Bool
}

func (i *lostReplyInventory) InitInv(ctx context.Context, req *invproto.GoodsInventoryInfo, opts ...grpc.CallOption) (*invproto.GoodsInventoryInfo, error) {
	result, err := i.InventoryClient.InitInv(ctx, req, opts...)
	if err == nil && !i.lost.Swap(true) {
		return nil, status.Error(codes.Unavailable, "injected lost reply after commit")
	}
	return result, err
}

func TestCreateGoodsResumesAfterLostInventoryReply(t *testing.T) {
	ctx, client, inventory, server, req := creationClient(t)
	server.Inventory = &lostReplyInventory{InventoryClient: inventory}
	pending, err := client.CreateGoods(ctx, req)
	if err != nil || pending.GetId() <= 0 || pending.GetInventoryReady() || pending.GetOnSale() {
		t.Fatalf("pending product must stay off sale: %v, %v", pending, err)
	}
	_, err = client.UpdateGoodsStatus(ctx, &proto.UpdateGoodsStatusRequest{Id: pending.Id, OnSale: gproto.Bool(true)})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("pending product was made saleable: %v", err)
	}
	stock, err := client.GetGoodsInventory(ctx, &proto.GoodInfoRequest{Id: pending.Id})
	if err != nil || stock.GetReady() {
		t.Fatalf("pending stock should not claim readiness: %v %v", stock, err)
	}
	if _, err := inventory.SetInv(ctx, &invproto.GoodsInventoryInfo{GoodsId: pending.Id, Num: 90}); err != nil {
		t.Fatal(err)
	}
	// A fresh handler has no request cache: recovery depends on persisted records.
	fresh := &handler.GoodsServer{Inventory: inventory}
	ready, err := fresh.CreateGoods(ctx, req)
	if err != nil || ready.GetId() != pending.Id || !ready.GetInventoryReady() || !ready.GetOnSale() {
		t.Fatalf("failed to resume: %v %v", ready, err)
	}
	stock, err = client.GetGoodsInventory(ctx, &proto.GoodInfoRequest{Id: ready.Id})
	if err != nil || stock.GetStocks() != 90 {
		t.Fatalf("retry overwrote current stock: %v %v", stock, err)
	}
	if _, err = client.UpdateGoodsStatus(ctx, &proto.UpdateGoodsStatusRequest{Id: ready.Id, OnSale: gproto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	retry, err := client.CreateGoods(ctx, req)
	if err != nil || retry.GetOnSale() {
		t.Fatalf("retry restored old sale state: %v %v", retry, err)
	}
}

func TestCreateGoodsConcurrentSameKeyAndConflictingBody(t *testing.T) {
	ctx, client, _, _, req := creationClient(t)
	var wg sync.WaitGroup
	results := make(chan *proto.GoodsInfoResponse, 6)
	errors := make(chan error, 6)
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := client.CreateGoods(ctx, req); results <- r; errors <- e }()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id int32
	for result := range results {
		if result.GetId() <= 0 || !result.GetInventoryReady() || (id != 0 && result.GetId() != id) {
			t.Fatalf("duplicate or incomplete product: %v", result)
		}
		id = result.Id
	}
	conflicting := gproto.Clone(req).(*proto.CreateGoodsInfo)
	conflicting.Stocks++
	_, err := client.CreateGoods(ctx, conflicting)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("same key with different body must conflict: %v", err)
	}
	list, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{KeyWords: req.Name, Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 1 {
		t.Fatalf("concurrent request duplicated rows: %v %v", list, err)
	}
}

func TestCreateGoodsDatabaseFailureDoesNotReportSuccess(t *testing.T) {
	ctx, client, _, _, req := creationClient(t)
	req.Name = string(make([]byte, 501)) // Exceeds the real schema and exercises INSERT failure/rollback.
	_, err := client.CreateGoods(ctx, req)
	if status.Code(err) != codes.Internal {
		t.Fatalf("failed INSERT reported success: %v", err)
	}
	var count int64
	if err := global.DB.Table("goods_creation").Where("request_key=?", req.RequestKey).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed product insert left a committed request record")
	}
}
