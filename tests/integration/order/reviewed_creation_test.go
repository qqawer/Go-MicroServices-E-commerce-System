//go:build integration

package order_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

type reviewedGoods struct {
	proto.GoodsClient
	price                      float32
	fail                       bool
	missing, offSale, notReady bool
	calls                      atomic.Int32
}

func (f *reviewedGoods) BatchGetGoods(context.Context, *proto.BatchGoodsIdInfo, ...grpc.CallOption) (*proto.GoodsListResponse, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, status.Error(codes.Unavailable, "goods fixture unavailable")
	}
	if f.missing {
		return &proto.GoodsListResponse{}, nil
	}
	return &proto.GoodsListResponse{Data: []*proto.GoodsInfoResponse{{Id: 421, Name: "reviewed goods", ShopPrice: f.price, OnSale: !f.offSale, InventoryReady: !f.notReady}}}, nil
}

type reviewedMQ struct {
	rocketmq.TransactionProducer
	calls atomic.Int32
}

func (f *reviewedMQ) SendMessageInTransaction(ctx context.Context, msg *primitive.Message) (*primitive.TransactionSendResult, error) {
	f.calls.Add(1)
	return f.TransactionProducer.SendMessageInTransaction(ctx, msg)
}

func reviewedFixture(t *testing.T, key string) (addressOrderTest, model.ShoppingCart, *proto.OrderRequest, *reviewedGoods, *reviewedMQ) {
	t.Helper()
	f := addressOrderSetup(t)
	row := model.ShoppingCart{User: 900120, Goods: 421, Nums: 2, Checked: true}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	req := selectedOrderRequest(key)
	req.CheckoutItems = []*proto.CheckoutItem{{CartId: row.ID, GoodsId: row.Goods, Nums: row.Nums, UnitCents: 4490}}
	goods := &reviewedGoods{price: 44.9}
	mq := &reviewedMQ{TransactionProducer: f.producer}
	global.GoodsSrvClient, global.MQTransactions = goods, mq
	return f, row, req, goods, mq
}

func assertRejectedReviewHasNoEffects(t *testing.T, f addressOrderTest, key string, mq *reviewedMQ) {
	t.Helper()
	var intents, orders int64
	if err := f.db.Model(&model.OrderTransaction{}).Where("user_id = ? AND request_key = ?", 900120, key).Count(&intents).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.OrderInfo{}).Where("user = ?", 900120).Count(&orders).Error; err != nil {
		t.Fatal(err)
	}
	stock := global.InventorySrvClient.(*transactionStock)
	stock.mu.Lock()
	stockCalls := stock.calls
	stock.mu.Unlock()
	if intents != 0 || orders != 0 || stockCalls != 0 || mq.calls.Load() != 0 {
		t.Fatalf("rejected review had side effects: intents=%d orders=%d stock_calls=%d mq_calls=%d", intents, orders, stockCalls, mq.calls.Load())
	}
}

