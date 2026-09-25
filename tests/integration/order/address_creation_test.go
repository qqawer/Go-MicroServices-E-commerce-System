//go:build integration

package order_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"grpctest/order_srv/global"
	"grpctest/order_srv/handler"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
	"grpctest/tests/integration/testdb"
)

// The order and AddressBook calls use actual protobuf transport. Address storage
// is a fixture here; user/address integration tests exercise its real MySQL CRUD.
type orderAddressFixture struct {
	proto.UnimplementedAddressBookServer
	mu      sync.Mutex
	address *proto.Address
	calls   int
}

func (f *orderAddressFixture) GetAddress(_ context.Context, req *proto.AddressLookup) (*proto.Address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.address == nil || f.address.Id != req.Id || f.address.UserId != req.UserId {
		return nil, status.Error(codes.NotFound, "收货地址不存在")
	}
	return pb.Clone(f.address).(*proto.Address), nil
}

func (f *orderAddressFixture) change(address *proto.Address) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.address = address
}

func (f *orderAddressFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type addressOrderTest struct {
	db        *gorm.DB
	producer  *creationProducer
	addresses *orderAddressFixture
	ctx       context.Context
	client    proto.OrderClient
}

func addressOrderSetup(t *testing.T) addressOrderTest {
	t.Helper()
	db, producer := creationSetup(t)
	addresses := &orderAddressFixture{address: &proto.Address{Id: 7, UserId: 900120, Name: "原收货人", Mobile: "13000000001", Province: "浙江省", City: "杭州市", District: "西湖区", Address: "原地址 1 号"}}
	ctx, conn := testdb.Client(t, func(server *grpc.Server) {
		proto.RegisterOrderServer(server, &handler.OrderServer{})
		proto.RegisterAddressBookServer(server, addresses)
	})
	old := global.AddressBookClient
	global.AddressBookClient = proto.NewAddressBookClient(conn)
	t.Cleanup(func() { global.AddressBookClient = old })
	return addressOrderTest{db: db, producer: producer, addresses: addresses, ctx: ctx, client: proto.NewOrderClient(conn)}
}

func selectedOrderRequest(key string) *proto.OrderRequest {
	return &proto.OrderRequest{UserId: 900120, AddressId: 7, RequestKey: key, Post: "地址簿测试"}
}

func TestSelectedAddressCreationSnapshotSurvivesEditDeleteAndOutage(t *testing.T) {
	test := addressOrderSetup(t)
	if err := test.db.Create(&model.ShoppingCart{User: 900120, Goods: 421, Nums: 2, Checked: true}).Error; err != nil {
		t.Fatal(err)
	}
	req := selectedOrderRequest("creation-address-snapshot")
	original := pb.Clone(req)
	test.producer.fail = true
	pending, err := test.client.CreateOrderFromAddress(test.ctx, req)
	if err != nil || pending.Status != "CREATING" || test.addresses.count() != 1 {
		t.Fatal("new address intent", pending, err, test.addresses.count())
	}
	var intent model.OrderTransaction
	if err := test.db.Where("order_sn = ?", pending.OrderSn).First(&intent).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot struct{ Order model.OrderInfo }
	if intent.Snapshot == nil || json.Unmarshal([]byte(*intent.Snapshot), &snapshot) != nil || snapshot.Order.Address != "浙江省 杭州市 西湖区 原地址 1 号" || snapshot.Order.SignerName != "原收货人" {
		t.Fatal("durable intent did not capture full shipping information")
	}
	test.addresses.change(&proto.Address{Id: 7, UserId: 900120, Name: "新收货人", Mobile: "13000000002", Province: "江苏省", City: "苏州市", District: "吴中区", Address: "修改后的地址"})
	replayed, err := test.client.CreateOrderFromAddress(test.ctx, req)
	if err != nil || replayed.OrderSn != pending.OrderSn || test.addresses.count() != 1 {
		t.Fatal("editing address must not alter or block existing intent", replayed, err)
	}
	test.addresses.change(nil)
	if _, err := test.client.CreateOrderFromAddress(test.ctx, selectedOrderRequest("creation-address-deleted-new-key")); status.Code(err) != codes.NotFound {
		t.Fatal("deleted address must reject a new intent", err)
	}
	lookupCount := test.addresses.count()
	test.producer.fail = false
	created, err := test.client.CreateOrderFromAddress(test.ctx, req)
	if err != nil || created.Status != "WAIT_BUYER_PAY" || created.OrderSn != pending.OrderSn || created.Address != snapshot.Order.Address || created.Name != "原收货人" || created.Mobile != "13000000001" {
		t.Fatal("deleted address must still replay original shipping snapshot", created, err)
	}
	global.AddressBookClient = nil
	again, err := test.client.CreateOrderFromAddress(test.ctx, req)
	if err != nil || again.Id != created.Id || again.Address != created.Address || test.addresses.count() != lookupCount {
		t.Fatal("address outage blocked previously accepted intent", again, err)
	}
	if !pb.Equal(req, original) {
		t.Fatal("address lookup mutated caller request")
	}
	detail, err := test.client.OrderDetail(test.ctx, &proto.OrderRequest{UserId: req.UserId, Id: created.Id})
	if err != nil || detail.OrderInfo.Address != created.Address || detail.OrderInfo.Name != "原收货人" {
		t.Fatal("historical order should not reread deleted address", detail, err)
	}
}

func TestSelectedAddressCreationRejectsCrossUserAndChangedSelectors(t *testing.T) {
	test := addressOrderSetup(t)
	for _, user := range []int32{900120, 900121} {
		if err := test.db.Create(&model.ShoppingCart{User: user, Goods: 421, Nums: 1, Checked: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	other := selectedOrderRequest("creation-address-other-user")
	other.UserId = 900121
	if _, err := test.client.CreateOrderFromAddress(test.ctx, other); status.Code(err) != codes.NotFound {
		t.Fatal("user must not select another user's address", err)
	}
	var count int64
	if err := test.db.Model(&model.OrderTransaction{}).Where("user_id = ?", other.UserId).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("cross-user attempt created a durable order intent", count, err)
	}
	req := selectedOrderRequest("creation-address-conflict")
	test.producer.fail = true
	accepted, err := test.client.CreateOrderFromAddress(test.ctx, req)
	if err != nil || accepted.Status != "CREATING" {
		t.Fatal(accepted, err)
	}
	lookups := test.addresses.count()
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.AddressId++ },
		func(r *proto.OrderRequest) { r.Post = "改变留言" },
	} {
		changed := pb.Clone(req).(*proto.OrderRequest)
		change(changed)
		if _, err := test.client.CreateOrderFromAddress(test.ctx, changed); status.Code(err) != codes.AlreadyExists {
			t.Fatal("same key accepted different selector", err)
		}
	}
	if test.addresses.count() != lookups {
		t.Fatal("conflicting intent unnecessarily resolved mutable address")
	}
	legacy := creationRequest(req.UserId, req.RequestKey)
	if _, err := test.client.CreateOrder(test.ctx, legacy); status.Code(err) != codes.AlreadyExists {
		t.Fatal("direct-address request reused selected-address key", err)
	}
}

func TestOrderCreationMethodsRemainDistinctOverRPC(t *testing.T) {
	test := addressOrderSetup(t)
	legacy := creationRequest(900120, "creation-address-method")
	legacy.AddressId = 7
	if _, err := test.client.CreateOrder(test.ctx, legacy); status.Code(err) != codes.InvalidArgument {
		t.Fatal("legacy method ignored address_id", err)
	}
	if _, err := test.client.CreateOrderFromAddress(test.ctx, legacy); status.Code(err) != codes.InvalidArgument {
		t.Fatal("selected-address method accepted mixed direct shipping fields", err)
	}
	legacy.AddressId = 0
	if _, err := test.client.CreateOrderFromAddress(test.ctx, legacy); status.Code(err) != codes.InvalidArgument {
		t.Fatal("selected-address method accepted missing selector", err)
	}
	if test.addresses.count() != 0 {
		t.Fatal("invalid inputs reached address RPC")
	}
	// Direct-address orders keep functioning without any AddressBook connection.
	global.AddressBookClient = nil
	if err := test.db.Create(&model.ShoppingCart{User: 900120, Goods: 421, Nums: 1, Checked: true}).Error; err != nil {
		t.Fatal(err)
	}
	created, err := test.client.CreateOrder(test.ctx, legacy)
	if err != nil || created.Status != "WAIT_BUYER_PAY" || created.Address != legacy.Address {
		t.Fatal("address lookup became a dependency of legacy direct checkout", created, err)
	}
}