func TestReviewedCreationRejectsChangedCheckoutBeforeAnySideEffects(t *testing.T) {
	for _, mutation := range []string{"quantity", "goods", "unchecked", "deleted", "extra-checked", "wrong-row", "price", "invalid-price", "missing-goods", "off-sale", "not-ready"} {
		t.Run(mutation, func(t *testing.T) {
			f, row, req, goods, mq := reviewedFixture(t, "creation-reviewed-"+mutation)
			var err error
			switch mutation {
			case "quantity":
				err = f.db.Model(&row).Update("nums", 3).Error
			case "goods":
				err = f.db.Model(&row).Update("goods", 422).Error
			case "unchecked":
				err = f.db.Model(&row).Update("checked", false).Error
			case "deleted":
				err = f.db.Delete(&row).Error
			case "extra-checked":
				err = f.db.Create(&model.ShoppingCart{User: row.User, Goods: 421, Nums: 1, Checked: true}).Error
			case "wrong-row":
				req.CheckoutItems[0].CartId++
			case "price":
				goods.price = 44.91
			case "invalid-price":
				goods.price = 44.901
			case "missing-goods":
				goods.missing = true
			case "off-sale":
				goods.offSale = true
			case "not-ready":
				goods.notReady = true
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.client.CreateReviewedOrderFromAddress(f.ctx, req)
			if status.Code(err) != codes.Aborted || status.Convert(err).Message() != "结算商品已变化，请重新确认" {
				t.Fatal("changed checkout was not rejected", err)
			}
			if mutation != "price" && mutation != "invalid-price" && mutation != "missing-goods" && mutation != "off-sale" && mutation != "not-ready" && goods.calls.Load() != 0 {
				t.Fatal("changed cart reached goods RPC")
			}
			assertRejectedReviewHasNoEffects(t, f, req.RequestKey, mq)
		})
	}
}

func TestReviewedCreationReplaySurvivesClearedCartAndMissingDependencies(t *testing.T) {
	f, row, req, goods, mq := reviewedFixture(t, "creation-reviewed-replay")
	unchecked := model.ShoppingCart{User: row.User, Goods: 421, Nums: 9, Checked: false}
	if err := f.db.Create(&unchecked).Error; err != nil {
		t.Fatal(err)
	}
	created, err := f.client.CreateReviewedOrderFromAddress(f.ctx, req)
	if err != nil || created.Id <= 0 || created.TotalCents != 8980 || created.Status != "WAIT_BUYER_PAY" {
		t.Fatal(created, err)
	}
	var active int64
	if err := f.db.Model(&model.ShoppingCart{}).Where("id = ?", row.ID).Count(&active).Error; err != nil || active != 0 {
		t.Fatal("reviewed checked row not consumed", active, err)
	}
	if err := f.db.Model(&model.ShoppingCart{}).Where("id = ?", unchecked.ID).Count(&active).Error; err != nil || active != 1 {
		t.Fatal("unchecked cart row was consumed", active, err)
	}
	newKey := pb.Clone(req).(*proto.OrderRequest)
	newKey.RequestKey += "-new-key"
	beforeMQ := mq.calls.Load()
	if _, err := f.client.CreateReviewedOrderFromAddress(f.ctx, newKey); status.Code(err) != codes.Aborted {
		t.Fatal("new key bought cleared rows again", err)
	}
	if mq.calls.Load() != beforeMQ {
		t.Fatal("cleared cart published another transaction")
	}
	goods.fail = true
	f.addresses.change(nil)
	global.AddressBookClient = nil
	lookups, goodsCalls := f.addresses.count(), goods.calls.Load()
	again, err := f.client.CreateReviewedOrderFromAddress(f.ctx, req)
	if err != nil || again.Id != created.Id || again.TotalCents != created.TotalCents || f.addresses.count() != lookups || goods.calls.Load() != goodsCalls {
		t.Fatal("replay depended on mutable cart/address/goods", again, err)
	}
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.AddressId++ },
		func(r *proto.OrderRequest) { r.Post += "改" },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].CartId++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].GoodsId++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].Nums++ },
		func(r *proto.OrderRequest) { r.CheckoutItems[0].UnitCents++ },
	} {
		changed := pb.Clone(req).(*proto.OrderRequest)
		change(changed)
		if _, err := f.client.CreateReviewedOrderFromAddress(f.ctx, changed); status.Code(err) != codes.AlreadyExists {
			t.Fatal("changed confirmation reused key", err)
		}
	}
	other := pb.Clone(req).(*proto.OrderRequest)
	other.UserId++
	if _, err := f.client.GetOrderCreation(f.ctx, other); status.Code(err) != codes.NotFound {
		t.Fatal("request result leaked across accounts", err)
	}
	if _, err := f.client.OrderDetail(f.ctx, &proto.OrderRequest{UserId: other.UserId, Id: created.Id}); status.Code(err) != codes.NotFound {
		t.Fatal("order detail leaked across accounts", err)
	}
}

func TestReviewedCreationPendingSnapshotReplaysConcurrently(t *testing.T) {
	f, row, req, goods, _ := reviewedFixture(t, "creation-reviewed-concurrent-replay")
	second := model.ShoppingCart{User: row.User, Goods: row.Goods, Nums: 1, Checked: true}
	if err := f.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	req.CheckoutItems = append(req.CheckoutItems, &proto.CheckoutItem{CartId: second.ID, GoodsId: second.Goods, Nums: second.Nums, UnitCents: 4490})
	original := pb.Clone(req)
	f.producer.fail = true
	pending, err := f.client.CreateReviewedOrderFromAddress(f.ctx, req)
	if err != nil || pending.Status != "CREATING" {
		t.Fatal(pending, err)
	}
	if err := f.db.Model(&row).Update("nums", 8).Error; err != nil {
		t.Fatal(err)
	}
	goods.fail = true
	f.addresses.change(nil)
	f.producer.fail = false
	lookups, goodsCalls := f.addresses.count(), goods.calls.Load()
	// Reordered input is the same canonical confirmation. All callers must replay
	// the durable snapshot, not the changed cart, deleted address, or failed goods RPC.
	reordered := pb.Clone(req).(*proto.OrderRequest)
	reordered.CheckoutItems[0], reordered.CheckoutItems[1] = reordered.CheckoutItems[1], reordered.CheckoutItems[0]
	ids := concurrentReviewedOrders(t, f, reordered, 12)
	if f.addresses.count() != lookups || goods.calls.Load() != goodsCalls || !pb.Equal(req, original) {
		t.Fatal("replay reloaded mutable inputs or mutated caller request")
	}
	var orders int64
	if err := f.db.Model(&model.OrderInfo{}).Where("order_sn = ?", pending.OrderSn).Count(&orders).Error; err != nil || orders != 1 {
		t.Fatal("concurrent replay did not converge to one order", orders, err)
	}
	var item model.OrderGoods
	if err := f.db.Where("`order` = ?", ids[0]).First(&item).Error; err != nil || item.Nums != 3 || item.UnitCents == nil || *item.UnitCents != 4490 {
		t.Fatal("duplicate-goods rows did not preserve reviewed total", item, err)
	}
	var remaining model.ShoppingCart
	if err := f.db.First(&remaining, row.ID).Error; err != nil || remaining.Nums != 8 {
		t.Fatal("later cart edit was deleted", remaining, err)
	}
	var intent model.OrderTransaction
	if err := f.db.Where("order_sn = ?", pending.OrderSn).First(&intent).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot struct{ Order model.OrderInfo }
	if intent.Snapshot == nil || json.Unmarshal([]byte(*intent.Snapshot), &snapshot) != nil || snapshot.Order.SignerName != "原收货人" {
		t.Fatal("original address snapshot lost")
	}
}

func TestReviewedCreationConcurrentFirstSubmissionAndMethodSeparation(t *testing.T) {
	f, _, req, _, mq := reviewedFixture(t, "creation-reviewed-concurrent-first")
	if _, err := f.client.CreateOrderFromAddress(f.ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("old address RPC ignored confirmation", err)
	}
	raw := creationRequest(req.UserId, req.RequestKey)
	raw.CheckoutItems = req.CheckoutItems
	if _, err := f.client.CreateOrder(f.ctx, raw); status.Code(err) != codes.InvalidArgument {
		t.Fatal("old raw RPC ignored confirmation", err)
	}
	missing := pb.Clone(req).(*proto.OrderRequest)
	missing.CheckoutItems = nil
	if _, err := f.client.CreateReviewedOrderFromAddress(f.ctx, missing); status.Code(err) != codes.InvalidArgument {
		t.Fatal("reviewed RPC accepted no confirmation", err)
	}
	assertRejectedReviewHasNoEffects(t, f, req.RequestKey, mq)
	concurrentReviewedOrders(t, f, req, 12)
	var intents, orders int64
	if err := f.db.Model(&model.OrderTransaction{}).Where("user_id = ? AND request_key = ?", req.UserId, req.RequestKey).Count(&intents).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.OrderInfo{}).Where("user = ?", req.UserId).Count(&orders).Error; err != nil || intents != 1 || orders != 1 {
		t.Fatal("first concurrent submission duplicated durable state", intents, orders, err)
	}
}

func concurrentReviewedOrders(t *testing.T, f addressOrderTest, req *proto.OrderRequest, count int) []int32 {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	ids := make([]int32, count)
	errs := make(chan error, count)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			result, err := f.client.CreateReviewedOrderFromAddress(f.ctx, req)
			if err != nil {
				errs <- err
				return
			}
			if result.Id <= 0 || result.Status != "WAIT_BUYER_PAY" {
				errs <- errors.New("reviewed result did not become an order")
				return
			}
			ids[i] = result.Id
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, id := range ids {
		if id <= 0 || id != ids[0] {
			t.Fatal(fmt.Sprintf("same-key responses diverged: %v", ids))
		}
	}
	return ids
}
